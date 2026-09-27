"""Relay only WFUseat inline callbacks from an existing Telegram update consumer.

Call forward_wfuseat_callback(update, bot_token) BEFORE the quiz bot's chat filter.
If it returns True, skip the quiz handler for this update. No polling or webhook
is installed here. The backend must be the local trusted WFUseat service.
"""
import hashlib
import hmac
import json
import logging
import urllib.request
from urllib.parse import urlsplit


def forward_wfuseat_callback(update, bot_token, backend_url="http://127.0.0.1:8787", *, opener=None):
    query = update.get("callback_query") or {}
    if not str(query.get("data", "")).startswith("wfuseat:"):
        return False
    url = urlsplit(backend_url)
    if (url.scheme not in ("http", "https") or url.username or url.password
            or url.query or url.fragment
            or (url.scheme == "http" and url.hostname not in ("127.0.0.1", "::1", "localhost"))):
        logging.warning("WFUseat callback: invalid backend address")
        return True
    message = query.get("message") or {}
    body = {
        "id": query.get("id", ""),
        "data": query.get("data", ""),
        "message": {
            "message_id": message.get("message_id", 0),
            "chat": {"id": (message.get("chat") or {}).get("id", 0)},
        },
    }
    key = hmac.new(bot_token.encode(), b"wfuseat-callback-v1", hashlib.sha256).hexdigest()
    req = urllib.request.Request(
        backend_url.rstrip("/") + "/v1/telegram/callback",
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        # Explicit direct connection; never send bot credentials or callback keys
        # to the school's proxy or a proxy inherited from the environment.
        client = opener or urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with client.open(req, timeout=30) as response:
            if response.status != 200:
                logging.warning("WFUseat callback: backend did not confirm expansion")
    except Exception:
        # Exception strings may contain destinations; never print payloads/keys.
        logging.warning("WFUseat callback: expansion failed; check backend availability")
    return True
