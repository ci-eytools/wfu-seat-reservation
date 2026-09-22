"""Redacted local HTTP diagnostics. Never log successful response bodies."""
import json
import os
import re
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit, parse_qsl, urlencode, urlunsplit

LOG_DIR = Path(__file__).resolve().parent / 'work' / 'login_logs'

def safe_url(value):
    p = urlsplit(value)
    host = p.hostname or ''
    if p.port:
        host += ':' + str(p.port)
    return urlunsplit((p.scheme, host, p.path, urlencode([(k, '[REDACTED]') for k, _ in parse_qsl(p.query)]), ''))

def scrub(text, response):
    secrets = []
    request = response.request
    for url in (response.url, getattr(request, 'url', '')):
        secrets.extend(v for _, v in parse_qsl(urlsplit(url).query) if len(v) >= 4)
    if request is not None:
        body = request.body
        if isinstance(body, bytes):
            body = body.decode('utf-8', errors='replace')
        if isinstance(body, str):
            secrets.extend(v for _, v in parse_qsl(body) if len(v) >= 4)
        cookie = request.headers.get('Cookie', '')
        secrets.extend(part.split('=', 1)[1].strip() for part in cookie.split(';') if '=' in part)
    secrets.extend(c.value for c in response.cookies)
    for secret in sorted(set(secrets), key=len, reverse=True):
        if secret:
            text = text.replace(secret, '[REDACTED]')
    text = re.sub(r'https?://[^\s<>"\x27]+', lambda m: safe_url(m.group()), text)
    text = re.sub(r'(?i)\b(?:[0-9a-f]{24,}|[a-z0-9_+/=-]{40,})\b', '[REDACTED]', text)
    text = re.sub(r'(?i)((?:token|authorization|password|secret|uuid|code)\s*[=:]\s*)[^\s<>,;]+', r'\1[REDACTED]', text)
    return text

class LoginLog:
    def __init__(self, session):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        self.path = LOG_DIR / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S') + '-' + str(time.time_ns()) + '.jsonl')
        fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.close(fd)
        self.session = session

    def record(self, response, **kwargs):
        req = response.request
        request_headers = {}
        for name in ('User-Agent', 'Accept', 'Accept-Language', 'Content-Type', 'Origin', 'Referer', 'X-Requested-With'):
            if req is not None and name in req.headers:
                value = req.headers[name]
                request_headers[name] = safe_url(value) if name in ('Origin', 'Referer') else value
        response_headers = {k: scrub(v, response) for k, v in response.headers.items() if k.lower() in {
            'content-type', 'content-length', 'server', 'via', 'date', 'x-cache', 'retry-after', 'cf-ray', 'x-request-id'
        }}
        if 'Location' in response.headers:
            response_headers['Location'] = safe_url(response.headers['Location'])
        record = {
            'time_utc': datetime.now(timezone.utc).isoformat(),
            'method': getattr(req, 'method', None), 'url': safe_url(response.url),
            'status': response.status_code, 'elapsed_ms': round(response.elapsed.total_seconds()*1000),
            'request_headers': request_headers, 'response_headers': response_headers,
            'proxy_configured': bool(self.session.proxies), 'trust_env': self.session.trust_env,
            'sent_cookie_names': sorted({v.split('=',1)[0].strip() for v in (req.headers.get('Cookie','') if req is not None else '').split(';') if '=' in v}),
            'received_cookie_names': sorted({c.name for c in response.cookies}),
        }
        if response.status_code >= 400:
            record['error_body_excerpt'] = scrub(response.text[:8192], response)
            record['body_bytes'] = len(response.content)
        with self.path.open('a', encoding='utf-8') as f:
            f.write(json.dumps(record, ensure_ascii=False) + '\n')
        return response

def enable_login_logging(login, include_last=True):
    """Works on an existing notebook instance; does not send any request."""
    if getattr(login, 'http_log', None) is None:
        login.http_log = LoginLog(login.session)
        login.session.hooks['response'].append(login.http_log.record)
    if include_last and login.last_response is not None:
        login.http_log.record(login.last_response)
    return str(login.http_log.path)