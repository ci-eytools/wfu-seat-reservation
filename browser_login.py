"""Official interactive browser login; no automatic seat reservations."""
import asyncio
from urllib.parse import parse_qs, urlencode, urlsplit, unquote
from playwright.async_api import async_playwright
from qr_login import AUTH_URL

ENTRY_URL = 'https://e.wfu.edu.cn/?' + urlencode({'code':'0x010040009', 'locationurl':AUTH_URL})
TOKEN_HOSTS = {'tyrzfw.chaoxing.com', 'passport2.chaoxing.com', 'office.chaoxing.com'}

def seat_api_blocked(url):
    path = unquote(urlsplit(url).path).lower()
    return '/data/apps/seat/' in path or path.rstrip('/').endswith('/data/apps/seat')

def safe_location(url):
    parsed = urlsplit(url)
    return {'host':parsed.hostname, 'path':parsed.path}

class BrowserLogin:
    def __init__(self, proxy='http://127.0.0.1:7890'):
        self.proxy = proxy
        self.runtime = self.browser = self.context = self.page = None
        self.tokens = {}
        self.events = []
        self.blocked_count = 0

    async def _route(self, route):
        if seat_api_blocked(route.request.url):
            self.blocked_count += 1
            await route.abort('blockedbyclient')
        else:
            await route.continue_()

    def _capture_url(self, url):
        if urlsplit(url).hostname in TOKEN_HOSTS:
            for name, values in parse_qs(urlsplit(url).query).items():
                if name.lower() in {'wfw_token', 'access_token', 'token'}:
                    self.tokens[name] = values[-1]

    def _response(self, response):
        if response.request.is_navigation_request():
            self._capture_url(response.url)
            self.events.append({**safe_location(response.url), 'status':response.status})
            self.events = self.events[-30:]

    def _page(self, page):
        page.on('framenavigated', lambda frame: self._capture_url(frame.url))

    async def start(self, *, navigate=True):
        if self.runtime is not None:
            raise RuntimeError('已有登录浏览器，请先 await browser_login.close()')
        self.tokens.clear()
        self.events.clear()
        self.blocked_count = 0
        self.runtime = await async_playwright().start()
        options = {'headless':False}
        if self.proxy:
            options['proxy'] = {'server':self.proxy}
        try:
            self.browser = await self.runtime.chromium.launch(**options)
            self.context = await self.browser.new_context(service_workers='block')
            # Install before navigation, including popup pages; no browser profile is imported.
            await self.context.route('**/*', self._route)
            self.context.on('response', self._response)
            self.context.on('page', self._page)
            self.page = await self.context.new_page()
            if navigate:
                await self.page.goto(ENTRY_URL, wait_until='domcontentloaded', timeout=45000)
        except Exception:
            await self.close()
            raise
        return {'state':'browser_open', 'message':'在新浏览器中切换到智慧潍苑APP扫码登录，用自己的App确认。'}

    async def status(self):
        if self.context is None or not self.browser.is_connected():
            return {'state':'closed'}
        cookies = await self.context.cookies()
        locations = [safe_location(p.url) for p in self.context.pages if not p.is_closed()]
        return {
            'state':'token_observed' if self.tokens else 'inspect_browser',
            'token_names':sorted(self.tokens),
            'cookie_names':sorted({c['name'] for c in cookies if c['domain'].lstrip('.') == 'chaoxing.com' or c['domain'].lstrip('.').endswith('.chaoxing.com')}),
            'pages':locations, 'navigation_chain':list(self.events),
            'blocked_seat_requests':self.blocked_count,
            'note':'发现token不代表已验证有效性；本登录助手不提交预约。',
        }

    async def wait_for_token(self, seconds=120):
        if self.context is None:
            raise RuntimeError('请先启动登录浏览器')
        deadline = asyncio.get_running_loop().time() + seconds
        while asyncio.get_running_loop().time() < deadline:
            if not self.browser.is_connected():
                break
            if self.tokens or not any(not p.is_closed() for p in self.context.pages):
                break
            if self.events and self.events[-1]['status'] >= 400:
                break
            await asyncio.sleep(0.5)
        return await self.status()

    async def close(self):
        try:
            if self.browser and self.browser.is_connected():
                await self.browser.close()
        finally:
            if self.runtime:
                await self.runtime.stop()
            self.runtime = self.browser = self.context = self.page = None
            self.tokens.clear()