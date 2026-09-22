"""Read-only check of the user-supplied seat landing page."""
import re
from html import unescape
from urllib.parse import urlsplit, urljoin, parse_qsl, urlencode, urlunsplit
from login_diagnostics import safe_url

def check_seat_page(login, url, *, without_wfw_token=False):
    p=urlsplit(url)
    if p.scheme!='https' or p.hostname!='office.chaoxing.com' or p.path!='/front/third/apps/seat/index':
        raise ValueError('请输入指定的 HTTPS 座位首页地址')
    if without_wfw_token:
        url=urlunsplit((p.scheme,p.netloc,p.path,urlencode([(k,v) for k,v in parse_qsl(p.query,keep_blank_values=True) if k!='wfw_token']),''))
    chain=[]
    for _ in range(6):
        p=urlsplit(url)
        # Stop before sending any redirected request outside the seat homepage.
        if p.scheme!='https' or p.hostname!='office.chaoxing.com' or p.path!='/front/third/apps/seat/index':
            return {'state':'redirected_away','target':safe_url(url),'chain':chain}
        r=login.session.get(url,allow_redirects=False,timeout=30)
        login.seat_page_response=r
        chain.append({'url':safe_url(r.url),'status':r.status_code})
        if r.is_redirect:
            url=urljoin(r.url,r.headers['Location']); continue
        title=re.search(r'<title[^>]*>(.*?)</title>',r.text,re.I|re.S)
        title=unescape(title.group(1)).strip() if title else None
        evidence={
            'seat_title':bool(title and '座位' in title),
            'seat_script':bool(re.search(r'<script[^>]+src=[\x22\x27][^\x22\x27]*apps/seat/',r.text,re.I)),
            'user_info_present':bool(re.search(r'userLoginInfo\s*=\s*\{\s*"userInfo"\s*:\s*\{',r.text)),
        }
        state='seat_page_loaded' if r.status_code==200 and all(evidence.values()) else 'needs_inspection'
        return {'state':state,'status':r.status_code,'title':title,'evidence':evidence,'chain':chain,'without_wfw_token':without_wfw_token,'note':'仅GET座位首页；未调用预约接口。页面内容保存在login.seat_page_response。'}
    return {'state':'redirect_limit','chain':chain}