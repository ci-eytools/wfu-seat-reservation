"""Official QR-login flow. No seat reservation requests are implemented."""
import base64
import json
import re
import time
from urllib.parse import parse_qs, urljoin, urlsplit
import requests
from seat_guard import guard_session
from login_diagnostics import enable_login_logging

AUTH_URL = "https://e.wfu.edu.cn/oauth2/authorize?response_type=redirect&redirect_uri=https://tyrzfw.chaoxing.com/OAuth2/wfu/index&client_id=6f2c5bd1a3614fef8493100be288f709&state=1"
ALLOWED_HOSTS = {"e.wfu.edu.cn", "tyrzfw.chaoxing.com", "passport2.chaoxing.com", "office.chaoxing.com"}

BROWSER_USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"

def configure_login_session(session):
    """Use a web-client UA; the callback rejects the default requests UA."""
    session.headers['User-Agent'] = BROWSER_USER_AGENT


def response_diagnostic(response):
    """Inspect an existing response without making another request."""
    if response is None:
        return {"state": "no_response"}
    parsed = urlsplit(response.url)
    body = response.text.lower()
    indicators = [word for word in (
        "access denied", "forbidden", "proxy", "cloudflare", "nginx",
        "invalid code", "expired", "验证码", "访问被拒绝", "授权码"
    ) if word in body]
    return {
        "host": parsed.hostname, "path": parsed.path,
        "status": response.status_code,
        "content_type": response.headers.get("Content-Type"),
        "server": response.headers.get("Server"),
        "via": response.headers.get("Via"),
        "body_bytes": len(response.content),
        "body_indicators": indicators,
        "cookie_names": sorted({c.name for c in response.cookies}),
    }

class QRLogin:
    def __init__(self, proxy=None):
        self.session = guard_session(requests.Session())
        self.session.trust_env = False
        configure_login_session(self.session)
        if proxy:
            self.session.proxies.update(http=proxy, https=proxy)
        self.uuid = None
        self.tokens = {}
        self.last_response = None
        self.callback_attempted = False
        self.callback_result = None
        enable_login_logging(self, include_last=False)

    def start(self):
        response = self.session.get("https://e.wfu.edu.cn/ssoApi/appQRCode", params={"locationurl": AUTH_URL}, timeout=30)
        response.raise_for_status()
        def read_string(name):
            match = re.search(r"\bvar\s+" + name + r'\s*=\s*("(?:\\.|[^"\\])*")', response.text)
            if not match:
                raise RuntimeError(f"扫码页面未找到 {name}；请检查页面结构或响应状态")
            return json.loads(match.group(1))
        self.uuid = read_string("UUID")
        data_uri = read_string("baseImg")
        if not data_uri.startswith("data:image/png;base64,"):
            raise RuntimeError("二维码格式发生变化")
        self.last_response = response
        return base64.b64decode(data_uri.split(",", 1)[1], validate=True)

    def poll_once(self):
        if not self.uuid:
            raise RuntimeError("请先生成二维码")
        response = self.session.post("https://e.wfu.edu.cn/ssoApi/checkQRLogin", data={"uuid": self.uuid, "locationurl": AUTH_URL}, timeout=20)
        response.raise_for_status()
        result = response.json()
        code = result.get("code")
        if code == "0x000000":
            target = result.get("data", {}).get("locatUrl")
            if not target:
                raise RuntimeError("登录成功响应缺少 locatUrl")
            return "confirmed", target
        if code == "0x0030010016":
            return "expired", None
        return "waiting", None

    def wait_for_scan(self, seconds=120):
        if self.callback_attempted:
            return self.callback_result or {"state": "callback_already_attempted", "diagnostic": response_diagnostic(self.last_response)}
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            state, target = self.poll_once()
            if state == "confirmed":
                return self.follow_callback(target)
            if state == "expired":
                return {"state": "expired", "message": "二维码已过期，请重新运行生成单元格"}
            time.sleep(1)
        return {"state": "timeout", "message": "等待超时，可继续等待或重新生成二维码"}

    def follow_callback(self, url):
        if self.callback_attempted:
            return self.callback_result or {"state": "callback_already_attempted", "diagnostic": response_diagnostic(self.last_response)}
        self.callback_attempted = True
        chain = []
        for _ in range(12):
            parsed = urlsplit(url)
            if parsed.scheme != "https" or parsed.hostname not in ALLOWED_HOSTS:
                return {"state": "inspect_redirect", "host": parsed.hostname, "path": parsed.path}
            if "/data/apps/seat/" in parsed.path or parsed.path.endswith("/submit"):
                raise RuntimeError("登录流程禁止调用预约接口")
            for key, values in parse_qs(parsed.query).items():
                if "token" in key.lower():
                    self.tokens[key] = values[-1]
            response = self.session.get(url, allow_redirects=False, timeout=30)
            self.last_response = response
            chain.append({"host": parsed.hostname, "path": parsed.path, "status": response.status_code})
            if response.status_code >= 400:
                self.callback_result = {
                    "state": "callback_http_error", "chain": chain,
                    "diagnostic": response_diagnostic(response),
                    "message": "扫码已确认，但回调失败；未自动重试授权码。",
                }
                return self.callback_result
            if response.is_redirect:
                url = urljoin(url, response.headers["Location"])
                continue
            if parsed.hostname == "tyrzfw.chaoxing.com" and parsed.path == "/OAuth2/wfu/index":
                from web_login_step import complete_web_login
                self.callback_result = complete_web_login(self)
                self.callback_result["chain"] = chain
                return self.callback_result
            return {"state": "callback_loaded", "chain": chain, "token_names": sorted(self.tokens), "cookie_names": sorted({c.name for c in self.session.cookies}), "note": "未发现 token 不等于失败；可能使用 Cookie 会话或还需分析页面跳转。"}
        raise RuntimeError("回调跳转超过限制")