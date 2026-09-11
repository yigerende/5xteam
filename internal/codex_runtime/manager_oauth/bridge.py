"""Local Go job integration. The protocol runner never calls a manager service."""
from __future__ import annotations

import json
import re
import sys
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone

_payload = {}
_rpc = None
_secrets = []


def configure(payload, rpc=None):
    global _payload, _rpc, _secrets
    _payload, _rpc = payload, rpc
    _secrets = []
    def collect(value, key=""):
        if isinstance(value, dict):
            for k, v in value.items():
                collect(v, k)
        elif isinstance(value, list):
            for v in value:
                collect(v, key)
        elif isinstance(value, str) and len(value) >= 4 and any(
            word in key.lower() for word in ("password", "secret", "token", "api_key", "cdk", "pickup_url", "proxy")
        ):
            _secrets.append(value)
    collect(payload)
    _secrets.sort(key=len, reverse=True)


def redact(value):
    text = str(value or "")
    for secret in _secrets:
        text = text.replace(secret, "[redacted]")
    text = re.sub(r"(https?://)[^\s/@]+:[^\s/@]+@", r"\1[redacted]@", text)
    text = re.sub(r"(?i)([?&](?:code|state|token|code_verifier)=)[^\s&#\"']+", r"\1[redacted]", text)
    text = re.sub(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?", "[redacted-token]", text)
    return text


def safe_url(value):
    parsed = urllib.parse.urlsplit(str(value or ""))
    if parsed.scheme and parsed.hostname:
        return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc.rsplit("@", 1)[-1], parsed.path, "", ""))
    return parsed.path


def emit(stage, event, message, *, http_status=0, request=None, response=None, details=None, level="info"):
    item = {"schema_version": 1, "stage": stage, "event": event, "message": redact(message),
            "http_status": http_status, "level": level, "request": request or {},
            "response": response or {}, "details": details or {}}
    print("[protocol-event] " + json.dumps(item, ensure_ascii=False, separators=(",", ":")), file=sys.stderr, flush=True)


def append_login_log(job_id, message, level="info", step="oauth"):
    print("[protocol] " + redact(message), file=sys.stderr, flush=True)
    emit(step, "progress", message, level=level)


def _diag_fallback_log(message):
    message = re.sub(r"proxy=.*", "proxy=[configured]", str(message))
    append_login_log("", message, "info", "sentinel")


def call(method, **params):
    if _rpc is None:
        raise RuntimeError("Local Go OAuth integration is unavailable: " + method)
    return _rpc(method, params)


class RemoteLock:
    def __enter__(self):
        call("sms_lock")
        return self

    def __exit__(self, *args):
        call("sms_unlock")


_SMS_CDK_LOCK = RemoteLock()


def get_sms_platform_config(workspace_id, provider):
    cfg = dict(call("sms_config", provider=provider) if _rpc else _payload.get("sms_config", {}))
    cdks = cfg.get("cdks", [])
    if isinstance(cdks, str):
        cfg["cdks"] = [x.strip() for x in re.split(r"[\r\n,]+", cdks) if x.strip()]
    return cfg


def save_sms_platform_config(workspace_id, provider, config, enabled):
    return call("sms_save_config", provider=provider, config=config, enabled=enabled)


def normalize_workspace_id(value):
    return "local"


def iso_now():
    return datetime.now(timezone.utc).isoformat()


def request_proxy_url(payload):
    proxy = str(payload.get("proxy") or "").strip()
    if not proxy:
        raise RuntimeError("OAuth proxy required")
    return proxy


def raise_if_login_job_cancelled(job_id):
    # Go owns the process context and terminates the child on cancellation/timeout.
    return None


def _update_gpt_sync_fields(workspace_id, email, fields):
    return call("credential_update", fields=fields)


def manual_email_code_for_payload(payload):
    for key in ("manual_email_code", "email_code", "verification_code"):
        value = str(payload.get(key) or "").strip()
        if re.fullmatch(r"\d{6}", value):
            return value
    return ""


def manual_phone_code_for_payload(payload):
    for key in ("manual_phone_code", "phone_code", "sms_code"):
        value = str(payload.get(key) or "").strip()
        if re.fullmatch(r"\d{6}", value):
            return value
    return ""


def login_mail_fetch_payload(payload):
    return {"email": payload.get("email", ""), "generic_accounts": payload.get("generic_accounts", []),
            "limit": payload.get("limit", 20), "sender_filter": payload.get("sender_filter", "")}


def http_request_text(url, *, method="GET", headers=None, timeout=30, proxy_url=""):
    from .upstream import DEFAULT_HTTP_HEADERS
    request = urllib.request.Request(url, method=method, headers={**DEFAULT_HTTP_HEADERS, **(headers or {})})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler(
        {"http": proxy_url, "https": proxy_url} if proxy_url else {}))
    with opener.open(request, timeout=timeout) as response:
        return response.read(2 << 20).decode("utf-8", "replace")


def http_request_json(url, *, method="GET", json_data=None, headers=None, timeout=30, proxy_url=""):
    from .upstream import DEFAULT_HTTP_HEADERS
    final_headers = {**DEFAULT_HTTP_HEADERS, **(headers or {})}
    data = None
    if json_data is not None:
        data = json.dumps(json_data, ensure_ascii=False).encode("utf-8")
        final_headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, method=method, data=data, headers=final_headers)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler(
        {"http": proxy_url, "https": proxy_url} if proxy_url else {}))
    with opener.open(request, timeout=timeout) as response:
        value = json.loads(response.read(2 << 20).decode("utf-8", "replace"))
    return value if isinstance(value, dict) else {"data": value}


def fetch_transient_client_mail(payload):
    from . import upstream as core
    messages, errors = [], []
    for account in payload.get("generic_accounts", []):
        if not isinstance(account, dict):
            continue
        try:
            fetch = core.fetch_mailtoken_messages if account.get("mode") == "mailtoken" else core.fetch_directurl_messages
            messages.extend(fetch(account, limit=int(payload.get("limit") or 20),
                                  sender_filter=str(payload.get("sender_filter") or "")))
        except Exception as exc:
            errors.append(redact(exc))
    return {"messages": sorted(messages, key=core.message_sort_value, reverse=True), "errors": errors}


DEAD_CODES = {"account_deactivated", "account_deleted", "account_banned", "account_disabled", "account_suspended"}
DEAD_MARKERS = (
    "deleted or deactivated", "account has been deleted", "account has been deactivated",
    "account was deleted", "account was deactivated", "account deactivated", "account deleted",
    "account banned", "account disabled", "account suspended", "access deactivated",
    "账号已删除", "账号已停用", "账号已禁用", "账号被封", "账户已删除", "账户已停用", "账户已禁用", "账户被封",
)


def dead_code(text):
    low = str(text or "").lower()
    for code in sorted(DEAD_CODES):
        if code in low:
            return code
    if any(marker in low for marker in DEAD_MARKERS):
        if "delete" in low or "删除" in low:
            return "account_deleted"
        if "ban" in low or "封" in low:
            return "account_banned"
        return "account_deactivated"
    return ""


class DeadAccountError(BaseException):
    # An explicit account termination must escape the reference's broad
    # recoverable-request catches (bootstrap/OTP resend), like cancellation.
    def __init__(self, code, stage, status, message):
        super().__init__(redact(message))
        self.code, self.stage, self.status = code, stage, status


def token_response(status, data):
    code = dead_code(json.dumps(data, ensure_ascii=False)) if status >= 400 else ""
    emit("oauth_token", "exchange_complete", "OAuth Token 交换已返回", http_status=status,
         request={"method": "POST", "url": "https://auth.openai.com/oauth/token",
                  "payload_fields": ["grant_type", "client_id", "code", "redirect_uri", "code_verifier"]},
         response={"returned_fields": sorted(data) if isinstance(data, dict) else [],
                   "access_token_present": bool(data.get("access_token")),
                   "refresh_token_present": bool(data.get("refresh_token")),
                   "error_code": code or str(data.get("error") or "")[:100] if isinstance(data.get("error"), str) else code},
         level="info" if status == 200 else "warning")
    if code:
        raise DeadAccountError(code, "oauth_token", status, code)
