"""Complete the specific AJAX step found in the official WFU callback HTML."""
import json
import re
from urllib.parse import urlsplit

FIELDS = ('data', 'time', 'enc', 'displayName', 'userRole')

def callback_params(html):
    url = re.search(r'var\s+url\s*=\s*("(?:\\.|[^"\\])*")', html)
    if not url or json.loads(url.group(1)) != '/OAuth2/wfu/login':
        raise ValueError('当前响应不是已确认的 WFU 登录回调模板')
    block = re.search(r'\bdata\s*:\s*\{(.*?)\}\s*,\s*dataType', html, re.S)
    if not block:
        raise ValueError('未找到回调 AJAX 参数块')
    result = {}
    for field in FIELDS:
        match = re.search(r'(?<!\w)' + field + r'\s*:\s*("(?:\\.|[^"\\])*"|-?\d+)(?=\s*[,}])', block.group(1) + '}', re.S)
        if not match:
            raise ValueError(f'回调参数缺失: {field}')
        result[field] = json.loads(match.group(1))
    if any('[REDACTED]' in str(v) for v in result.values()):
        raise ValueError('不能使用脱敏文件，请保留原内核的 login.last_response')
    return result

def complete_web_login(login):
    """Use original response in RAM. No authorization-code replay or seat request."""
    if getattr(login, '_web_login_attempted', False):
        return getattr(login, '_web_login_result', {'state':'web_login_already_attempted'})
    response = login.last_response
    if response is None or response.status_code != 200:
        raise ValueError('需要成功加载的原始回调响应')
    p = urlsplit(response.url)
    if p.scheme != 'https' or p.hostname != 'tyrzfw.chaoxing.com' or p.path != '/OAuth2/wfu/index':
        raise ValueError('当前响应不是 WFU 官方回调页')
    params = callback_params(response.text)
    login._web_login_attempted = True
    login._web_login_result = {'state':'web_login_attempted_no_result', 'note':'不自动重试，保留会话检查结果'}
    r = login.session.get(
        'https://tyrzfw.chaoxing.com/OAuth2/wfu/login', params=params,
        headers={'Referer':response.url, 'X-Requested-With':'XMLHttpRequest', 'Accept':'application/json, text/javascript, */*; q=0.01'},
        allow_redirects=False, timeout=30,
    )
    login.last_response = r
    if r.status_code != 200:
        from qr_login import response_diagnostic
        result = {'state':'web_login_http_error', 'diagnostic':response_diagnostic(r)}
    else:
        try:
            payload = r.json()
        except ValueError:
            result = {'state':'web_login_non_json', 'content_type':r.headers.get('Content-Type')}
        else:
            login._web_login_payload = payload
            accepted = payload.get('status') if isinstance(payload, dict) else None
            result = {
                'state':'web_login_confirmed' if accepted is True or accepted == 1 else 'web_login_rejected',
                'response_keys': sorted(payload) if isinstance(payload,dict) else [],
                'cookie_names': sorted({c.name for c in login.session.cookies}),
                'token_names': sorted(login.tokens),
                'next_page':'https://i.chaoxing.com',
                'note':'这里只确认网页登录接口状态；座位应用 token 仍需后续应用入口验证。',
            }
    login._web_login_result = result
    return result