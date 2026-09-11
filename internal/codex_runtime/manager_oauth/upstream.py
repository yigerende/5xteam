"""Codex protocol core vendored from the running gpt-account-manager server.py.

The request/state-machine methods are mechanically ported; integration with
the Go store and diagnostics lives in adapter.py and bridge.py. No external
manager process, database, code path or CPA OAuth service is used.
See PORT.md for the exact boundaries and parity verification.
"""
from __future__ import annotations
import base64
import hashlib
import html
import http.cookiejar
import json
import os
import random
import re
import secrets
import shutil
import subprocess
import time
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from pathlib import Path
from typing import Any
from . import sms_providers as sp
from .bridge import (
    request_proxy_url, append_login_log, _diag_fallback_log,
    raise_if_login_job_cancelled, get_sms_platform_config, save_sms_platform_config,
    manual_email_code_for_payload, manual_phone_code_for_payload,
    fetch_transient_client_mail, http_request_text, http_request_json,
    login_mail_fetch_payload, normalize_workspace_id, iso_now,
    _update_gpt_sync_fields, _SMS_CDK_LOCK, token_response,
)
ROOT = Path(__file__).resolve().parent
LOGIN_NODE_BIN = os.environ.get("MAIL_PICKUP_NODE_BIN", "node").strip() or "node"
OPENAI_SENTINEL_HELPER = ROOT / "openai_sentinel_token.cjs"
SENTINEL_BASE = os.environ.get("SENTINEL_BASE_URL", "https://sentinel.openai.com")
SENTINEL_SDK_VERSION = os.environ.get("SENTINEL_SDK_VERSION", "20260124ceb8")
SENTINEL_FRAME_VERSION = os.environ.get("SENTINEL_FRAME_VERSION", "20260219f9f6")
SENTINEL_SDK_URL = f"{SENTINEL_BASE}/sentinel/{SENTINEL_SDK_VERSION}/sdk.js"
SENTINEL_REQ_URL = f"{SENTINEL_BASE}/backend-api/sentinel/req"
SENTINEL_FRAME_URL = f"{SENTINEL_BASE}/backend-api/sentinel/frame.html?sv={SENTINEL_FRAME_VERSION}"
OPENAI_OAUTH_AUTHORIZE_URL = "https://auth.openai.com/oauth/authorize"
OPENAI_OAUTH_TOKEN_URL = "https://auth.openai.com/oauth/token"
OPENAI_CODEX_CLIENT_ID = os.environ.get("OPENAI_CODEX_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann").strip()
OPENAI_OAUTH_SCOPE = os.environ.get("OPENAI_OAUTH_SCOPE", "openid profile email offline_access").strip()
OPENAI_OAUTH_REFRESH_SCOPE = os.environ.get("OPENAI_OAUTH_REFRESH_SCOPE", "openid profile email").strip()
OPENAI_OAUTH_REDIRECT_URI = os.environ.get(
    "OPENAI_OAUTH_REDIRECT_URI",
    "http://localhost:1455/auth/callback",
).strip() or "http://localhost:1455/auth/callback"
OPENAI_IMPERSONATE = "chrome131"
CODE_PATTERNS = [
    r"(?<!\d)(\d{6})(?!\d)",
    r"(?<![A-Za-z0-9])([A-Z0-9]{6,8})(?![A-Za-z0-9])",
]
MAIL_TYPE_LABELS = {
    "verification": "verification",
    "invite": "invite",
    "security": "security",
    "promotion": "promotion",
    "banned": "banned",
    "other": "other",
}
DEFAULT_HTTP_HEADERS = {
    "Accept": "application/json",
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/145.0.0.0 Safari/537.36"
    ),
}
OPENAI_SEC_CH_UA = '"Google Chrome";v="145", "Not?A_Brand";v="8", "Chromium";v="145"'
OPENAI_SEC_CH_UA_FULL_VERSION_LIST = '"Chromium";v="145.0.0.0", "Not:A-Brand";v="99.0.0.0", "Google Chrome";v="145.0.0.0"'
CPA_PROBE_USER_AGENT = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"
SMS_MAX_NUMBERS_PER_ACCOUNT = 3
_PICKUP_BAD_HOSTS: dict[str, set[str]] = {}

def normalize_generic_mail_mode(value: Any) -> str:
    text = str(value or "").strip().lower().replace("_", "-")
    aliases = {"pop": "pop3", "mail-pop": "pop3", "mail-pop3": "pop3", "mail-imap": "imap",
               "cloud-mail": "cloudmail", "skymail": "cloudmail", "luck-mail": "luckmail",
               "luckmail-api": "luckmail", "luckyous": "luckmail", "icloudapi": "directurl",
               "icloudapi.xyz": "directurl", "direct-url": "directurl", "showmail": "directurl",
               "mail-token": "mailtoken", "hashmail": "mailtoken", "mtoken": "mailtoken",
               "mail2925": "auto", "2925": "auto", "mail-pickup-tool": "auto"}
    text = aliases.get(text, text)
    return text if text in {"auto", "imap", "pop3", "cloudmail", "luckmail", "inbucket", "directurl", "mailtoken", "totp"} else "auto"


def parse_message_datetime(value: Any) -> datetime | None:
    text = coerce_text(value)
    if not text:
        return None
    try:
        parsed = parsedate_to_datetime(text)
        if parsed:
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=timezone.utc)
            return parsed.astimezone(timezone.utc)
    except Exception:
        pass
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed.astimezone(timezone.utc)
    except Exception:
        return None


def message_sort_value(message: dict[str, Any]) -> str:
    value = message.get("received_at") or message.get("cached_at") or ""
    parsed = parse_message_datetime(value)
    return parsed.isoformat() if parsed else str(value)


def normalize_phone_digits(value: Any) -> str:
    return re.sub(r"\D+", "", coerce_text(value))


def extract_phone_hint_from_text(value: Any) -> str:
    text = coerce_text(value)
    if not text:
        return ""
    patterns = [
        r"(?:ending\s+in|ends\s+in|last\s+\d*\s*digits?|尾号|末尾|手机|手机号|电话|phone|mobile|sms)[^\d+*xX•]{0,40}(\+?\d[\d\s().-]{1,22}\d|[*xX•]{2,}\s*\d{2,6}|\d{2,6})",
        r"(\+\d[\d\s().-]{6,22}\d)",
    ]
    for pattern in patterns:
        match = re.search(pattern, text, flags=re.I)
        if match:
            digits = normalize_phone_digits(match.group(1))
            if len(digits) >= 2:
                return digits
    return ""


def extract_phone_hint_from_step(data: Any, continue_url: str = "") -> str:
    texts: list[str] = []
    seen = 0

    def visit(value: Any, key_hint: str = "") -> None:
        nonlocal seen
        if seen > 120:
            return
        seen += 1
        if isinstance(value, dict):
            for key, item in value.items():
                key_text = coerce_text(key)
                if re.search(r"phone|mobile|sms|mfa|factor|verification|otp|channel|手机|号码", key_text, re.I):
                    texts.append(f"{key_text}: {coerce_text(item)}")
                visit(item, key_text)
        elif isinstance(value, list):
            for item in value[:80]:
                visit(item, key_hint)
        elif isinstance(value, str):
            if key_hint or re.search(r"phone|mobile|sms|mfa|otp|channel|手机|号码|\+\d|尾号|ending\s+in", value, re.I):
                texts.append(value)

    visit(data)
    if continue_url:
        texts.append(continue_url)
    for text in texts:
        hint = extract_phone_hint_from_text(text)
        if hint:
            return hint
    return ""


def phone_pool_entries_from_payload(payload: dict[str, Any]) -> list[dict[str, str]]:
    raw_entries = payload.get("phone_pool") or payload.get("phonePool") or []
    if not isinstance(raw_entries, list):
        return []
    entries: list[dict[str, str]] = []
    for item in raw_entries:
        if not isinstance(item, dict):
            continue
        phone = coerce_text(item.get("phone") or item.get("phone_number") or item.get("phoneNumber"))
        api_url = coerce_text(item.get("api_url") or item.get("apiUrl") or item.get("phone_api_url") or item.get("phoneApiUrl"))
        if not phone or not api_url:
            continue
        entries.append({
            "id": coerce_text(item.get("id")),
            "mode": coerce_text(item.get("mode")),
            "phone": phone,
            "phone_digits": normalize_phone_digits(phone),
            "api_url": api_url,
            "account_email": coerce_text(item.get("account_email") or item.get("accountEmail")).lower(),
        })
    return entries


def phone_pool_match_by_hint(entries: list[dict[str, str]], hint: str) -> dict[str, str] | None:
    hint_digits = normalize_phone_digits(hint)
    if len(hint_digits) < 2:
        return None
    exact = [entry for entry in entries if entry["phone_digits"] == hint_digits]
    if len(exact) == 1:
        return exact[0]
    if len(hint_digits) >= 4:
        suffix = [entry for entry in entries if entry["phone_digits"].endswith(hint_digits)]
        if len(suffix) == 1:
            return suffix[0]
    if len(hint_digits) >= 2:
        suffix = [entry for entry in entries if entry["phone_digits"].endswith(hint_digits)]
        if len(suffix) == 1:
            return suffix[0]
    return None


def network_error_message(url: str, exc: BaseException) -> str:
    parsed = urllib.parse.urlparse(url)
    host = parsed.hostname or url
    reason = getattr(exc, "reason", exc)
    text = str(reason or exc)
    lowered = text.lower()
    if "Temporary failure in name resolution" in text or "Name or service not known" in text:
        return f"服务器 DNS 解析失败：{host}。服务端请求由 VPS 发起，不是用户浏览器直接访问；请检查 VPS DNS、代理或目标 API 域名。原始错误：{text}"
    if "nodename nor servname provided" in text or "getaddrinfo failed" in text:
        return f"服务器 DNS 解析失败：{host}。服务端请求由 VPS 发起，不是用户浏览器直接访问；请检查 VPS DNS、代理或目标 API 域名。原始错误：{text}"
    if "unexpected_eof_while_reading" in lowered or "eof occurred in violation of protocol" in lowered:
        return f"代理 TLS 连接被中断：{host}。当前代理出口没有稳定完成 HTTPS 握手，请更换代理或稍后重试。原始错误：{text}"
    if "connection reset" in lowered or "connection refused" in lowered or "remote end closed connection" in lowered:
        return f"代理连接失败：{host}。当前代理出口连接被关闭或拒绝，请更换代理。原始错误：{text}"
    if "timed out" in lowered or "timeout" in lowered:
        return f"代理连接超时：{host}。当前代理出口响应太慢，请更换代理或降低批量。原始错误：{text}"
    return f"服务器网络请求失败：{host}。原始错误：{text}"


def _weimail_api_url(url: str) -> str:
    """weimail(微邮/linlinflow 系)接码页链接 → 同源 JSON 数据 API。

    链接形如 https://host/latest?email=xx&auth_code=yy,页面本身只是 SPA 壳,
    真实邮件在 /mail-api/{key}/{email}?folder=inbox。
    只认 query 带 email+auth_code 的形状(不限定域名);
    不认纯路径形式——/show/{token}/{email}(icloud-api.top)等会直接当整页邮件 HTML 走 directurl。"""
    parsed = urllib.parse.urlsplit(coerce_text(url))
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        return ""
    if parsed.netloc.lower() in _PICKUP_BAD_HOSTS.get("weimail", ()):
        return ""
    qs = urllib.parse.parse_qs(parsed.query)
    def _q(*names: str) -> str:
        for name in names:
            values = qs.get(name)
            if values and values[0].strip():
                return values[0].strip()
        return ""
    email = _q("email", "mail")
    key = _q("auth_code", "code", "key")
    if not (email and key and "@" in email):
        return ""
    base = f"{parsed.scheme}://{parsed.netloc}"
    return (f"{base}/mail-api/{urllib.parse.quote(key, safe='')}/{urllib.parse.quote(email, safe='')}"
            f"?folder=inbox&cache_first=1")


def _cst_naive(value: Any) -> str:
    """国内接码服务(msgviewer/thefindnet)返回裸本地时间(UTC+8)无时区标记,
    而 parse_message_datetime 把裸时间当 UTC → 解析出 8 小时后的"未来时间",
    发码时刻(since)过滤对这些邮件永远失效。无时区标记时补 +08:00。"""
    text = coerce_text(value).strip()
    if text and not re.search(r"(?:z|[+-]\d{2}:?\d{2})$", text, re.I):
        text += "+08:00"
    return text


def _msgviewer_api(url: str) -> tuple[str, str, str] | None:
    """自建邮件查看器(154.219.x 系)接码页:/messages/{token}/{email} 包装页 →
    新版在原页面加 ?json=1 返回最新邮件；旧版使用 /api/messages 列表与 /message 详情。
    命中返回 (旧列表API, 旧详情前缀, 新版JSON地址),否则 None。"""
    parsed = urllib.parse.urlsplit(coerce_text(url))
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        return None
    parts = [p for p in parsed.path.split("/") if p]
    if len(parts) >= 3 and parts[0] == "messages" and "@" in urllib.parse.unquote(parts[-1]):
        base = f"{parsed.scheme}://{parsed.netloc}"
        tail = "/".join(parts[1:])
        query = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
        if not any(key == "json" for key, _ in query):
            query.append(("json", "1"))
        json_url = urllib.parse.urlunsplit((
            parsed.scheme, parsed.netloc, parsed.path, urllib.parse.urlencode(query), "",
        ))
        return f"{base}/api/messages/{tail}", f"{base}/message", json_url
    return None


def _fetch_msgviewer_messages(account: dict[str, Any], ctx: tuple[str, str, str], *,
                              limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    """邮件查看器取信：优先新版 ?json=1，失败时兼容旧版列表/详情 API。"""
    list_api, detail_base, json_api = ctx
    new_api_error = ""
    try:
        response = http_request_json(json_api, timeout=30)
        row = response.get("data") if isinstance(response, dict) else None
        if isinstance(row, dict):
            if not row.get("hasMail") and not any(coerce_text(row.get(key)) for key in ("code", "subject", "body", "html")):
                return []
            code = coerce_text(row.get("code"))
            messages = [normalize_message(
                account=account.get("email", ""),
                source="generic",
                provider="msgviewer",
                folder="api",
                mid=coerce_text(row.get("id")),
                sender=coerce_text(row.get("from") or row.get("from_address") or row.get("sender")),
                subject=coerce_text(row.get("subject")),
                body=coerce_text(row.get("body") or row.get("text") or code),
                html_body=coerce_text(row.get("html") or row.get("html_body")),
                received_at=_cst_naive(row.get("receivedAt") or row.get("received_at") or row.get("date")),
            )]
            if sender_filter:
                needle = sender_filter.lower()
                messages = [m for m in messages
                            if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
            return messages[:limit]
        new_api_error = "新版 JSON 响应缺少 data"
    except Exception as exc:
        new_api_error = str(exc)

    try:
        data = http_request_json(list_api, timeout=30)
    except Exception as exc:
        detail = f"新版接口失败: {new_api_error}; " if new_api_error else ""
        raise RuntimeError(f"msgviewer: {detail}旧版接口失败: {exc}") from exc
    items = data.get("items") if isinstance(data.get("items"), list) else []
    tail = list_api.split("/api/messages/", 1)[-1]
    messages: list[dict[str, Any]] = []
    for item in items[: max(limit * 2, limit)]:
        if not isinstance(item, dict):
            continue
        mid = coerce_text(item.get("id"))
        html_body = ""
        if mid:
            try:
                detail = http_request_json(f"{detail_base}/{mid}/{tail}", timeout=30)
                body = coerce_text(detail.get("body"))
                m = re.search(r"base64,(.*)$", body, flags=re.S)
                if m:
                    html_body = base64.b64decode(m.group(1)).decode("utf-8", "replace")
            except Exception:
                html_body = ""
        messages.append(normalize_message(
            account=account.get("email", ""),
            source="generic",
            provider="msgviewer",
            folder=coerce_text(item.get("mailbox")) or "api",
            mid=mid,
            sender=coerce_text(item.get("from_address") or item.get("from")),
            subject=coerce_text(item.get("subject")),
            body="",
            html_body=html_body,
            received_at=_cst_naive(item.get("received_at")),
        ))
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages
                    if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def _fetch_weimail_messages(account: dict[str, Any], api_url: str, *, limit: int,
                            sender_filter: str = "") -> list[dict[str, Any]]:
    """weimail JSON API:顶层 code=最新验证码;messages[] 每封含 verification_code/received_time/html。"""
    try:
        data = http_request_json(api_url, timeout=30)
    except Exception as exc:
        raise RuntimeError(f"weimail: {exc}") from exc
    if not isinstance(data, dict) or not data.get("ok"):
        raise RuntimeError(f"weimail: {(data or {}).get('error') if isinstance(data, dict) else '' or 'fetch failed'}")
    rows = data.get("messages") if isinstance(data.get("messages"), list) else []
    messages: list[dict[str, Any]] = []
    for row in rows[: max(limit * 2, limit)]:
        if not isinstance(row, dict):
            continue
        messages.append(normalize_message(
            account=account.get("email", ""),
            source="generic",
            provider="weimail",
            folder="api",
            mid=coerce_text(row.get("id")),
            sender=coerce_text(row.get("from_name") or row.get("from_address") or row.get("from")),
            subject=coerce_text(row.get("subject")),
            body=coerce_text(row.get("verification_code")),
            html_body=coerce_text(row.get("html")),
            received_at=coerce_text(row.get("received_time") or row.get("smtp_received_at") or row.get("date")),
        ))
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages
                    if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def _thefindnet_match(url: str, account: dict[str, Any]) -> str | None:
    """thefindnet 系(icloud.thefindnet.xyz)接码 API:/public/search-emails.php。
    按端点文件名识别(同套程序任何部署域名通用)。凭据在 account.mail_password(查询码)。"""
    parsed = urllib.parse.urlsplit(coerce_text(url))
    if parsed.scheme in ("http", "https") and parsed.netloc and "search-emails.php" in parsed.path:
        return f"{parsed.scheme}://{parsed.netloc}{parsed.path}"
    return None


def _fetch_thefindnet_messages(account: dict[str, Any], search_url: str, *,
                               limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    """thefindnet 两步取信:POST search-emails.php {credentials: 邮箱----查询码} 建会话拿列表,
    带会话 cookie GET get-email-body.php?id=N 取正文。"""
    email = coerce_text(account.get("email"))
    token = coerce_text(account.get("mail_password"))
    if not token:
        raise RuntimeError("thefindnet: 缺少邮件查询码（导入格式：邮箱----查询码----URL）")
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

    def _call(u: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8") if payload is not None else None
        req = urllib.request.Request(
            u, data=data, method="POST" if data else "GET",
            headers={"Content-Type": "application/json", "Accept": "application/json",
                     "User-Agent": DEFAULT_HTTP_HEADERS["User-Agent"]})
        with opener.open(req, timeout=30) as resp:
            return json.loads(resp.read().decode("utf-8", "replace"))

    try:
        res = _call(search_url, {"credentials": f"{email}----{token}"})
    except Exception as exc:
        raise RuntimeError(f"thefindnet: {exc}") from exc
    if not isinstance(res, dict) or res.get("status") != "ok":
        raise RuntimeError(f"thefindnet: {coerce_text(res.get('message') if isinstance(res, dict) else '') or 'search failed'}")
    items = res.get("emails") if isinstance(res.get("emails"), list) else []
    detail_url = search_url.replace("search-emails.php", "get-email-body.php")
    messages: list[dict[str, Any]] = []
    for item in items[: max(limit * 2, limit)]:
        if not isinstance(item, dict):
            continue
        mid = coerce_text(item.get("id"))
        html_body = ""
        if mid:
            try:
                d = _call(f"{detail_url}?id={urllib.parse.quote(mid)}")
                if isinstance(d, dict) and d.get("status") == "ok":
                    html_body = coerce_text(d.get("html_body") or d.get("htmlBody"))
            except Exception:
                html_body = ""
        messages.append(normalize_message(
            account=email,
            source="generic",
            provider="thefindnet",
            folder="api",
            mid=mid,
            sender=coerce_text(item.get("from") or item.get("from_email")),
            subject=coerce_text(item.get("subject")),
            body="",
            html_body=html_body or coerce_text(item.get("body_excerpt") or item.get("snippet")),
            received_at=_cst_naive(item.get("date") or item.get("created_at")),
        ))
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages
                    if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def _aisvip_match(url: str, account: dict[str, Any]) -> str | None:
    """aisvip 系(main.aisvip.shop)接码页:/c/{渠道id}?token=xxx → 建会话后
    POST /api/public/code {email} 返回 {code, messages[]}。按 /c/+token 形状识别。"""
    parsed = urllib.parse.urlsplit(coerce_text(url))
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        return None
    if re.fullmatch(r"/c/\d+", parsed.path or "") and urllib.parse.parse_qs(parsed.query).get("token"):
        return f"{parsed.scheme}://{parsed.netloc}{parsed.path}?{parsed.query}"
    return None


def _fetch_aisvip_messages(account: dict[str, Any], page_url: str, *,
                           limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    """aisvip 两步取信:GET 页面建会话(cookie) → POST /api/public/code {email} 拿邮件列表。"""
    email = coerce_text(account.get("email"))
    if not email:
        raise RuntimeError("aisvip: 缺少邮箱")
    origin = urllib.parse.urlsplit(page_url)
    api_url = f"{origin.scheme}://{origin.netloc}/api/public/code"
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))

    def _call(u: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8") if payload is not None else None
        req = urllib.request.Request(
            u, data=data, method="POST" if data else "GET",
            headers={"Content-Type": "application/json", "Accept": "application/json",
                     "User-Agent": DEFAULT_HTTP_HEADERS["User-Agent"]})
        with opener.open(req, timeout=30) as resp:
            body = resp.read().decode("utf-8", "replace")
        return json.loads(body) if body.strip().startswith("{") else {"_raw": body}

    try:
        _call(page_url)  # 建会话
        res = _call(api_url, {"email": email})
    except Exception as exc:
        raise RuntimeError(f"aisvip: {exc}") from exc
    if not isinstance(res, dict) or res.get("ok") is False or res.get("error"):
        raise RuntimeError(f"aisvip: {coerce_text(res.get('error') if isinstance(res, dict) else '') or 'fetch failed'}")
    items = res.get("messages") if isinstance(res.get("messages"), list) else []
    messages: list[dict[str, Any]] = []
    for item in items[: max(limit * 2, limit)]:
        if not isinstance(item, dict):
            continue
        messages.append(normalize_message(
            account=email,
            source="generic",
            provider="aisvip",
            folder="api",
            mid=coerce_text(item.get("id")),
            sender=coerce_text(item.get("from") or item.get("from_address") or item.get("sender")),
            subject=coerce_text(item.get("subject")),
            body=coerce_text(item.get("body")),
            html_body=coerce_text(item.get("html") or item.get("html_body")),
            received_at=coerce_text(item.get("date") or item.get("received_at") or item.get("created_at")),
        ))
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages
                    if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def _api798_match(url: str, account: dict[str, Any]) -> tuple[str, str, str] | None:
    """api798 系接码:/latest 或 /list_emails 页面,query 带 email+auth_code。
    真实数据走 JSON 两步:list_emails?email&auth_code → get_email_detail?id(免鉴权)。
    /latest?email&auth_code 形状与 weimail 无法静态区分,故按域名限定(api798),
    避免探测请求把真 weimail 站点拖进错误分支。"""
    parsed = urllib.parse.urlsplit(coerce_text(url))
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        return None
    if "api798" not in parsed.netloc.lower():
        return None
    path = (parsed.path or "").lower()
    if not (path.startswith("/latest") or path.startswith("/list_emails")):
        return None
    qs = urllib.parse.parse_qs(parsed.query)
    email = (qs.get("email") or [""])[0].strip()
    key = (qs.get("auth_code") or qs.get("code") or qs.get("key") or [""])[0].strip()
    if not (email and key and "@" in email):
        return None
    return f"{parsed.scheme}://{parsed.netloc}", email, key


def _fetch_api798_messages(account: dict[str, Any], ctx: tuple[str, str, str], *,
                           limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    """api798 两步取信:list_emails 拿 id/主题/时间 → get_email_detail 取 html 正文。"""
    origin, email, key = ctx
    list_url = (f"{origin}/list_emails?email={urllib.parse.quote(email)}"
                f"&auth_code={urllib.parse.quote(key)}")
    try:
        data = http_request_json(list_url, timeout=30)
    except Exception as exc:
        raise RuntimeError(f"api798: {exc}") from exc
    if not isinstance(data, dict) or not data.get("success"):
        raise RuntimeError(f"api798: {coerce_text(data.get('message') if isinstance(data, dict) else '') or 'fetch failed'}")
    items = data.get("emails") if isinstance(data.get("emails"), list) else []
    messages: list[dict[str, Any]] = []
    for item in items[: max(limit * 2, limit)]:
        if not isinstance(item, dict):
            continue
        mid = coerce_text(item.get("id"))
        html_body = ""
        if mid:
            try:
                detail = http_request_json(f"{origin}/get_email_detail?id={urllib.parse.quote(mid)}", timeout=30)
                d = detail.get("data") if isinstance(detail.get("data"), dict) else {}
                html_body = coerce_text(d.get("body") or d.get("html") or d.get("html_body"))
            except Exception:
                html_body = ""
        messages.append(normalize_message(
            account=email,
            source="generic",
            provider="api798",
            folder="api",
            mid=mid,
            sender=coerce_text(item.get("from") or item.get("from_address") or item.get("sender")),
            subject=coerce_text(item.get("subject")),
            body="",
            html_body=html_body,
            received_at=coerce_text(item.get("date") or item.get("received_at")),
        ))
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages
                    if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def fetch_directurl_messages(account: dict[str, Any], *, limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    url = coerce_text(account.get("pickup_url"))
    if not url.lower().startswith(("http://", "https://")):
        raise RuntimeError("directurl: missing fetch url")
    for name, matcher, fetcher in _PICKUP_ADAPTERS:
        ctx = matcher(url, account)
        if not ctx:
            continue
        try:
            return fetcher(account, ctx, limit=limit, sender_filter=sender_filter)
        except Exception as exc:
            # 探测 404 = 该宿主只是形状相同、并无此适配器的端点:
            # 记入黑名单后交给下一个适配器(后续 fetch 不再白打 404)
            if "404" in str(exc):
                _PICKUP_BAD_HOSTS.setdefault(name, set()).add(
                    urllib.parse.urlsplit(url).netloc.lower())
                continue
            raise
    try:
        raw = http_request_text(url, timeout=30)
    except Exception as exc:
        raise RuntimeError(f"directurl: {exc}") from exc
    # 接码服务在没有邮件时返回 200 + 错误占位页，按"暂无邮件"处理而非报错
    if not raw.strip() or "No email found" in raw or "<h1>错误</h1>" in raw or "暂无邮件" in raw:
        return []
    # 有的接码页是"包装页"：真正的邮件正文塞进 base64 的 iframe（如 yangyang.website 的
    # <iframe src="data:text/html;base64,...">），验证码在解码后的正文里。先解码出来作正文，
    # 并从外层提取主题/时间/发件人；没有 iframe 的（如 icloudapi）整页就是邮件 HTML，保持原样。
    html_body = raw
    subject = sender = received_at = ""
    iframe = re.search(r'src="data:text/html[^"]*?base64,([^"]+)"', raw, flags=re.I)
    if iframe:
        try:
            html_body = base64.b64decode(html.unescape(iframe.group(1))).decode("utf-8", "replace")
        except Exception:
            html_body = raw
        h3 = re.search(r"<h3[^>]*>(.*?)</h3>", raw, flags=re.I | re.S)
        subject = html.unescape(h3.group(1).strip()) if h3 else ""
        t = re.search(r"时间[：:]\s*([0-9][0-9:\-/ ]+)", raw)
        received_at = t.group(1).strip() if t else ""
        s = re.search(r"发件人[：:]\s*([^<\n]+)", raw)
        sender = s.group(1).strip() if s else ""
    if not subject:
        title_match = re.search(r"<title[^>]*>(.*?)</title>", html_body, flags=re.I | re.S)
        subject = html.unescape(title_match.group(1).strip()) if title_match else ""
    message = normalize_message(
        account=account.get("email", ""),
        source="generic",
        provider="directurl",
        folder="api",
        mid="",
        sender=sender,
        subject=subject,
        body="",
        html_body=html_body,
        received_at=received_at,
    )
    if sender_filter:
        needle = sender_filter.lower()
        haystack = f"{message.get('sender', '')} {message.get('subject', '')} {message.get('body', '')}".lower()
        if needle not in haystack:
            return []
    return [message][:limit]


def fetch_mailtoken_messages(account: dict[str, Any], *, limit: int, sender_filter: str = "") -> list[dict[str, Any]]:
    """接码链接形如 http://host:port/m#token：token 在 fragment 里，需两步 API 取信。
    ① GET /api/email-token/<token> 换出 account；② POST /api/emails {account} 拿邮件数组。"""
    url = coerce_text(account.get("pickup_url"))
    parsed = urllib.parse.urlsplit(url)
    if not parsed.scheme or not parsed.netloc:
        raise RuntimeError("mailtoken: missing url")
    token = parsed.fragment
    if not token:
        raise RuntimeError("mailtoken: missing token in url fragment")
    base = f"{parsed.scheme}://{parsed.netloc}"
    try:
        tok = http_request_json(f"{base}/api/email-token/{urllib.parse.quote(token)}", timeout=30)
    except Exception as exc:
        raise RuntimeError(f"mailtoken: {exc}") from exc
    if not tok.get("ok"):
        raise RuntimeError(f"mailtoken: {tok.get('error') or 'token invalid'}")
    acct = coerce_text(tok.get("account"))
    if not acct:
        raise RuntimeError("mailtoken: empty account")
    try:
        data = http_request_json(f"{base}/api/emails", method="POST", json_data={"account": acct}, timeout=40)
    except Exception as exc:
        raise RuntimeError(f"mailtoken: {exc}") from exc
    if not data.get("ok"):
        raise RuntimeError(f"mailtoken: {data.get('error') or 'fetch failed'}")
    rows = data.get("emails") if isinstance(data.get("emails"), list) else []
    messages: list[dict[str, Any]] = []
    for row in rows[: max(limit * 2, limit)]:
        if not isinstance(row, dict):
            continue
        messages.append(normalize_message(
            account=account.get("email", ""),
            source="generic",
            provider="mailtoken",
            folder=first_text(row.get("folder")) or "INBOX",
            mid=first_text(row.get("id"), row.get("message_id")),
            sender=first_text(row.get("from"), row.get("sender")),
            subject=first_text(row.get("subject")),
            body=first_text(row.get("body"), row.get("text")),
            html_body=first_text(row.get("html"), row.get("body_html")),
            received_at=first_text(row.get("date"), row.get("received_at")),
        ))
        if len(messages) >= limit:
            break
    if sender_filter:
        needle = sender_filter.lower()
        messages = [m for m in messages if needle in f"{m.get('sender', '')} {m.get('subject', '')} {m.get('body', '')}".lower()]
    return messages[:limit]


def first_text(*values: Any) -> str:
    for value in values:
        text = coerce_text(value)
        if text:
            return text
    return ""


def normalize_message(**kwargs: Any) -> dict[str, Any]:
    subject = coerce_text(kwargs.get("subject"))
    body_text = strip_html(coerce_text(kwargs.get("body")))
    html_body = sanitize_email_html(coerce_text(kwargs.get("html_body")))
    html_text = strip_html(html_body)
    if not body_text and html_body:
        body_text = html_text
    text = strip_html(f"{subject}\n{body_text}\n{html_text}")
    links = extract_links(text)
    codes = extract_codes(text)
    mail_type = normalize_mail_type("", f"{kwargs.get('sender', '')} {subject} {text}")
    return {
        **kwargs,
        "source": kwargs.get("source", "microsoft"),
        "mail_type": mail_type,
        "mail_type_label": MAIL_TYPE_LABELS.get(mail_type, "other"),
        "body": body_text[:6000],
        "html_body": html_body[:200000],
        "preview": " ".join((body_text or subject).split())[:260],
        "codes": codes,
        "links": links[:12],
    }


def normalize_mail_type(value: Any, text: str = "") -> str:
    raw = coerce_text(value).strip().lower()
    haystack = f"{raw} {coerce_text(text)}".lower()
    if any(word in haystack for word in [
        "access deactivated",
        "account deactivated",
        "deleted or deactivated",
        "deactivated",
        "disabled",
        "banned",
        "suspended",
        "封禁",
        "停用",
        "禁用",
    ]):
        return "banned"
    if (
        any(word in haystack for word in [
            "verify",
            "verification",
            "otp",
            "confirm",
            "验证码",
            "安全代码",
            "認証コード",
            "認証番号",
            "検証コード",
            "確認コード",
            "ワンタイム",
            "一時ログインコード",
        ])
        and re.search(r"\b\d{4,8}\b", haystack)
    ):
        return "verification"
    if any(word in haystack for word in ["invite", "invitation", "join", "team", "邀请"]):
        return "invite"
    if any(word in haystack for word in ["security", "alert", "sign-in", "login", "unusual", "安全", "登录", "multi-factor", "mfa"]):
        return "security"
    if any(word in haystack for word in [
        "images",
        "image",
        "reimagine",
        "plus plan",
        "start creating",
        "launch",
        "promo",
        "promotion",
        "newsletter",
        "digest",
        "update",
        "introducing",
        "通知",
        "订阅",
        "推广",
    ]):
        return "promotion"
    if raw == "reset":
        return "security"
    if raw in {"billing", "newsletter"}:
        return "promotion"
    return raw if raw in {"verification", "invite", "security", "promotion", "banned", "other"} else "other"


def sanitize_email_html(value: str) -> str:
    text = str(value or "")
    if not text:
        return ""
    text = re.sub(r"<\s*(script|iframe|object|embed|form|input|button|select|textarea)\b.*?</\s*\1\s*>", "", text, flags=re.I | re.S)
    text = re.sub(r"<\s*(script|iframe|object|embed|form|input|button|select|textarea|meta)\b[^>]*>", "", text, flags=re.I | re.S)
    text = re.sub(r"\s+on[a-z]+\s*=\s*(\"[^\"]*\"|'[^']*'|[^\s>]+)", "", text, flags=re.I)
    text = re.sub(r"\s+(href|src)\s*=\s*(['\"])\s*javascript:[^'\"]*\2", "", text, flags=re.I)
    text = re.sub(r"\s+(href|src)\s*=\s*javascript:[^\s>]+", "", text, flags=re.I)
    return text


def strip_html(text: str) -> str:
    text = re.sub(r"<(script|style).*?</\1>", " ", text, flags=re.I | re.S)
    text = re.sub(r"<[^>]+>", " ", text)
    return html.unescape(text)


def extract_links(text: str) -> list[str]:
    links = re.findall(r"https?://[^\s<>'\")]+", text)
    clean: list[str] = []
    seen: set[str] = set()
    for link in links:
        link = link.rstrip(".,;]")
        if link not in seen:
            seen.add(link)
            clean.append(link)
    return clean


def extract_codes(text: str) -> list[str]:
    found: list[str] = []
    seen: set[str] = set()
    for pattern in CODE_PATTERNS:
        for match in re.finditer(pattern, text, flags=re.I):
            code = match.group(1)
            if code.lower() in {"ffffff", "000000"}:
                continue
            if not code.isdigit() and not (re.search(r"[A-Za-z]", code) and re.search(r"\d", code)):
                continue
            if code not in seen:
                seen.add(code)
                found.append(code)
    return found[:10]


def coerce_text(value: Any) -> str:
    return str(value or "").strip()


def _cffi_proxies(proxy_url: str) -> dict[str, str] | None:
    """把 proxy_url 转成 curl_cffi 用的 proxies dict。

    socks5 统一按 socks5h（域名交代理远端解析）处理：避免本地 DNS 被代理客户端
    fake-ip(198.18.x.x)污染后把假 IP 发给远端服务器。curl_cffi/libcurl 原生支持
    socks5h（0.15.0 实测可用）——不要再转 http：专用 SOCKS 端口收到 HTTP CONNECT
    会直接中止连接（curl 56 Proxy CONNECT aborted）。"""
    if not proxy_url:
        return None
    try:
        parsed = urllib.parse.urlparse(proxy_url)
    except Exception:
        return {"http": proxy_url, "https": proxy_url}
    scheme = (parsed.scheme or "").lower()
    if scheme == "socks5":
        converted = "socks5h" + proxy_url[len("socks5"):]
        return {"http": converted, "https": converted}
    return {"http": proxy_url, "https": proxy_url}


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> None:
        return None


class LoginFlowError(RuntimeError):
    def __init__(
        self,
        message: str,
        *,
        code: str = "login_failed",
        hint: str = "",
        status: int | None = None,
        retryable: bool = True,
    ):
        super().__init__(message)
        self.code = code
        self.hint = hint
        self.status = status
        self.retryable = retryable


class PhoneNumberRejected(Exception):
    """单个接码手机号未能完成验证（被风控拒绝 / 未收到验证码 / 验证码无效）。
    用于触发换号重试。disable=True 表示该号应被永久停用（如 fraud_guard）。"""
    def __init__(self, reason: str, *, disable: bool = True):
        super().__init__(reason)
        self.reason = reason or "手机号验证失败"
        self.disable = disable


class _NoPhoneAvailable(Exception):
    """号码池已无可用手机号（均已停用/绑满/冷却/过期/试过）。"""


@dataclass
class ProtocolResponse:
    status: int
    url: str
    headers: Any
    text: str

    def json(self) -> dict[str, Any]:
        if not self.text.strip():
            return {}
        try:
            data = json.loads(self.text)
        except json.JSONDecodeError:
            return {"raw": self.text[:5000]}
        return data if isinstance(data, dict) else {"data": data}

    def location(self) -> str:
        return self.headers.get("Location") or self.headers.get("location") or ""


def protocol_compact_error(data: Any) -> str:
    def auth_block_hint(value: str) -> str:
        if "Unable to load site" not in value and "using a VPN" not in value:
            return ""
        ip_match = re.search(r"\[IP:([^\]|]+)", value)
        ray_match = re.search(r"Ray ID:([a-zA-Z0-9]+)", value)
        suffix = []
        if ip_match:
            suffix.append(f"IP {ip_match.group(1).strip()}")
        if ray_match:
            suffix.append(f"Ray {ray_match.group(1).strip()}")
        extra = f" ({', '.join(suffix)})" if suffix else ""
        return f"OpenAI 登录端点拒绝了当前服务器/IP。协议登录不会自动切换其他方案，请换出口 IP 或配置稳定代理后重试。{extra}"

    if not data:
        return "empty response"
    if isinstance(data, str):
        hint = auth_block_hint(data)
        if hint:
            return hint
        if looks_like_html_challenge(data):
            return html_challenge_hint(data)
        clean = strip_html(data).strip()
        return (clean or data)[:260]
    if isinstance(data, dict):
        raw = coerce_text(data.get("raw"))
        if raw:
            hint = auth_block_hint(raw)
            if hint:
                return hint
            if looks_like_html_challenge(raw):
                return html_challenge_hint(raw)
            clean = strip_html(raw).strip()
        err = data.get("error")
        if isinstance(err, str):
            if looks_like_html_challenge(err):
                return html_challenge_hint(err)
            return err[:260]
        if isinstance(err, dict):
            parts = [err.get("message"), err.get("code"), err.get("type")]
            return " / ".join(str(item) for item in parts if item)[:260] or json.dumps(err, ensure_ascii=False)[:260]
        for key in ("message", "detail", "error_description", "raw"):
            if data.get(key):
                value = str(data.get(key))
                hint = auth_block_hint(value)
                if hint:
                    return hint
                if looks_like_html_challenge(value):
                    return html_challenge_hint(value)
                clean = strip_html(value).strip()
                return (clean or value)[:260]
    try:
        return json.dumps(data, ensure_ascii=False)[:260]
    except Exception:
        return str(data)[:260]


def looks_like_html_challenge(value: str) -> bool:
    text = coerce_text(value)
    if not text:
        return False
    lowered = text.lower()
    return bool(
        "<html" in lowered
        or "<body" in lowered
        or "body{font-family" in lowered
        or "cf-ray" in lowered
        or "cloudflare" in lowered
        or "csrf request failed" in lowered
        or "could not validate your token" in lowered
        or "access denied" in lowered
        or "unable to load site" in lowered
    )


def html_challenge_hint(value: str) -> str:
    clean = re.sub(r"<style.*?</style>", " ", value, flags=re.I | re.S)
    clean = re.sub(r"<script.*?</script>", " ", clean, flags=re.I | re.S)
    clean = strip_html(clean)
    clean = re.sub(r"\s+", " ", clean).strip()
    lowered = value.lower()
    if "body{font-family" in lowered or "@keyframes" in lowered or ".container{" in lowered:
        return "ChatGPT 登录入口返回了风控/拒绝页。当前 VPS 或代理出口被目标站拦截，请更换稳定代理或干净出口后重试。"
    if "csrf request failed" in lowered or "could not validate your token" in lowered:
        return "CSRF 校验失败：登录会话的 cookie/state/token 不匹配或已失效。请保持同一代理出口后重试协议登录。"
    if "cloudflare" in lowered or "cf-ray" in lowered or "access denied" in lowered:
        return "目标站点返回了风控/Cloudflare 拒绝页。协议登录不会自动切换其他方案，请换干净出口 IP 或稳定代理后重试。"
    if "unable to load site" in lowered or "using a vpn" in lowered:
        return "目标站点拒绝当前网络出口。请换 VPS 出口 IP 或使用稳定代理。"
    return clean[:260] or "目标站点返回 HTML 拒绝页，未返回可用 JSON。"


def generate_random_password(length: int = 16) -> str:
    """生成随机强密码：必含大小写字母、数字、符号（满足 OpenAI 密码策略）。"""
    letters_lower = "abcdefghijklmnopqrstuvwxyz"
    letters_upper = letters_lower.upper()
    digits = "0123456789"
    special = "!@#$%^&*"
    alphabet = letters_lower + letters_upper + digits + special
    while True:
        pwd = "".join(secrets.choice(alphabet) for _ in range(length))
        if (any(c in letters_lower for c in pwd) and any(c in letters_upper for c in pwd)
                and any(c in digits for c in pwd) and any(c in special for c in pwd)):
            return pwd


class ChatGPTProtocolLogin:
    def __init__(self, job_id: str, payload: dict[str, Any]):
        self.job_id = job_id
        self.payload = payload
        self.proxy_url = request_proxy_url(payload)
        self.cookie_jar = http.cookiejar.CookieJar()
        handlers: list[Any] = [
            urllib.request.HTTPCookieProcessor(self.cookie_jar),
            NoRedirectHandler(),
        ]
        if self.proxy_url and urllib.parse.urlparse(self.proxy_url).scheme.lower() in {"http", "https"}:
            handlers.append(urllib.request.ProxyHandler({
                "http": self.proxy_url,
                "https": self.proxy_url,
            }))
        elif not self.proxy_url:
            handlers.append(urllib.request.ProxyHandler({}))
        self.opener = urllib.request.build_opener(
            *handlers,
        )
        self.auth_url = ""
        self.login_url = ""
        self.state = ""
        self.device_id = ""
        self.sentinel_token = ""
        self.oauth_state = ""
        self.oauth_code_verifier = ""
        self.oauth_redirect_uri = OPENAI_OAUTH_REDIRECT_URI
        self.oauth_client_id = OPENAI_CODEX_CLIENT_ID
        self.oauth_authorize_url = ""
        self.oauth_authorize_source = "local"
        self.oauth_cpa_state = ""
        self.changed_password = ""

    def log(self, step: str, message: str, level: str = "info") -> None:
        append_login_log(self.job_id, message, level, step)

    def trace_headers(self) -> dict[str, str]:
        parent_id = secrets.randbits(63) or 1
        return {
            "traceparent": f"00-{secrets.token_hex(16)}-{parent_id:016x}-01",
            "tracestate": "dd=s:1;o:rum",
            "x-datadog-origin": "rum",
            "x-datadog-parent-id": str(parent_id),
            "x-datadog-sampling-priority": "1",
            "x-datadog-trace-id": str(secrets.randbits(63) or 1),
        }

    def headers(self, url: str, extra: dict[str, str] | None = None) -> dict[str, str]:
        parsed = urllib.parse.urlparse(url)
        path = parsed.path or ""
        accept = "application/json"
        if extra and extra.get("Accept"):
            accept = extra["Accept"]
        is_navigation = "text/html" in accept
        final_headers = {
            "User-Agent": DEFAULT_HTTP_HEADERS["User-Agent"],
            "Accept": accept,
            "Accept-Language": "en-US,en;q=0.9",
            "sec-ch-ua": OPENAI_SEC_CH_UA,
            "sec-ch-ua-arch": '"x86_64"',
            "sec-ch-ua-bitness": '"64"',
            "sec-ch-ua-full-version-list": OPENAI_SEC_CH_UA_FULL_VERSION_LIST,
            "sec-ch-ua-mobile": "?0",
            "sec-ch-ua-model": '""',
            "sec-ch-ua-platform": '"Windows"',
            "sec-ch-ua-platform-version": '"10.0.0"',
            "sec-fetch-dest": "document" if is_navigation else "empty",
            "sec-fetch-mode": "navigate" if is_navigation else "cors",
            "sec-fetch-site": "same-origin",
            "oai-device-id": self.device_id or "",
        }
        if is_navigation:
            final_headers["sec-fetch-user"] = "?1"
        else:
            final_headers.update(self.trace_headers())
            final_headers.setdefault("Origin", f"{parsed.scheme}://{parsed.netloc}" if parsed.scheme and parsed.netloc else "https://auth.openai.com")
            if path.startswith("/api/") or "/api/" in path:
                final_headers.setdefault("Content-Type", "application/json")
        if not final_headers["oai-device-id"]:
            final_headers.pop("oai-device-id", None)
        if extra:
            final_headers.update(extra)
        return final_headers

    def request(
        self,
        url: str,
        *,
        method: str = "GET",
        json_data: dict[str, Any] | None = None,
        form_data: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
        timeout: int = 60,
    ) -> ProtocolResponse:
        body = None
        final_headers = dict(headers or {})
        if json_data is not None:
            body = json.dumps(json_data, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            final_headers.setdefault("Content-Type", "application/json")
        elif form_data is not None:
            body = urllib.parse.urlencode(form_data).encode("utf-8")
            final_headers.setdefault("Content-Type", "application/x-www-form-urlencoded")
        # 用 cookie_jar 计算本次应携带的 Cookie 头（cookie_jar 仍是唯一真相源）
        cookie_req = urllib.request.Request(url, method=method)
        self.cookie_jar.add_cookie_header(cookie_req)
        cookie_header = cookie_req.get_header("Cookie")
        if cookie_header:
            final_headers["Cookie"] = cookie_header
        # 关键：底层走 curl_cffi（chrome120 指纹）而非 urllib——auth.openai.com 的
        # Cloudflare Bot Management 按 TLS/JA3 指纹拦截，urllib(Python 指纹)一律 403，
        # curl_cffi 浏览器指纹可放行。为保持指纹一致，丢弃自定义 UA / sec-ch-ua，
        # 交给 impersonate 生成。
        for drop in ("User-Agent", "sec-ch-ua", "sec-ch-ua-full-version-list",
                     "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-ch-ua-arch",
                     "sec-ch-ua-bitness", "sec-ch-ua-model", "sec-ch-ua-platform-version"):
            final_headers.pop(drop, None)

        from curl_cffi import requests as cffi_requests  # type: ignore
        last_error = ""
        attempts = 3 if self.proxy_url else 2
        for attempt in range(attempts):
            try:
                resp = cffi_requests.request(
                    method,
                    url,
                    headers=final_headers,
                    data=body,
                    impersonate=OPENAI_IMPERSONATE,
                    proxies=_cffi_proxies(self.proxy_url),
                    timeout=timeout,
                    allow_redirects=False,
                )
            except Exception as exc:
                last_error = str(exc)
                if attempt + 1 < attempts:
                    time.sleep(0.6 + attempt * 0.7)
                    continue
                raise RuntimeError(f"network error: {network_error_message(url, exc)}") from exc
            try:
                set_cookies = resp.headers.get_list("Set-Cookie")
            except Exception:
                set_cookies = []
            if set_cookies:
                self._extract_cookies_into_jar(url, method, set_cookies)
            return ProtocolResponse(int(resp.status_code), str(resp.url or url), resp.headers, resp.text)
        raise RuntimeError(last_error or "请求失败")

    def _extract_cookies_into_jar(self, url: str, method: str, set_cookie_headers: list[str]) -> None:
        """把 curl_cffi 响应的原始 Set-Cookie 串按请求 host 补全 domain 后存入 cookie_jar。"""
        req = urllib.request.Request(url, method=method)

        class _SetCookieAdapter:
            def __init__(self, hdrs: list[str]):
                self._hdrs = hdrs

            def info(self):
                return self

            def get_all(self, name, default=None):
                if name.lower() == "set-cookie":
                    return self._hdrs
                return default if default is not None else []

        try:
            self.cookie_jar.extract_cookies(_SetCookieAdapter(set_cookie_headers), req)
        except Exception:
            pass

    def login(self) -> dict[str, Any]:
        email_addr = coerce_text(self.payload.get("email"))
        password = coerce_text(self.payload.get("password"))
        force_email_code = str(first_text(
            self.payload.get("force_email_code"),
            self.payload.get("forceEmailCode"),
            self.payload.get("email_code_login"),
            self.payload.get("emailCodeLogin"),
        )).lower() in {"1", "true", "yes", "on"}
        if force_email_code:
            password = ""
        if not email_addr:
            raise RuntimeError("protocol login needs email")

        self.device_id = self.device_id or uuid.uuid4().hex
        self.set_cookie("oai-did", self.device_id, "auth.openai.com")
        self.set_cookie("oai-did", self.device_id, ".auth.openai.com")
        self.set_cookie("oai-did", self.device_id, "chatgpt.com")
        self.set_cookie("oai-did", self.device_id, ".chatgpt.com")

        self.log("oauth_init", "后端协议：生成 OpenAI OAuth 授权会话")
        self.auth_url = self.prepare_oauth_authorize_url()
        self.log("authorize", "后端协议：打开 OAuth 授权入口并建立 login_session")
        login_state = self.bootstrap_oauth_session(self.auth_url)
        if not login_state.get("ok"):
            raw_error = login_state.get("error") or "OAuth 授权入口没有建立 auth.openai.com 登录会话。"
            raw_hint = login_state.get("hint") or "CPA 已返回授权链接，但协议链路没有拿到 auth.openai.com 的 login_session；请看 authorize 日志里的最终 URL、HTTP 状态和响应摘要。"
            error_code = "oauth_session_missing"
            lowered_error = f"{raw_error} {raw_hint}".lower()
            if "unsupported_country_region_territory" in lowered_error or "country, region, or territory not supported" in lowered_error:
                error_code = "unsupported_country_region_territory"
                raw_hint = "OpenAI OAuth 明确拒绝当前后端出口：所在国家/地区不受支持。这一步还没有到邮箱验证码，也不是邮箱/JWT问题；请看“当前后端出口”日志，换成 OpenAI 支持地区的 HTTP 代理或 VPS 出口后重试。"
            raise LoginFlowError(
                raw_error,
                code=error_code,
                hint=raw_hint,
                status=login_state.get("status") if isinstance(login_state.get("status"), int) else None,
                retryable=True,
            )

        issued_after = time.time()
        self.log("sentinel", "Protocol login: generate Sentinel token")
        self.sentinel_token = generate_openai_sentinel_token(self.device_id, "authorize_continue", self.proxy_url)
        if not self.sentinel_token:
            self.log("sentinel", "Sentinel token helper returned empty token; continuing once", "warning")

        # 接码链接(directurl)无邮件时间戳：必须在“提交邮箱触发发码”之前采集旧码基线，
        # 否则发码后再采集会把本次新码误当旧码排除。预采集结果存进 payload 供取码时复用。
        if login_payload_has_directurl(self.payload):
            baseline = prime_directurl_baseline(self.payload)
            self.payload["_directurl_baseline_codes"] = sorted(baseline)
            self.log("mail_code_baseline", f"接码链接基线已记录 {len(baseline)} 个旧验证码（已在发码前采集，只接受新码）", "info")

        self.log("identifier", "Protocol login: submit email")
        step = self.authorize_continue(email_addr)
        continue_url = self.complete_modern_login(step, password, issued_after)
        self.log("callback", "后端协议：跟随 OAuth 后续页面并捕获 callback code")
        callback_url, final_url = self.capture_oauth_callback(continue_url or self.auth_url)
        if not callback_url and continue_url:
            callback_url, final_url = self.capture_oauth_callback(self.auth_url)
        if not callback_url:
            raise RuntimeError(f"OAuth flow did not return callback code; final={final_url[:220] if final_url else 'empty'}")

        if self.oauth_authorize_source == "chatgpt_web":
            self.log("session", "后端协议：完成 ChatGPT callback 并读取 Web Session")
        else:
            self.log("token", "后端协议：交换 OpenAI OAuth token")
        session = self.exchange_oauth_callback(callback_url)
        email_from_token = access_token_email(session.get("access_token", ""))
        if email_from_token:
            session["email"] = email_from_token
        else:
            session["email"] = email_addr
        session["user"] = {**(session.get("user") if isinstance(session.get("user"), dict) else {}), "email": session["email"]}
        if self.changed_password:
            session["changed_password"] = self.changed_password
        self.log("success", "Protocol login succeeded", "success")
        return session





    def set_cookie(self, name: str, value: str, domain: str, path: str = "/") -> None:
        if not value:
            return
        cookie = http.cookiejar.Cookie(
            version=0,
            name=name,
            value=value,
            port=None,
            port_specified=False,
            domain=domain,
            domain_specified=True,
            domain_initial_dot=domain.startswith("."),
            path=path,
            path_specified=True,
            secure=True,
            expires=None,
            discard=True,
            comment=None,
            comment_url=None,
            rest={},
            rfc2109=False,
        )
        self.cookie_jar.set_cookie(cookie)

    def prepare_oauth_authorize_url(self) -> str:
        self.oauth_authorize_source = "local"
        self.oauth_state = secrets.token_urlsafe(32)
        self.oauth_code_verifier = generate_openai_code_verifier()
        authorize_url = build_openai_oauth_authorize_url(self.oauth_state, openai_code_challenge(self.oauth_code_verifier))
        authorize_url = self.append_password_reset_params(authorize_url)
        self.remember_oauth_params_from_authorize_url(authorize_url)
        return authorize_url

    def append_password_reset_params(self, authorize_url: str) -> str:
        """登录后改密扩展流程：authorize 带 post_login_password_reset=true 时,
        登录(TOTP)通过后会落到 reset-password 页而不是直接回调,此时可提交新密码。"""
        if not self.payload.get("_change_password"):
            return authorize_url
        if "post_login_password_reset=" in authorize_url:
            return authorize_url
        separator = "&" if "?" in authorize_url else "?"
        return f"{authorize_url}{separator}post_login_password_reset=true&max_age=0"

    def remember_oauth_params_from_authorize_url(self, authorize_url: str) -> None:
        parsed = urllib.parse.urlparse(authorize_url)
        query = urllib.parse.parse_qs(parsed.query)
        self.oauth_authorize_url = authorize_url
        self.oauth_state = first_text(query.get("state", [""])[0], self.oauth_state, self.oauth_cpa_state)
        self.oauth_redirect_uri = first_text(query.get("redirect_uri", [""])[0], self.oauth_redirect_uri, OPENAI_OAUTH_REDIRECT_URI)
        self.oauth_client_id = first_text(query.get("client_id", [""])[0], self.oauth_client_id, OPENAI_CODEX_CLIENT_ID)
        if not self.oauth_code_verifier and self.oauth_authorize_source == "local":
            self.oauth_code_verifier = generate_openai_code_verifier()

    def bootstrap_oauth_session(self, authorize_url: str) -> dict[str, Any]:
        attempts = [
            ("CPA 授权链接", authorize_url, "https://chatgpt.com/"),
            ("OpenAI OAuth API", self.oauth2_auth_url_from_authorize(authorize_url), authorize_url),
        ]
        best: dict[str, Any] = {"ok": False, "final_url": "", "status": None, "hint": ""}
        seen_starts: set[str] = set()
        for label, start_url, referer in attempts:
            if not start_url or start_url in seen_starts:
                continue
            seen_starts.add(start_url)
            state = self.follow_oauth_authorize_chain(start_url, referer, label)
            if state.get("ok"):
                return state
            if state.get("final_url") or state.get("status") is not None or state.get("hint"):
                best = state
        final_url = coerce_text(best.get("final_url"))
        if final_url:
            self.login_url = final_url if "auth.openai.com" in final_url else "https://auth.openai.com/log-in"
        ok = self.has_auth_session_cookie()
        if ok:
            return {"ok": True, "final_url": final_url}
        cookie_names = self.auth_cookie_names()
        final_label = self.safe_url_for_log(final_url) if final_url else "空"
        status = best.get("status")
        hint = coerce_text(best.get("hint")) or "没有收到 login_session / oai-client-auth-session cookie"
        error = f"OAuth 授权入口没有建立 auth.openai.com 登录会话：final={final_label}，HTTP {status or '-'}，cookies={cookie_names or '无'}，摘要：{hint}"
        self.log("authorize", error[:700], "error")
        return {**best, "ok": False, "error": error, "hint": hint}

    def follow_oauth_authorize_chain(self, start_url: str, referer: str, label: str, max_hops: int = 12) -> dict[str, Any]:
        current_url = self.normalize_auth_url(start_url)
        last_url = current_url
        last_status: int | None = None
        last_hint = ""
        visited: set[str] = set()
        for hop in range(max_hops):
            if not current_url or current_url in visited:
                break
            visited.add(current_url)
            try:
                resp = self.request(
                    current_url,
                    headers=self.headers(current_url, {
                        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7",
                        "Referer": referer or last_url or "https://chatgpt.com/",
                        "Upgrade-Insecure-Requests": "1",
                    }),
                    timeout=45,
                )
            except Exception as exc:
                last_hint = str(exc)[:260]
                self.log("authorize", f"OAuth {label} 第 {hop + 1} 跳请求异常：{last_hint}", "warning")
                return {"ok": False, "final_url": current_url, "status": last_status, "hint": last_hint}

            last_status = resp.status
            last_url = resp.url or current_url
            last_hint = self.oauth_response_hint(resp)
            next_url = self.next_oauth_authorize_url(resp, current_url)
            log_parts = [
                f"OAuth {label} 第 {hop + 1} 跳：HTTP {resp.status}",
                self.safe_url_for_log(last_url),
            ]
            if next_url:
                log_parts.append(f"-> {self.safe_url_for_log(next_url)}")
            elif last_hint:
                log_parts.append(f"摘要：{last_hint[:180]}")
            self.log("authorize", " ".join(log_parts), "info" if next_url or self.has_auth_session_cookie() else "warning")

            if self.has_auth_session_cookie() and not next_url:
                final_url = last_url
                self.login_url = final_url if "auth.openai.com" in final_url else "https://auth.openai.com/log-in"
                return {"ok": True, "final_url": final_url, "status": resp.status, "hint": last_hint}

            if not next_url:
                break
            referer = current_url
            current_url = self.normalize_auth_url(next_url)

        return {"ok": False, "final_url": last_url, "status": last_status, "hint": last_hint}

    def next_oauth_authorize_url(self, resp: ProtocolResponse, current_url: str) -> str:
        if resp.status in {301, 302, 303, 307, 308} and resp.location():
            return urllib.parse.urljoin(current_url, resp.location())
        data = resp.json()
        candidates: list[str] = []
        if isinstance(data, dict):
            nested = data.get("data") if isinstance(data.get("data"), dict) else {}
            candidates.extend([
                coerce_text(data.get("continue_url")),
                coerce_text(data.get("continueUrl")),
                coerce_text(data.get("url")),
                coerce_text(data.get("redirect_url")),
                coerce_text(data.get("redirectUrl")),
                coerce_text(data.get("authorize_url")),
                coerce_text(data.get("auth_url")),
                coerce_text(nested.get("continue_url")),
                coerce_text(nested.get("continueUrl")),
                coerce_text(nested.get("url")),
                coerce_text(nested.get("redirect_url")),
                coerce_text(nested.get("redirectUrl")),
                coerce_text(nested.get("authorize_url")),
                coerce_text(nested.get("auth_url")),
            ])
        text = resp.text or ""
        patterns = [
            r"window\.location(?:\.href)?\s*=\s*['\"]([^'\"]+)['\"]",
            r"location\.replace\(\s*['\"]([^'\"]+)['\"]",
            r"<a\b[^>]+href=['\"]([^'\"]+)['\"]",
            r"<form\b[^>]+action=['\"]([^'\"]+)['\"]",
        ]
        for pattern in patterns:
            for match in re.finditer(pattern, text, flags=re.I):
                candidates.append(html.unescape(match.group(1)))
        for candidate in candidates:
            candidate = coerce_text(candidate)
            if not candidate:
                continue
            joined = urllib.parse.urljoin(current_url, candidate)
            parsed = urllib.parse.urlparse(joined)
            if parsed.scheme in {"http", "https"} and parsed.netloc and self.is_oauth_chain_url(joined, current_url):
                return joined
        return ""

    @staticmethod
    def is_oauth_chain_url(candidate_url: str, current_url: str) -> bool:
        try:
            parsed = urllib.parse.urlparse(candidate_url)
            host = (parsed.hostname or "").lower()
            marker = f"{parsed.path}?{parsed.query}".lower()
            oauth_markers = ("oauth", "auth", "callback", "login", "log-in", "authorize", "accounts", "session", "email-verification", "consent", "workspace", "organization", "codex")
            if host in {"auth.openai.com", "auth0.openai.com", "chatgpt.com"}:
                return any(part in marker for part in oauth_markers)
            if "auth" in host and "openai.com" in host:
                return any(part in marker for part in oauth_markers)
            current = urllib.parse.urlparse(current_url)
            if host and host == (current.hostname or "").lower():
                return any(part in marker for part in oauth_markers)
        except Exception:
            return False
        return False

    def oauth_response_hint(self, resp: ProtocolResponse) -> str:
        content_type = coerce_text(resp.headers.get("Content-Type") or resp.headers.get("content-type")).lower()
        text = resp.text or ""
        if "json" in content_type or text.lstrip().startswith(("{", "[")):
            return protocol_compact_error(resp.json())
        return protocol_compact_error(text)

    def has_auth_session_cookie(self) -> bool:
        return self.has_cookie("login_session") or self.has_cookie("oai-client-auth-session")

    def auth_cookie_names(self) -> str:
        names = sorted({cookie.name for cookie in self.cookie_jar if "openai.com" in coerce_text(cookie.domain)})
        return ",".join(names)

    @staticmethod
    def safe_url_for_log(value: str) -> str:
        raw = coerce_text(value)
        if not raw:
            return ""
        try:
            parsed = urllib.parse.urlparse(raw)
            if not parsed.scheme or not parsed.netloc:
                return raw[:220]
            query = urllib.parse.parse_qs(parsed.query)
            safe_items: list[tuple[str, str]] = []
            for key in ("response_type", "client_id", "redirect_uri", "state", "screen_hint", "email"):
                if key not in query:
                    continue
                item = coerce_text(query.get(key, [""])[0])
                if key == "state" and len(item) > 10:
                    item = f"...{item[-8:]}"
                elif key == "email" and item:
                    item = "***"
                elif key == "redirect_uri" and item:
                    p = urllib.parse.urlparse(item)
                    item = urllib.parse.urlunparse((p.scheme, p.netloc, p.path, "", "", ""))
                safe_items.append((key, item))
            safe_query = urllib.parse.urlencode(safe_items)
            return urllib.parse.urlunparse((parsed.scheme, parsed.netloc, parsed.path, "", safe_query, ""))[:260]
        except Exception:
            return raw[:220]

    def oauth2_auth_url_from_authorize(self, authorize_url: str) -> str:
        parsed = urllib.parse.urlparse(authorize_url)
        if not parsed.query:
            return ""
        return urllib.parse.urlunparse(("https", "auth.openai.com", "/api/oauth/oauth2/auth", "", parsed.query, ""))

    def has_cookie(self, name: str) -> bool:
        return bool(self.cookie_value(name))

    def get_csrf_token(self) -> str:
        url = "https://chatgpt.com/api/auth/csrf"
        resp = self.request(url, headers=self.headers(url, {"Referer": "https://chatgpt.com/auth/login"}))
        data = resp.json()
        csrf_token = coerce_text(data.get("csrfToken"))
        if resp.status != 200 or not csrf_token:
            compact = protocol_compact_error(data)
            proxy_text = "已启用代理" if self.proxy_url else "未启用代理"
            raise LoginFlowError(
                f"CSRF 校验失败：HTTP {resp.status} - {compact}",
                code="csrf_or_risk_blocked",
                hint=f"{proxy_text}。请确认整轮登录使用同一出口 IP/cookie 会话，然后重试协议登录。",
                status=resp.status,
                retryable=True,
            )
        return csrf_token

    def signin_openai(self, csrf_token: str) -> str:
        attempts = [
            {
                "url": "https://chatgpt.com/api/auth/signin/openai",
                "callbackUrl": "https://chatgpt.com/",
                "referer": "https://chatgpt.com/auth/login",
            },
            {
                "url": "https://chatgpt.com/api/auth/signin/login-web?callbackUrl=%2F",
                "callbackUrl": "/",
                "referer": "https://chatgpt.com/",
            },
        ]
        last_url = ""
        for attempt in attempts:
            url = attempt["url"]
            resp = self.request(
                url,
                method="POST",
                form_data={"callbackUrl": attempt["callbackUrl"], "csrfToken": csrf_token, "json": "true"},
                headers=self.headers(url, {
                    "Origin": "https://chatgpt.com",
                    "Referer": attempt["referer"],
                }),
            )
            data = resp.json()
            last_url = coerce_text(data.get("url") or resp.location())
            if last_url and "/api/auth/signin?csrf=true" not in last_url:
                return urllib.parse.urljoin(url, last_url)
        raise RuntimeError(f"signin did not return authorize URL: {last_url or 'empty'}")

    def follow_authorize(self, auth_url: str) -> dict[str, Any]:
        state = {"is_modern": False, "login_url": "", "last_url": auth_url}
        current_url = auth_url
        for _ in range(12):
            parsed = urllib.parse.urlparse(current_url)
            if parsed.query:
                qs = urllib.parse.parse_qs(parsed.query)
                self.state = first_text(qs.get("state", [""])[0], self.state)
            if parsed.hostname == "auth.openai.com" and (
                "/api/accounts/authorize" in parsed.path or parsed.path == "/log-in"
            ):
                state["is_modern"] = True

            resp = self.request(
                current_url,
                headers=self.headers(current_url, {
                    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                    "Referer": "https://chatgpt.com/",
                }),
            )
            state["last_url"] = current_url
            if 300 <= resp.status < 400 and resp.location():
                current_url = urllib.parse.urljoin(current_url, resp.location())
                state["last_url"] = current_url
                loc = urllib.parse.urlparse(current_url)
                if loc.query:
                    qs = urllib.parse.parse_qs(loc.query)
                    self.state = first_text(qs.get("state", [""])[0], self.state)
                if loc.hostname == "auth.openai.com" and loc.path == "/log-in":
                    state["is_modern"] = True
                    state["login_url"] = current_url
                    self.login_url = current_url
                    self.request(
                        current_url,
                        headers=self.headers(current_url, {
                            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                            "Referer": "https://chatgpt.com/auth/login",
                        }),
                    )
                    return state
                if "/u/login/identifier" in current_url or "/u/login/password" in current_url:
                    state["login_url"] = current_url
                    self.login_url = current_url
                    return state
                continue
            if parsed.hostname in {"auth.openai.com", "auth0.openai.com"}:
                state["login_url"] = current_url
                self.login_url = current_url
                return state
            break
        return state

    def authorize_continue(self, email_addr: str) -> dict[str, Any]:
        url = "https://auth.openai.com/api/accounts/authorize/continue"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": "https://auth.openai.com/log-in?usernameKind=email",
        })
        if self.sentinel_token:
            headers["openai-sentinel-token"] = self.sentinel_token
        payload = {"username": {"kind": "email", "value": email_addr}}
        resp = self.request(
            url,
            method="POST",
            json_data=payload,
            headers=headers,
        )
        data = resp.json()
        if resp.status == 400 and "invalid_auth_step" in json.dumps(data, ensure_ascii=False):
            self.log("authorize", "OAuth login_session 失效，重新建立授权会话后重试", "warning")
            self.bootstrap_oauth_session(self.auth_url)
            headers = self.headers(url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": "https://auth.openai.com/log-in?usernameKind=email",
            })
            self.sentinel_token = generate_openai_sentinel_token(self.device_id, "authorize_continue", self.proxy_url)
            if self.sentinel_token:
                headers["openai-sentinel-token"] = self.sentinel_token
            resp = self.request(
                url,
                method="POST",
                json_data=payload,
                headers=headers,
            )
            data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"submit email failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        return data

    def raise_if_passkey_or_challenge(self, step: dict[str, Any], continue_url: str = "") -> None:
        """识别 passkey/安全令牌账号或额外安全挑战页（auth_challenge）。
        这类账号无法走邮箱验证码登录，属永久失败，抛 retryable=False 让上层不再重试、也不再入队。"""
        pt = (self.extract_page_type(step) or "").lower()
        cu = (continue_url or "").lower()
        if ("auth_challenge" in pt or "passkey" in pt or "webauthn" in pt or "security_key" in pt
                or "/auth-challenge" in cu or "/passkey" in cu or "/webauthn" in cu):
            raise LoginFlowError(
                "账号绑定了 passkey/安全令牌或触发了额外安全挑战，无法用邮箱验证码登录。",
                code="passkey_or_challenge",
                hint="该账号绑定了 passkey/安全令牌，或被 OpenAI 要求额外安全挑战（auth_challenge）。"
                     "这类账号无法走邮箱验证码登录，属永久失败、不应重试或再次入队。",
                retryable=False,
            )

    def complete_modern_login(self, step: dict[str, Any], password: str, issued_after: float) -> str:
        current_step = step or {}
        continue_url = self.normalize_auth_url(self.extract_continue_url(current_step))
        page_type = self.extract_page_type(current_step)
        mode = self.extract_email_verification_mode(current_step)
        self.log("identifier", f"登录步骤：page={page_type or '-'}，mode={mode or '-'}", "info")
        # 提交邮箱后若直接落到 passkey/auth_challenge：永久失败，不再走邮箱码流程
        self.raise_if_passkey_or_challenge(current_step, continue_url)

        if (page_type == "login_password" or "/log-in/password" in continue_url) and password:
            self.log("password", "Protocol login: submit password")
            self.sentinel_token = generate_openai_sentinel_token(self.device_id, "password_verify", self.proxy_url)
            current_step = self.submit_modern_password(password)
            continue_url = self.normalize_auth_url(self.extract_continue_url(current_step))
            page_type = self.extract_page_type(current_step)
            mode = self.extract_email_verification_mode(current_step) or mode
        elif page_type == "login_password" or "/log-in/password" in continue_url:
            self.log("send_code", "Protocol login: use email code path", "info")
            self.sentinel_token = generate_openai_sentinel_token(self.device_id, "email_verification", self.proxy_url)
            if not self.kickoff_modern_otp(mode):
                self.log("send_code", "OpenAI 发码接口没有返回确认，继续等待邮箱新验证码", "warning")
            continue_url = ""
            page_type = "email_otp_verification"

        # TOTP(验证器 App)二次验证：password/verify 后进入 mfa_challenge，
        # 选 totp 因子用 2FA 密钥生成动态码提交，直接换取 OAuth callback（不经邮箱/短信）。
        if page_type == "mfa_challenge" or "/mfa-challenge" in (continue_url or ""):
            totp_secret = coerce_text(self.payload.get("_totp_secret"))
            if totp_secret:
                return self.complete_totp_challenge(current_step, totp_secret)
            raise LoginFlowError(
                "账号启用了验证器(TOTP)二次验证，但未提供 2FA 密钥。",
                code="totp_secret_missing",
                hint="请在邮件管理中为该账号补充 OpenAI TOTP 密钥后重试；邮箱验证码登录无需 GPT 密码。",
                retryable=False,
            )

        if continue_url and not self.needs_modern_otp(page_type, continue_url):
            return continue_url

        self.log("waiting_code", "Protocol login: waiting for email code")
        code = manual_email_code_for_payload(self.payload)
        if code:
            self.log("waiting_code", "使用手动填写的邮箱验证码", "info")
        elif login_payload_has_pickup_link(self.payload):
            # 接码链接邮箱（mailtoken/directurl）：经第三方服务取信，有邮件投递延迟 + ~30s 缓存窗口。
            # 发码后先等 16s 再取（投递快时一次命中），之后按 ~30s 间隔穿透缓存重试，共 3 次（约 76s）。
            code = fetch_login_verification_code(
                self.payload, since=issued_after, attempts=3, delay=30,
                fallback_to_baseline=True, initial_wait=16)
        else:
            # 普通邮箱（Outlook/IMAP 直连）：至多轮询 5 次（约 25s）；仍未取到"新码"则回退提交已有的最近验证码
            # （应对 OpenAI 短时间重试复用旧码、旧码被基线排除导致始终取不到的情况）。
            code = fetch_login_verification_code(
                self.payload, since=issued_after, attempts=5, delay=5, fallback_to_baseline=True)
        if not code:
            raise RuntimeError("no verification code was found in local mailbox credentials")

        self.log("verify_code", "Protocol login: submit email code")
        current_step = self.submit_modern_code(code)
        continue_url = self.normalize_auth_url(self.extract_continue_url(current_step))
        # 邮箱码通过后若被甩到 passkey/auth_challenge：同样永久失败
        self.raise_if_passkey_or_challenge(current_step, continue_url)
        # Email OTP can require the same TOTP challenge as password login.
        if self.extract_page_type(current_step) == "mfa_challenge" or "/mfa-challenge" in (continue_url or ""):
            totp_secret = coerce_text(self.payload.get("_totp_secret"))
            if totp_secret:
                return self.complete_totp_challenge(current_step, totp_secret)
            raise LoginFlowError(
                "账号启用了验证器(TOTP)二次验证，但未提供 2FA 密钥。",
                code="totp_secret_missing",
                hint="请在邮件管理中为该账号补充 OpenAI TOTP 密钥后重试；邮箱验证码登录无需 GPT 密码。",
                retryable=False,
            )
        if self.needs_phone_verification(current_step, continue_url):
            continue_url = self.complete_phone_verification(current_step, continue_url)
        if not continue_url:
            raise RuntimeError(f"email code accepted but no continue URL returned: {protocol_compact_error(current_step)}")
        return continue_url

    def complete_phone_verification(self, step: dict[str, Any], continue_url: str = "") -> str:
        if any(bool(self.payload.get(key)) for key in ("skip_phone_verification", "skipPhoneVerification")):
            credential_mode = coerce_text(self.payload.get("credential_mode") or self.payload.get("credentialMode")).strip().lower()
            flow_name = "邮件管理的临时 AT 登录" if credential_mode == "chatgpt_at" else "Team 轮转的 Codex OAuth"
            self.log("phone_skipped", f"OpenAI 返回了手机验证页面；{flow_name}已禁用手机接码，本次停止", "warning")
            raise LoginFlowError(
                f"OpenAI 要求手机验证，但{flow_name}已禁用手机接码。",
                code="phone_verification_skipped",
                hint="不会调用号码池或消耗手机号。请更换全局代理出口或账号后重试。",
                retryable=False,
            )
        self.log("phone_code", "账号要求手机二次验证", "warning")
        phone_hint = extract_phone_hint_from_step(step, continue_url)
        if phone_hint:
            self.payload["_detected_phone_hint"] = phone_hint
            self.log("phone_pool", f"检测到账号使用手机号尾号 {phone_hint[-4:]}", "info")

        # 账号已绑过手机号、只需选通道收码：不涉及新绑号，单次执行（不换号）
        if not self.needs_add_phone(step, continue_url):
            if self.needs_phone_channel_selection(step, continue_url):
                continue_url = self.select_phone_otp_channel(step, continue_url)
            try:
                return self._fetch_and_submit_phone_code(continue_url)
            except PhoneNumberRejected as exc:
                raise LoginFlowError(
                    f"手机二次验证失败：{exc.reason}", code="phone_2fa_failed",
                    hint="账号已有手机号但未能完成短信验证。请确认接码号即账号绑定号，或手动输入验证码。",
                    retryable=False,
                )

        # add_phone：需要新绑号，支持换号重试（每账号至多 SMS_MAX_NUMBERS_PER_ACCOUNT 个号）
        # 注意：曾尝试"绑号前重新 authorize 刷新会话"，但实测重新授权会跳回 /log-in（认证态不持久），
        # 反而用未认证的新会话覆盖已认证会话、导致 add-phone/send 必 409，故已移除。
        self.payload["_sms_tried_ids"] = []
        last_reason = ""
        for attempt in range(1, SMS_MAX_NUMBERS_PER_ACCOUNT + 1):
            # 清掉上一轮选号缓存，按已试集合重新选号。
            # 注意：submit/fetch 会把选中的号写进 payload["phone_number"]/["phone_api_url"]，
            # 自动选号模式下必须一并清掉，否则 resolve 会走"payload 已有号"分支拿回同一个号，
            # 导致换号失效（同号重试、排除/停用都不生效）。
            self.payload.pop("_resolved_sms_source", None)
            self.payload.pop("_resolved_sms_phone", None)
            if self.sms_pool_enabled() or self._sms_realtime_provider():
                self.payload.pop("phone_number", None)
                self.payload.pop("phone_api_url", None)
            try:
                next_url = self.submit_phone_number_for_verification(continue_url, phone_hint)
                return self._fetch_and_submit_phone_code(next_url)
            except _NoPhoneAvailable:
                # 明确区分"没开接码"与"号码池无可用号"，否则只看到秒退到 fallback 不知原因
                if (not self.sms_pool_enabled() and not self._sms_realtime_provider()
                        and not phone_pool_entries_from_payload(self.payload)):
                    self.log("phone_pool",
                             "账号需要新绑手机号，但本次任务未开启『遇到手机验证自动接码』开关，无法绑号", "warning")
                else:
                    self.log("phone_pool",
                             "账号需要新绑手机号，但号码池没有可用号（空 / 全部绑满·冷却·过期·停用 / 渠道不匹配）", "warning")
                break
            except PhoneNumberRejected as exc:
                last_reason = exc.reason
                self._handle_rejected_phone(exc)
                continue
        raise LoginFlowError(
            f"手机二次验证失败：已尝试至多 {SMS_MAX_NUMBERS_PER_ACCOUNT} 个手机号仍未通过"
            f"（{last_reason or '号码池无可用号'}），已跳过该账号。",
            code="phone_2fa_failed",
            hint="多个手机号均被 OpenAI 拒绝或无法收码。请在接码管理页补充更优质的号源，或更换出口 IP 地区后重试。",
            retryable=False,
        )

    def _fetch_and_submit_phone_code(self, continue_url: str) -> str:
        """取码并提交。号码层面的失败（没收到码/码无效）抛 PhoneNumberRejected 以触发换号。"""
        code = self.fetch_phone_verification_code(coerce_text(self.payload.get("_detected_phone_hint")))
        if not code:
            raise PhoneNumberRejected("没有收到手机验证码", disable=False)
        self.log("phone_code", "已取到手机验证码，正在提交", "info")
        current_step = self.submit_phone_verification_code(code, continue_url)
        next_url = self.normalize_auth_url(self.extract_continue_url(current_step))
        if not next_url and self.needs_phone_verification(current_step, ""):
            raise PhoneNumberRejected("验证码无效或仍停留在手机验证步骤", disable=False)
        if not next_url:
            raise LoginFlowError(
                f"手机二次验证失败：没有返回继续授权地址。{protocol_compact_error(current_step)}",
                code="phone_2fa_failed",
                hint="手机验证码已提交，但 OpenAI 没有返回可继续的 OAuth 地址；请保留日志用于确认返回结构。",
                retryable=False,
            )
        self.persist_sms_binding()
        return next_url

    def _handle_rejected_phone(self, exc: "PhoneNumberRejected") -> None:
        # Storage is supplied by the owning Go job, never an external manager.
        raise NotImplementedError("local SMS storage adapter required")

    def sms_pool_enabled(self) -> bool:
        return str(first_text(
            self.payload.get("allow_sms"),
            self.payload.get("allowSms"),
        )).lower() in {"1", "true", "yes", "on"}

    def persist_sms_binding(self) -> None:
        # Storage is supplied by the owning Go job, never an external manager.
        raise NotImplementedError("local SMS storage adapter required")

    def _source_from_sms_row(self, row: dict[str, Any], account_email: str) -> dict[str, str]:
        return {
            "id": coerce_text(row.get("id")),
            "mode": "db",
            "provider": coerce_text(row.get("provider")) or "generic",
            "phone": coerce_text(row.get("phone_number")),
            "phone_digits": normalize_phone_digits(row.get("phone_number")),
            "api_url": coerce_text(row.get("api_url")),
            "card_code": coerce_text(row.get("card_code")),
            "account_email": account_email,
        }

    def resolve_db_phone_source(self, account_email: str) -> dict[str, str]:
        # Storage is supplied by the owning Go job, never an external manager.
        raise NotImplementedError("local SMS storage adapter required")

    def _sms_realtime_provider(self) -> str:
        """登录设置指定的实时接码平台 key（如 hero_sms）；空表示不用实时接码。"""
        return coerce_text(self.payload.get("sms_realtime_provider")
                           or self.payload.get("smsRealtimeProvider")).strip().lower()

    def resolve_realtime_phone_source(self, account_email: str) -> dict[str, str]:
        """实时接码平台(hero-sms/nextpro)：按需现取一个号，构造与号池号同形的号源 dict，
        并把平台配置一并带上，供 fetch_code/release 透传。
        卡密制平台(nextpro)：acquire 消耗一张卡，成功后从配置卡池扣减并持久化。"""
        provider_key = self._sms_realtime_provider()
        provider = sp.get_sms_provider(provider_key)
        if not provider or not hasattr(provider, "acquire"):
            self.log("phone_pool", f"实时接码平台 {provider_key} 不可用（未注册/不支持实时申请）", "warning")
            return {}
        workspace_id = normalize_workspace_id(self.payload.get("_workspace_id"))
        cfg = get_sms_platform_config(workspace_id, provider_key)
        if not cfg.get("enabled") or not provider.config_ready(cfg):
            self.log("phone_pool", f"实时接码平台 {provider_key} 未启用或凭证未配置", "warning")
            return {}
        # 换号重试优化(nextpro):同卡 replace 换新号,不消耗新卡;replace 失败(冷却中/单已完结)才退回新卡
        res = None
        prev_token = coerce_text(self.payload.get("_realtime_token")) if hasattr(provider, "replace") else ""
        if prev_token:
            res = provider.replace({**cfg, "card_code": prev_token})
            if res.get("ok"):
                self.log("phone_pool", "同卡换号成功（未消耗新卡）", "info")
            else:
                self.log("phone_pool", f"同卡换号失败（{coerce_text(res.get('message'))[:80]}），改用新卡", "warning")
                res = None
        if res is None and getattr(provider, "card_pool", False):
            # 卡密池取卡(线程安全):持锁重读卡池 → 逐卡兑换 → 从池里扣掉并持久化。
            # 「已使用/无效」=废卡:删出卡池并跳下一张;「处理中」=该卡有活动订单(并发撞卡/
            # 上次任务残留),留在池里跳下一张;其它失败(网络/平台故障)换卡无用,直接停。
            with _SMS_CDK_LOCK:
                cfg = get_sms_platform_config(workspace_id, provider_key)  # 锁内重读最新池
                cdks = list(cfg.get("cdks") or [])
                last_msg = "卡密池已空，请在接码管理页补充 CDK"
                drop: list[str] = []  # 要从池里移除的卡(成功消耗的 + 废卡)
                for cdk in cdks[:5]:
                    # 瞬时故障(CF 5xx/网络/超时):同卡隔 3s 重试至多 2 次;仍失败视为平台故障,停止
                    candidate: dict[str, Any] = {}
                    for t in range(3):
                        candidate = provider.acquire({**cfg, "cdks": [cdk]})
                        last_msg = coerce_text(candidate.get("message")) or ""
                        transient = bool(re.search(r"cloudflare|http 5\d\d|请求失败|超时|timed ?out|网络", last_msg, re.I))
                        if candidate.get("ok") or not transient or t >= 2:
                            break
                        self.log("phone_pool", f"兑换卡 {cdk[:10]}… 遇到瞬时故障（{last_msg[:60]}），3s 后重试（{t+1}/2）", "warning")
                        time.sleep(3)
                    if candidate.get("ok"):
                        res = candidate
                        drop.append(cdk)
                        break
                    last_msg = coerce_text(candidate.get("message")) or last_msg
                    if re.search(r"已使用|无效|失效|不存在", last_msg):
                        drop.append(cdk)
                        self.log("phone_pool", f"卡 {cdk[:10]}… 为废卡（{last_msg[:60]}），已删出卡池，换下一张", "warning")
                        continue
                    self.log("phone_pool", f"兑换卡 {cdk[:10]}… 失败：{last_msg[:80]}", "warning")
                    if "处理中" not in last_msg:
                        break
                if drop:
                    remaining = [c for c in cdks if c not in drop]
                    try:
                        save_sms_platform_config(workspace_id, provider_key, {**cfg, "cdks": remaining}, True)
                    except Exception as exc:
                        self.log("phone_pool", f"卡池扣减持久化失败（不影响本次取号）：{str(exc)[:100]}", "warning")
                if res is None:
                    res = {"ok": False, "message": last_msg}
        if res is None:
            res = provider.acquire(cfg)
        if not res.get("ok"):
            self.log("phone_pool", f"实时申请号码失败：{coerce_text(res.get('message'))[:120]}", "warning")
            return {}
        act_id = coerce_text(res.get("activation_id"))
        if act_id:
            self.payload["_realtime_token"] = act_id
        phone = coerce_text(res.get("phone_number"))
        row = {
            "id": f"{provider_key}:{act_id}",   # 逻辑 id（不入库），用于 tried/日志
            "provider": provider_key,
            "phone_number": phone,
            "card_code": act_id,                # activation_id 借道 card_code 透传
            "activation_id": act_id,
            "api_key": cfg.get("api_key"), "base_url": cfg.get("base_url"),
            "service": cfg.get("service"), "country": cfg.get("country"),
            "realtime": True,
        }
        self.payload["_resolved_sms_phone"] = row
        self.log("phone_pool", f"实时申请到手机号 {phone}（{provider_key}）", "info")
        return {
            "id": row["id"], "mode": "realtime", "provider": provider_key,
            "phone": phone, "phone_digits": normalize_phone_digits(phone),
            "api_url": "", "card_code": act_id, "account_email": account_email,
            "api_key": cfg.get("api_key"), "base_url": cfg.get("base_url"),
            "service": cfg.get("service"), "country": cfg.get("country"),
        }

    def _release_realtime_phone(self, phone: dict[str, Any], ok: bool) -> None:
        """释放实时号：setStatus 完成(ok)/取消(不 ok)。仅对实时号(有 activation_id)有效。
        退卡制平台(chatai):取消即退卡,把卡加回卡池(持锁),换号重试可立即再取。"""
        if not isinstance(phone, dict) or not (phone.get("realtime") or phone.get("activation_id")):
            return
        try:
            provider = sp.get_sms_provider(coerce_text(phone.get("provider")))
            if provider and hasattr(provider, "release"):
                res = provider.release(phone, ok)
                self.log("phone_pool",
                         f"实时号 {coerce_text(phone.get('phone_number'))} 已{'完成' if ok else '取消释放'}", "info")
                if isinstance(res, dict) and res.get("refunded") and res.get("cdk"):
                    provider_key = coerce_text(phone.get("provider"))
                    workspace_id = normalize_workspace_id(self.payload.get("_workspace_id"))
                    with _SMS_CDK_LOCK:
                        cfg = get_sms_platform_config(workspace_id, provider_key)
                        cdks = list(cfg.get("cdks") or [])
                        if res["cdk"] not in cdks:
                            save_sms_platform_config(workspace_id, provider_key,
                                                     {**cfg, "cdks": cdks + [res["cdk"]]}, True)
                    self.log("phone_pool", f"卡 {res['cdk'][:10]}… 已取消退卡，回到卡池", "info")
        except Exception as e:
            self.log("phone_pool", f"释放实时号失败：{str(e)[:120]}", "warning")

    def resolve_phone_code_source(self, phone_hint: str = "", *, allow_batch: bool = False) -> dict[str, str]:
        # 同一任务内锁定同一个号，保证 提交手机号 / 发码 / 取码 / 写绑定 用的是一致来源
        cached = self.payload.get("_resolved_sms_source")
        if cached:
            return cached
        entries = phone_pool_entries_from_payload(self.payload)
        account_email = coerce_text(self.payload.get("email")).lower()
        hint = phone_hint or coerce_text(self.payload.get("_detected_phone_hint"))
        source: dict[str, str] = {}
        by_hint = phone_pool_match_by_hint(entries, hint)
        if by_hint:
            source = by_hint
        if not source:
            bound = [entry for entry in entries if account_email and entry["account_email"] == account_email]
            if len(bound) == 1:
                source = bound[0]
        if not source:
            phone = coerce_text(self.payload.get("phone_number") or self.payload.get("phoneNumber"))
            api_url = coerce_text(self.payload.get("phone_api_url") or self.payload.get("phoneApiUrl"))
            if phone and api_url:
                source = {
                    "id": coerce_text(self.payload.get("phone_binding_id") or self.payload.get("phoneBindingId")),
                    "mode": "payload",
                    "provider": "generic",
                    "phone": phone,
                    "phone_digits": normalize_phone_digits(phone),
                    "api_url": api_url,
                    "card_code": "",
                    "account_email": account_email,
                }
        # 实时接码平台(hero-sms)：登录设置指定了实时平台则只用实时，按需现取号，
        # 且不再回退号码池（用户拍板：选了实时就不走池，失败即失败并报明确原因）。
        if not source and self._sms_realtime_provider():
            source = self.resolve_realtime_phone_source(account_email)
        elif not source and self.sms_pool_enabled():
            # 号码池（DB）兜底：仅在未指定实时平台、且勾选了「允许接码」时启用
            source = self.resolve_db_phone_source(account_email)
        if not source and allow_batch:
            batch = [entry for entry in entries if not entry["account_email"] or entry["mode"] == "batch"]
            if batch:
                source = batch[0]
        # 旧 phone_pool 条目缺省按 generic 处理，统一字段
        if source:
            source.setdefault("provider", "generic")
            source.setdefault("card_code", "")
            self.payload["_resolved_sms_source"] = source
        return source

    def submit_phone_number_for_verification(self, continue_url: str = "", phone_hint: str = "") -> str:
        source = self.resolve_phone_code_source(phone_hint, allow_batch=True)
        phone = coerce_text(source.get("phone"))
        if not phone:
            # 号码池已无可用号 → 让上层换号循环结束（区别于"号码被拒"）
            raise _NoPhoneAvailable()
        self.payload["phone_number"] = phone
        self.payload["phone_api_url"] = coerce_text(source.get("api_url"))
        referer = continue_url or "https://auth.openai.com/add-phone"
        e164 = "+" + normalize_phone_digits(phone)
        self.log("phone_pool", f"提交绑定手机号 {e164}", "info")
        # 发码前先 GET 一次 /add-phone 页：让 OAuth 会话从 email-otp 状态真正"导航"到 add_phone 步骤。
        # 真实浏览器/参考项目都是先加载 add-phone 页、再由前端调 add-phone/send；我们若在 email-otp
        # 通过后不到 1s 就直接 POST send，会话状态可能还没就位 → 被判 invalid_state(409)。
        page_url = self.normalize_auth_url(referer) or "https://auth.openai.com/add-phone"
        try:
            self.request(
                page_url,
                headers=self.headers(page_url, {
                    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                    "Referer": "https://auth.openai.com/email-verification",
                    "Upgrade-Insecure-Requests": "1",
                }),
                timeout=45,
            )
            time.sleep(1.5)  # 给会话状态一点过渡时间，避免紧贴着发码
        except Exception as exc:
            self.log("phone_pool", f"读取 add-phone 页失败，继续发码：{str(exc)[:120]}", "warning")
        # 真实端点取自 add-phone 路由 JS：SPA 实际调用的是 JSON API（与 email-otp 同一套路），
        # 而非 /add-phone 页面表单路由（直接 POST 页面路由会 500）。
        #   POST /api/accounts/add-phone/send  {"phone_number": "+1...", "channel": "sms"}
        url = "https://auth.openai.com/api/accounts/add-phone/send"
        # 对齐参考实现：add-phone/send 不带 sentinel。我们手里的 sentinel 是给 email_verification
        # 流程生成、已被邮箱验证码消费过的旧 token，复用到绑号不规范，故不再附带。
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": referer,
        })
        resp = self.request(
            url,
            method="POST",
            json_data={"phone_number": e164, "channel": "sms"},
            headers=headers,
            timeout=45,
        )
        data = resp.json()
        self.log("phone_pool", f"add-phone/send → HTTP {resp.status}", "info")
        if resp.status in {301, 302, 303, 307, 308} and resp.location():
            return urllib.parse.urljoin(url, resp.location())
        if resp.status in {200, 201, 202}:
            next_url = self.normalize_auth_url(self.extract_continue_url(data))
            return next_url or "https://auth.openai.com/phone-otp"
        msg = protocol_compact_error(data)
        # 分类务必精确：OpenAI 多数 400 的 type 都是 "invalid_request_error"，且 fraud_guard 文案里也含
        # "Please try again later"，因此绝不能用 invalid_request / try again later 这类泛词判定，否则
        # fraud_guard 会被误判成"会话失效"或"限流"，号码永远不会被停用换号。按可靠特征排序：
        # 1) 号码被风控/不可用（fraud_guard / suspicious / 已占用 / 号码非法）→ 停用该号并换下一个
        if re.search(r"fraud_guard|suspicious|in use|already (in use|registered)|not a valid", msg, re.I):
            raise PhoneNumberRejected(f"提交手机号被风控/不可用 HTTP {resp.status}：{msg}", disable=True)
        # 2) 账号/IP 提交过于频繁被限流（只认 "too many"，不用 try again later 泛词）→ 终止，换号无用
        if re.search(r"too many|rate.?limit|频繁", msg, re.I):
            raise LoginFlowError(
                f"手机二次验证失败：提交手机号过于频繁被限流（{msg[:120]}），已停止换号。",
                code="phone_2fa_failed",
                hint="该账号或出口 IP 短时间提交手机号过多，被 OpenAI 限流。请稍后再试或更换出口 IP 地区。",
                status=resp.status or None,
                retryable=False,
            )
        # 3) OAuth 登录会话/状态失效（invalid_state / no longer valid / start over；不含 invalid_request 泛词）：
        # 会话本身废了，不是号码问题。绝不停用/换号；终止本账号并标记可重试（重新登录会建新会话）。
        if re.search(r"no longer valid|invalid[_ ]?state|start over|session.{0,20}(invalid|expired|no longer)", msg, re.I):
            raise LoginFlowError(
                f"手机二次验证中断：OAuth 登录会话在绑号步骤失效（HTTP {resp.status}：{msg[:120]}），未改动号码池，可重试整个登录。",
                code="phone_session_invalid",
                hint="这不是手机号质量问题，而是进入『绑定手机号』步骤时 OAuth 会话/状态失效（invalid_state）。"
                     "常见于会话过期或出口波动；重新发起该账号登录通常即可，已避免误停用号码。",
                status=resp.status or None,
                retryable=True,
            )
        # 4) 其它未知拒绝：保守按"号码问题"停用并换号
        raise PhoneNumberRejected(f"提交手机号被拒 HTTP {resp.status}：{msg}", disable=True)

    def select_phone_otp_channel(self, step: dict[str, Any], continue_url: str = "") -> str:
        referer = self.normalize_auth_url(continue_url) or "https://auth.openai.com/phone-otp/select-channel"
        phone_hint = extract_phone_hint_from_step(step, continue_url)
        source = self.resolve_phone_code_source(phone_hint, allow_batch=True)
        phone = coerce_text(source.get("phone"))
        self.log("phone_code", "进入手机验证码通道，尝试发送短信", "info")
        try:
            page_resp = self.request(
                referer,
                headers=self.headers(referer, {
                    "Accept": "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8",
                    "Referer": "https://auth.openai.com/email-verification",
                }),
                timeout=45,
            )
            if page_resp.status in {301, 302, 303, 307, 308} and page_resp.location():
                return urllib.parse.urljoin(referer, page_resp.location())
            page_data = page_resp.json()
            page_hint = extract_phone_hint_from_step(page_data, page_resp.url or referer)
            if page_hint and not phone_hint:
                phone_hint = page_hint
                self.payload["_detected_phone_hint"] = phone_hint
        except Exception as exc:
            self.log("phone_code", f"读取手机验证页失败，继续尝试发码：{str(exc)[:120]}", "warning")

        attempts = [
            ("https://auth.openai.com/api/accounts/phone-otp/select-channel", {"channel": "sms"}),
            ("https://auth.openai.com/api/accounts/phone-otp/select-channel", {"type": "sms"}),
            ("https://auth.openai.com/api/accounts/phone-otp/send", {"channel": "sms"}),
            ("https://auth.openai.com/api/accounts/phone-otp/resend", {}),
            ("https://auth.openai.com/api/accounts/add-phone/send", {"channel": "sms"}),
            ("https://auth.openai.com/api/accounts/phone-verification/send", {"channel": "sms"}),
        ]
        last_status = 0
        last_data: dict[str, Any] = {}
        for url, body in attempts:
            request_body = dict(body)
            if phone and ("/add-phone/" in url or "/phone-verification/" in url):
                request_body.update({"phone_number": phone, "phoneNumber": phone})
            headers = self.headers(url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": referer,
            })
            if self.sentinel_token:
                headers["openai-sentinel-token"] = self.sentinel_token
            resp = self.request(url, method="POST", json_data=request_body, headers=headers, timeout=45)
            last_status = resp.status
            if resp.status in {301, 302, 303, 307, 308} and resp.location():
                self.log("phone_code", "已请求手机验证码，等待接码", "info")
                return urllib.parse.urljoin(url, resp.location())
            data = resp.json()
            last_data = data
            next_url = self.normalize_auth_url(self.extract_continue_url(data))
            if resp.status == 200:
                self.log("phone_code", "已请求手机验证码，等待接码", "info")
                return next_url or "https://auth.openai.com/phone-otp"
            if resp.status in {400, 401, 403} and re.search(r"invalid|expired|incorrect|验证码", protocol_compact_error(data), re.I):
                continue
        if last_status:
            self.log("phone_code", f"手机短信通道请求未确认：HTTP {last_status}", "warning")
            if last_data:
                self.log("phone_code", protocol_compact_error(last_data), "warning")
        return referer or "https://auth.openai.com/phone-otp"

    def fetch_phone_verification_code(self, phone_hint: str = "", attempts: int = 24, delay: float = 5) -> str:
        source = self.resolve_phone_code_source(phone_hint, allow_batch=False)
        phone = coerce_text(source.get("phone"))
        provider_key = coerce_text(source.get("provider"))
        # 统一走 sms_providers：chongpt 用卡密调 session，generic 用 URL；二者归一
        auto_provider = sp.get_sms_provider(provider_key) if provider_key else None
        has_auto = bool(phone and auto_provider and (source.get("card_code") or source.get("api_url")))
        if has_auto:
            # 自动接码源(实时/卡密平台):90s 收不到码就换号(实测成功投递 60~156s,
            # 30s 会误杀慢投递的号;手动输入通道不受影响,仍走传入的长窗口)
            attempts = min(attempts, 18)
        if phone:
            self.payload["phone_number"] = phone
            if source.get("api_url"):
                self.payload["phone_api_url"] = coerce_text(source.get("api_url"))
            self.log("phone_pool", f"使用手机号尾号 {normalize_phone_digits(phone)[-4:]} 接码", "info")
        phone_row = {
            "provider": provider_key,
            "card_code": coerce_text(source.get("card_code")),
            "api_url": coerce_text(source.get("api_url")),
            # 实时接码平台(hero-sms)透传配置，供 fetch_code 直接调其 API
            "api_key": coerce_text(source.get("api_key")),
            "base_url": coerce_text(source.get("base_url")),
            "service": coerce_text(source.get("service")),
            "country": coerce_text(source.get("country")),
        }
        for attempt in range(1, max(1, attempts) + 1):
            raise_if_login_job_cancelled(self.job_id)
            manual_code = manual_phone_code_for_payload(self.payload)
            if manual_code:
                self.log("manual_phone_code", "使用手动填写的手机验证码", "info")
                return manual_code
            if has_auto:
                try:
                    result = auto_provider.fetch_code(phone_row)
                    if result.get("found") and coerce_text(result.get("code")):
                        self.log("phone_code", "已从接码平台收到验证码", "success")
                        return coerce_text(result.get("code"))
                    if result.get("expired"):
                        self.log("phone_code", "接码号码租期已过期，停止自动取码", "warning")
                        has_auto = False
                    elif attempt == 1 or attempt % 4 == 0:
                        self.log("phone_code", coerce_text(result.get("message")) or "等待手机验证码", "warning")
                except Exception as exc:
                    if attempt == 1 or attempt % 4 == 0:
                        self.log("phone_code", f"手机取码失败：{str(exc)[:180]}", "warning")
            elif attempt == 1:
                self.log("phone_code", "未配置自动接码，等待手动输入手机验证码", "warning")
            time.sleep(max(1, delay))
        return ""

    def submit_phone_verification_code(self, code: str, referer_url: str = "") -> dict[str, Any]:
        referer = referer_url or "https://auth.openai.com/add-phone"
        # 参考实测（抓包实证、已回调成功）：add-phone/send 之后直接 POST phone-otp/validate，
        #   - JSON {"code": code}，无 sentinel、无表单页路由、无导航 GET；
        #   - 成功响应（200/204）直接带 continue_url，跟随它即可继续授权。
        # 注意：/api/accounts/add-phone/validate 会返回 200 但不含 continue_url（不可用）；
        #       直接 POST /phone-verification 页面路由会 500。两者都不走。
        attempts = [
            "https://auth.openai.com/api/accounts/phone-otp/validate",
            "https://auth.openai.com/api/accounts/phone-verification/validate",
            "https://auth.openai.com/api/accounts/sms/validate",
        ]
        last_status = 0
        last_data: dict[str, Any] = {}
        bare_ok: dict[str, Any] | None = None  # 200 但不含 continue_url 的兜底
        for url in attempts:
            short = "/".join(url.rsplit("/", 2)[-2:])
            headers = self.headers(url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": referer,
            })
            resp = self.request(url, method="POST", json_data={"code": code}, headers=headers, timeout=45)
            last_status = resp.status
            if resp.status in {301, 302, 303, 307, 308} and resp.location():
                self.log("phone_code", f"validate {short} → HTTP {resp.status}（302 跳转）", "info")
                return {"continue_url": urllib.parse.urljoin(url, resp.location())}
            data = resp.json()
            last_data = data
            cont = self.extract_continue_url(data)
            self.log("phone_code",
                     f"validate {short} → HTTP {resp.status}"
                     f"{'，已拿到 continue_url' if cont else '，无 continue_url'}", "info")
            if resp.status == 429:
                # phone-otp/validate 是抓包实证的正确端点；429 是"提交验证码被限流"而非端点错误。
                # 继续试后面两个 fallback 端点只会拿到 404，反而用 404 掩盖真相（误报 Invalid URL）。
                # → 直接终止并准确报限流，标记可重试（冷却后再来）。
                raise LoginFlowError(
                    f"手机二次验证失败：提交验证码被限流（HTTP 429：{protocol_compact_error(data)[:120]}）。",
                    code="phone_2fa_rate_limited",
                    hint="短时间内手机验证码提交过多，被 OpenAI 限流。请降低并发、稍后重试，或更换出口 IP 地区。",
                    status=429,
                    retryable=True,
                )
            if resp.status in {200, 204}:
                if cont:
                    return data
                # 200 但没有 continue_url（如 add-phone/validate 之类）：先记下，继续试下一个端点
                bare_ok = bare_ok or data
                continue
            compact = protocol_compact_error(data)
            # 验证码错/过期：换 validate 端点没意义，直接结束（交给上层换号/失败）
            if resp.status in {400, 401, 403} and re.search(r"invalid|expired|incorrect|code|验证码", compact, re.I):
                break
        # 所有端点都没给 continue_url：若曾有 200（验证码本身被接受），回退提交该响应，让上层继续探测
        if bare_ok is not None:
            self.log("phone_code", "验证码已被接受但未返回 continue_url，回退用该响应继续", "warning")
            return bare_ok
        raise LoginFlowError(
            f"手机二次验证失败：HTTP {last_status or '-'} - {protocol_compact_error(last_data)}",
            code="phone_2fa_failed",
            hint="账号要求手机二次验证，但短信验证码提交未通过。请确认收到的是本轮最新验证码。",
            status=last_status or None,
            retryable=False,
        )

    def submit_modern_password(self, password: str) -> dict[str, Any]:
        url = "https://auth.openai.com/api/accounts/password/verify"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": "https://auth.openai.com/log-in/password",
        })
        if self.sentinel_token:
            headers["openai-sentinel-token"] = self.sentinel_token
        resp = self.request(url, method="POST", json_data={"password": password}, headers=headers)
        data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"password verify failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        return data

    def kickoff_modern_otp(self, mode: str = "") -> bool:
        mode_lc = coerce_text(mode).lower()
        payload_mode = coerce_text(self.payload.get("mode") or "login").lower()
        existing = (
            payload_mode != "signup"
            or "passwordless_login" in mode_lc
            or "existing" in mode_lc
        )
        attempts = (
            [
                ("POST", "https://auth.openai.com/api/accounts/email-otp/resend", "https://auth.openai.com/email-verification", None),
                ("GET", "https://auth.openai.com/api/accounts/email-otp/send", "https://auth.openai.com/email-verification", None),
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp", "https://auth.openai.com/email-verification", {}),
            ]
            if existing
            else [
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp", "https://auth.openai.com/create-account/password", {}),
                ("POST", "https://auth.openai.com/api/accounts/email-otp/resend", "https://auth.openai.com/email-verification", None),
                ("GET", "https://auth.openai.com/api/accounts/email-otp/send", "https://auth.openai.com/email-verification", None),
            ]
        )
        for method, url, referer, body in attempts:
            headers = self.headers(url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": referer,
            })
            if self.sentinel_token:
                headers["openai-sentinel-token"] = self.sentinel_token
            try:
                resp = self.request(url, method=method, json_data=body, headers=headers, timeout=30)
                if resp.status == 200:
                    self.log("send_code", f"OpenAI 已返回发送/重发验证码请求：{urllib.parse.urlparse(url).path}", "info")
                    return True
            except Exception:
                continue
        return False

    def submit_modern_code(self, code: str) -> dict[str, Any]:
        url = "https://auth.openai.com/api/accounts/email-otp/validate"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": "https://auth.openai.com/email-verification",
        })
        if self.sentinel_token:
            headers["openai-sentinel-token"] = self.sentinel_token
        resp = self.request(url, method="POST", json_data={"code": code}, headers=headers)
        data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"email code verify failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        next_url = self.normalize_auth_url(self.extract_continue_url(data))
        self.log("verify_code", f"验证码提交响应：page={self.extract_page_type(data) or '-'}，next={self.safe_url_for_log(next_url) if next_url else '-'}", "info")
        return data

    def complete_totp_challenge(self, step: dict[str, Any], secret: str) -> str:
        """mfa_challenge：选 totp 因子 → issue_challenge → 用 2FA 密钥生成动态码 → mfa/verify → 返回 continue_url。"""
        try:
            import pyotp
        except ImportError:
            raise LoginFlowError(
                "服务端缺少 pyotp 依赖，无法生成 TOTP 动态码。",
                code="totp_dep_missing",
                hint="请在服务端执行 pip install pyotp 后重启。",
                retryable=False,
            )
        page = step.get("page") if isinstance(step.get("page"), dict) else {}
        payload = page.get("payload") if isinstance(page.get("payload"), dict) else {}
        factors = payload.get("factors") if isinstance(payload.get("factors"), list) else []
        factor_id = ""
        for factor in factors:
            if (isinstance(factor, dict)
                    and coerce_text(factor.get("factor_type")) == "totp"
                    and not factor.get("is_recovery")):
                factor_id = coerce_text(factor.get("id"))
                break
        if not factor_id:
            factor_id = coerce_text(payload.get("factor_id"))
        if not factor_id:
            raise LoginFlowError(
                "账号进入验证器(TOTP)二次验证，但响应里没有可用的 totp 因子。",
                code="totp_factor_missing",
                hint="账号可能只开了短信/passkey 等其它二次验证，未启用验证器 App。",
                retryable=False,
            )
        self.log("phone_otp", "验证器(TOTP)二次验证：生成并提交动态码", "info")
        self.submit_mfa_issue_challenge(factor_id)
        try:
            code = pyotp.TOTP(secret).now()
        except Exception as exc:
            raise LoginFlowError(
                f"2FA 密钥无法生成动态码：{exc}",
                code="totp_secret_invalid",
                hint="请检查导入的 2FA 密钥是否为完整 base32 字符串。",
                retryable=False,
            )
        data = self.submit_mfa_verify(factor_id, code)
        continue_url = self.normalize_auth_url(self.extract_continue_url(data))
        # 登录后改密扩展流程：authorize 带 post_login_password_reset=true 时,
        # TOTP 通过会落到 reset_password_new_password 页,此时提交随机新密码完成改密,
        # 响应里的 continue_url 即 OAuth callback,流程照常继续。
        if self.payload.get("_change_password"):
            page_type = self.extract_page_type(data)
            if page_type == "reset_password_new_password" or "/reset-password" in (continue_url or ""):
                new_password = generate_random_password()
                self.log("change_password", "进入改密页：提交随机生成的新密码", "info")
                data = self.submit_password_reset(new_password)
                self.changed_password = new_password
                # 立即回写库：改密已生效，若后续步骤瞬时失败导致整轮重试，旧密码已失效，
                # 不在这里落库新密码就会丢失（job 层成功后会再写一次，幂等）。
                try:
                    _update_gpt_sync_fields(
                        coerce_text(self.payload.get("_workspace_id") or "public"),
                        self.payload.get("email"),
                        {"gpt_password": new_password},
                    )
                    self.log("change_password", "新密码已回写源账号库", "success")
                except Exception as exc:
                    self.log("change_password", f"新密码回写库失败（job 层会重试）：{str(exc)[:160]}", "warning")
                continue_url = self.normalize_auth_url(self.extract_continue_url(data))
                self.log("change_password", "密码已修改，继续 OAuth 流程", "success")
            else:
                self.log("change_password",
                         f"勾选了登录后改密，但未进入改密页（page={page_type or '-'}），本次跳过改密",
                         "warning")
        self.raise_if_passkey_or_challenge(data, continue_url)
        self.log("verify_code", f"验证器(TOTP)动态码校验通过，next page={self.extract_page_type(data) or '-'}", "info")
        # TOTP 通过后账号仍可能被要求绑定/验证手机号（接码）：复用邮箱码路径同款手机验证逻辑
        if self.needs_phone_verification(data, continue_url):
            continue_url = self.complete_phone_verification(data, continue_url)
        if not continue_url:
            raise RuntimeError(f"TOTP 验证通过但未返回 continue URL: {protocol_compact_error(data)}")
        return continue_url

    def submit_mfa_issue_challenge(self, factor_id: str) -> None:
        """触发 mfa 挑战（totp 场景服务端无需真正发码，best-effort，不因失败中断登录）。"""
        url = "https://auth.openai.com/api/accounts/mfa/issue_challenge"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": "https://auth.openai.com/log-in/password",
        })
        try:
            self.request(url, method="POST",
                         json_data={"id": factor_id, "type": "totp", "force_fresh_challenge": False},
                         headers=headers, timeout=30)
        except Exception as exc:
            self.log("phone_otp", f"mfa/issue_challenge 未成功（忽略，继续校验）：{exc}", "warning")

    def submit_mfa_verify(self, factor_id: str, code: str) -> dict[str, Any]:
        url = "https://auth.openai.com/api/accounts/mfa/verify"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": f"https://auth.openai.com/mfa-challenge/{factor_id}",
        })
        resp = self.request(url, method="POST",
                            json_data={"id": factor_id, "type": "totp", "code": code},
                            headers=headers)
        data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"totp verify failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        return data

    def submit_password_reset(self, new_password: str) -> dict[str, Any]:
        """改密页提交新密码（抓包确认：POST /api/accounts/password/reset {"password": 新密码}）。"""
        url = "https://auth.openai.com/api/accounts/password/reset"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": "https://auth.openai.com/reset-password/new-password",
        })
        if self.sentinel_token:
            headers["openai-sentinel-token"] = self.sentinel_token
        resp = self.request(url, method="POST", json_data={"password": new_password}, headers=headers)
        data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"password reset failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        return data

    def capture_oauth_callback(self, start_url: str, max_hops: int = 18) -> tuple[str, str]:
        current_url = self.normalize_auth_url(start_url)
        last_url = current_url
        chose_account = False
        for hop in range(max_hops):
            if self.callback_has_code(current_url):
                return current_url, current_url
            try:
                resp = self.request(
                    current_url,
                    headers=self.headers(current_url, {
                        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                        "Referer": last_url if hop else "https://chatgpt.com/",
                        "Upgrade-Insecure-Requests": "1",
                    }),
                    timeout=45,
                )
            except Exception as exc:
                maybe = re.search(r"(https?://(?:localhost|127\.0\.0\.1):1455/auth/callback[^\s'\"<>]+)", str(exc))
                if maybe and self.callback_has_code(maybe.group(1)):
                    return maybe.group(1), maybe.group(1)
                raise
            last_url = resp.url or current_url
            if self.callback_has_code(last_url):
                return last_url, last_url
            if resp.status == 200:
                if self.is_workspace_or_consent_url(current_url):
                    next_url = self.submit_workspace_and_org(current_url)
                    if next_url:
                        if self.callback_has_code(next_url):
                            return next_url, next_url
                        current_url = self.normalize_auth_url(next_url)
                        continue
                if "/choose-an-account" in current_url and not chose_account:
                    chose_account = True
                    next_url = self.choose_account_from_html(resp.text, current_url)
                    if next_url:
                        current_url = self.normalize_auth_url(next_url)
                        continue
            if resp.status not in {301, 302, 303, 307, 308}:
                break
            loc = resp.location()
            if not loc:
                break
            loc = urllib.parse.urljoin(current_url, loc)
            if self.callback_has_code(loc):
                return loc, loc
            current_url = loc
        return "", last_url

    def callback_has_code(self, url: str) -> bool:
        if not url:
            return False
        try:
            parsed = urllib.parse.urlparse(url)
            redirect = urllib.parse.urlparse(self.oauth_redirect_uri)
            if parsed.scheme != redirect.scheme or parsed.hostname != redirect.hostname:
                return False
            if (parsed.port or (443 if parsed.scheme == "https" else 80)) != (redirect.port or (443 if redirect.scheme == "https" else 80)):
                return False
            if parsed.path.rstrip("/") != redirect.path.rstrip("/"):
                return False
            query = urllib.parse.parse_qs(parsed.query)
            return bool(first_text(query.get("code", [""])[0]))
        except Exception:
            return False

    @staticmethod
    def is_workspace_or_consent_url(url: str) -> bool:
        lowered = coerce_text(url).lower()
        return any(part in lowered for part in ["/workspace", "/sign-in-with-chatgpt/", "/consent", "/organization"])

    def submit_workspace_and_org(self, referer_url: str) -> str:
        session_data = self.decode_oauth_session_cookie()
        workspace_id = self.first_workspace_id(session_data)
        if not workspace_id:
            return ""
        url = "https://auth.openai.com/api/accounts/workspace/select"
        headers = self.headers(url, {
            "Accept": "application/json",
            "Origin": "https://auth.openai.com",
            "Referer": referer_url,
        })
        resp = self.request(url, method="POST", json_data={"workspace_id": workspace_id}, headers=headers, timeout=45)
        if resp.status in {301, 302, 303, 307, 308} and resp.location():
            return urllib.parse.urljoin(url, resp.location())
        data = resp.json()
        next_url = self.extract_continue_url(data)
        if next_url:
            return self.normalize_auth_url(next_url)
        orgs = data.get("data", {}).get("orgs", []) if isinstance(data.get("data"), dict) else []
        if not orgs and isinstance(session_data, dict):
            orgs = session_data.get("orgs") if isinstance(session_data.get("orgs"), list) else []
        if not orgs:
            return ""
        org = orgs[0] if isinstance(orgs[0], dict) else {}
        org_id = coerce_text(org.get("id"))
        projects = org.get("projects") if isinstance(org.get("projects"), list) else []
        body = {"org_id": org_id}
        if projects and isinstance(projects[0], dict) and projects[0].get("id"):
            body["project_id"] = projects[0]["id"]
        if not org_id:
            return ""
        org_url = "https://auth.openai.com/api/accounts/organization/select"
        org_resp = self.request(
            org_url,
            method="POST",
            json_data=body,
            headers=self.headers(org_url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": self.normalize_auth_url(next_url) or referer_url,
            }),
            timeout=45,
        )
        if org_resp.status in {301, 302, 303, 307, 308} and org_resp.location():
            return urllib.parse.urljoin(org_url, org_resp.location())
        return self.normalize_auth_url(self.extract_continue_url(org_resp.json()))

    def choose_account_from_html(self, html_text: str, referer_url: str) -> str:
        match = re.search(r"us_[A-Za-z0-9_-]{12,}", html_text or "")
        if not match:
            return ""
        session_id = match.group(0)
        url = "https://auth.openai.com/api/accounts/session/select"
        resp = self.request(
            url,
            method="POST",
            json_data={"session_id": session_id},
            headers=self.headers(url, {
                "Accept": "application/json",
                "Origin": "https://auth.openai.com",
                "Referer": referer_url,
            }),
            timeout=45,
        )
        if resp.status in {301, 302, 303, 307, 308} and resp.location():
            return urllib.parse.urljoin(url, resp.location())
        data = resp.json()
        next_url = self.extract_continue_url(data)
        return self.normalize_auth_url(next_url) if next_url else referer_url

    def decode_oauth_session_cookie(self) -> dict[str, Any]:
        raw_value = self.cookie_value("oai-client-auth-session")
        if not raw_value:
            return {}
        values = [raw_value]
        try:
            decoded = urllib.parse.unquote(raw_value)
            if decoded != raw_value:
                values.append(decoded)
        except Exception:
            pass
        for value in values:
            clean = value.strip().strip("\"'")
            parts = clean.split(".") if "." in clean else [clean]
            for part in parts[:2]:
                try:
                    padded = part + "=" * (-len(part) % 4)
                    data = json.loads(base64.urlsafe_b64decode(padded.encode("ascii")).decode("utf-8", errors="replace"))
                    if isinstance(data, dict):
                        return data
                except Exception:
                    continue
        return {}

    @staticmethod
    def first_workspace_id(data: dict[str, Any]) -> str:
        if not isinstance(data, dict):
            return ""
        direct = first_text(data.get("workspace_id"), data.get("workspaceId"))
        if direct:
            return direct
        workspaces = data.get("workspaces") if isinstance(data.get("workspaces"), list) else []
        for item in workspaces:
            if isinstance(item, dict) and item.get("id"):
                return coerce_text(item.get("id"))
        return ""

    def exchange_oauth_callback(self, callback_url: str) -> dict[str, Any]:
        query = urllib.parse.parse_qs(urllib.parse.urlparse(callback_url).query)
        error = first_text(query.get("error", [""])[0], query.get("error_description", [""])[0])
        if error:
            raise RuntimeError(f"OpenAI OAuth authorization failed: {error}")
        returned_state = first_text(query.get("state", [""])[0])
        expected_state = self.oauth_state or self.oauth_cpa_state
        if expected_state and returned_state and returned_state != expected_state:
            raise RuntimeError("OpenAI OAuth state mismatch")
        code = first_text(query.get("code", [""])[0])
        if not code:
            raise RuntimeError("OAuth callback missing authorization code")
        if not self.oauth_code_verifier:
            raise RuntimeError("OAuth callback captured, but code_verifier is unavailable; CPA callback was submitted but local token exchange cannot run")
        # code→token 加瞬时重试：仅网络失败(status 0)/5xx 重试；4xx(invalid_grant 等)确定性失败不重试
        # （授权码一次性，重试无意义）。降低偶发网络抖动导致整单登录失败。
        status, data, raw = 0, {}, ""
        for attempt in range(1, 4):
            status, data, raw = exchange_openai_oauth_code(code, self.oauth_code_verifier, proxy_url=self.proxy_url)
            if status == 200 and coerce_text(data.get("access_token")):
                break
            if 400 <= status < 500:
                break
            if attempt < 3:
                self.log("token", f"token 交换未成功(HTTP {status})，重试 {attempt + 1}/3", "warning")
                time.sleep(1.5)
        if status != 200:
            compact = protocol_compact_error(data) or raw[:260]
            raise RuntimeError(f"OpenAI OAuth token exchange failed: HTTP {status} - {compact}")
        if not coerce_text(data.get("access_token")):
            raise RuntimeError("OpenAI OAuth token exchange succeeded but returned no access_token")
        if not coerce_text(data.get("refresh_token")):
            raise RuntimeError("OpenAI OAuth token exchange succeeded but returned no refresh_token")
        return merge_session_with_oauth({}, data)

    def follow_callback(self, callback_url: str) -> None:
        current_url = self.normalize_auth_url(callback_url)
        for _ in range(12):
            resp = self.request(
                current_url,
                headers=self.headers(current_url, {
                    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                }),
            )
            if 300 <= resp.status < 400 and resp.location():
                current_url = urllib.parse.urljoin(current_url, resp.location())
                parsed = urllib.parse.urlparse(current_url)
                if parsed.hostname == "chatgpt.com" and not parsed.path.startswith("/api/auth/"):
                    return
                continue
            return

    def get_session(self) -> dict[str, Any]:
        url = "https://chatgpt.com/api/auth/session"
        resp = self.request(url, headers=self.headers(url, {"Referer": "https://chatgpt.com/"}))
        data = resp.json()
        if resp.status != 200:
            raise RuntimeError(f"session request failed: HTTP {resp.status} - {protocol_compact_error(data)}")
        return data

    def get_session_cookie(self) -> str:
        names = [
            "__Secure-next-auth.session-token",
            "__Secure-authjs.session-token",
            "next-auth.session-token",
            "authjs.session-token",
        ]
        for name in names:
            direct = self.cookie_value(name)
            if direct:
                return direct
            chunks: list[tuple[int, str]] = []
            for cookie in self.cookie_jar:
                if cookie.name.startswith(f"{name}."):
                    try:
                        idx = int(cookie.name.rsplit(".", 1)[1])
                    except ValueError:
                        continue
                    chunks.append((idx, cookie.value))
            if chunks:
                return "".join(value for _, value in sorted(chunks))
        return ""

    def cookie_value(self, name: str) -> str:
        for cookie in self.cookie_jar:
            if cookie.name == name:
                return coerce_text(cookie.value)
        return ""

    @staticmethod
    def extract_continue_url(data: dict[str, Any]) -> str:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        payload = page.get("payload") if isinstance(page.get("payload"), dict) else {}
        return first_text(
            data.get("continue_url"),
            data.get("continueUrl"),
            data.get("redirect_url"),
            data.get("redirectUrl"),
            data.get("url"),
            payload.get("continue_url"),
            payload.get("url"),
        )

    @staticmethod
    def extract_page_type(data: dict[str, Any]) -> str:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        return coerce_text(page.get("type") or data.get("page_type"))

    @staticmethod
    def needs_phone_verification(data: dict[str, Any], continue_url: str = "") -> bool:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        payload = page.get("payload") if isinstance(page.get("payload"), dict) else {}
        markers = " ".join([
            coerce_text(page.get("type")),
            coerce_text(data.get("page_type")),
            coerce_text(data.get("step")),
            coerce_text(data.get("state")),
            coerce_text(data.get("error")),
            coerce_text(data.get("message")),
            coerce_text(payload.get("type")),
            coerce_text(payload.get("step")),
            coerce_text(payload.get("state")),
            coerce_text(continue_url),
        ]).lower()
        return any(marker in markers for marker in [
            "phone_verification",
            "phone-verification",
            "phone_otp",
            "phone-otp",
            "phone otp",
            "select-channel",
            "select_channel",
            "add-phone",
            "mfa",
            "sms",
            "phone_number",
            "phone number",
            "mobile",
        ])

    @staticmethod
    def needs_phone_channel_selection(data: dict[str, Any], continue_url: str = "") -> bool:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        markers = " ".join([
            coerce_text(page.get("type")),
            coerce_text(data.get("page_type")),
            coerce_text(data.get("step")),
            coerce_text(data.get("state")),
            coerce_text(continue_url),
        ]).lower()
        return any(marker in markers for marker in [
            "phone_otp_select_channel",
            "phone-otp/select-channel",
            "select-channel",
            "select_channel",
        ])

    @staticmethod
    def needs_add_phone(data: dict[str, Any], continue_url: str = "") -> bool:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        markers = " ".join([
            coerce_text(page.get("type")),
            coerce_text(data.get("page_type")),
            coerce_text(data.get("step")),
            coerce_text(data.get("state")),
            coerce_text(continue_url),
        ]).lower()
        return "add-phone" in markers or "add_phone" in markers

    @staticmethod
    def extract_email_verification_mode(data: dict[str, Any]) -> str:
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        payload = page.get("payload") if isinstance(page.get("payload"), dict) else {}
        return coerce_text(payload.get("email_verification_mode"))

    @staticmethod
    def needs_modern_otp(page_type: str, continue_url: str) -> bool:
        page = page_type.lower()
        url = continue_url.lower()
        return page == "email_otp_verification" or "/email-verification" in url or not continue_url

    @staticmethod
    def normalize_auth_url(value: str) -> str:
        if not value:
            return ""
        return urllib.parse.urljoin("https://auth.openai.com", value)

    @staticmethod
    def extract_query_param(url: str, name: str) -> str:
        try:
            return urllib.parse.parse_qs(urllib.parse.urlparse(url).query).get(name, [""])[0]
        except Exception:
            return ""


class _SentinelTokenGenerator:
    """OpenAI Sentinel 的 requirements_token + PoW token 生成器，
    移植自 aBaiAutoplus（register.py:_SentinelTokenGenerator）。"""

    def __init__(self, device_id: str, user_agent: str):
        self.device_id = device_id or uuid.uuid4().hex
        self.user_agent = user_agent or DEFAULT_HTTP_HEADERS.get("User-Agent", "")
        self.sid = str(uuid.uuid4())

    @staticmethod
    def _fnv1a32(text: str) -> str:
        h = 2166136261
        for ch in text:
            h ^= ord(ch)
            h = (h * 16777619) & 0xFFFFFFFF
        h ^= (h >> 16)
        h = (h * 2246822507) & 0xFFFFFFFF
        h ^= (h >> 13)
        h = (h * 3266489909) & 0xFFFFFFFF
        h ^= (h >> 16)
        return f"{h & 0xFFFFFFFF:08x}"

    @staticmethod
    def _b64(data) -> str:
        return base64.b64encode(json.dumps(data, separators=(",", ":")).encode("utf-8")).decode("ascii")

    def _config(self) -> list:
        perf_now = 1000 + random.random() * 49000
        return [
            "1920x1080",
            time.strftime("%a, %d %b %Y %H:%M:%S GMT+0000 (Coordinated Universal Time)", time.gmtime()),
            4294705152,
            random.random(),
            self.user_agent,
            SENTINEL_SDK_URL,
            None,
            None,
            "en-US",
            "en-US,en",
            random.random(),
            "webkitTemporaryStorage−undefined",
            "location",
            "Object",
            perf_now,
            self.sid,
            "",
            random.choice([4, 8, 12, 16]),
            int(time.time() * 1000 - perf_now),
        ]

    def generate_requirements_token(self) -> str:
        cfg = self._config()
        cfg[3] = 1
        cfg[9] = round(5 + random.random() * 45)
        return "gAAAAAC" + self._b64(cfg)

    def generate_token(self, seed: str, difficulty: str) -> str:
        max_attempts = 500000
        cfg = self._config()
        start_ms = int(time.time() * 1000)
        diff = str(difficulty or "0")
        for nonce in range(max_attempts):
            cfg[3] = nonce
            cfg[9] = round(int(time.time() * 1000) - start_ms)
            encoded = self._b64(cfg)
            digest = self._fnv1a32((seed or "") + encoded)
            if digest <= diff:
                return "gAAAAAB" + encoded
        return "gAAAAAB" + self._b64(cfg)


def _generate_openai_sentinel_token_node(device_id: str, flow: str, proxy_url: str = "") -> str:
    """旧版 Node helper 路径，作为 Python 路径失败时的 fallback。"""
    node_bin = LOGIN_NODE_BIN
    if os.path.sep not in node_bin and (os.path.altsep is None or os.path.altsep not in node_bin):
        node_bin = shutil.which(node_bin) or node_bin
    if not OPENAI_SENTINEL_HELPER.exists():
        return ""
    try:
        env = os.environ.copy()
        if proxy_url:
            env["HTTPS_PROXY"] = proxy_url
            env["HTTP_PROXY"] = proxy_url
            env["ALL_PROXY"] = proxy_url
        completed = subprocess.run(
            [node_bin, str(OPENAI_SENTINEL_HELPER)],
            input=json.dumps({"deviceId": device_id, "flow": flow}, ensure_ascii=False),
            text=True,
            capture_output=True,
            timeout=75,
            check=False,
            env=env,
        )
    except Exception:
        return ""
    if completed.returncode != 0:
        return ""
    try:
        data = json.loads(completed.stdout or "{}")
    except json.JSONDecodeError:
        return ""
    return coerce_text(data.get("token"))


def generate_openai_sentinel_token(
    device_id: str,
    flow: str,
    proxy_url: str = "",
    *,
    impersonate: str = OPENAI_IMPERSONATE,
) -> str:
    _diag_fallback_log(f"sentinel: called flow={flow} did={device_id[:12]} proxy={proxy_url[:60]!r}")
    """生成 openai-sentinel-token 请求头的值。

    新版（aBaiAutoplus 风格）：
    - 调用 sentinel.openai.com/backend-api/sentinel/req 拿 c token + dx challenge
    - 用 _SentinelTokenGenerator 算 requirements/PoW token (p)
    - 用 sentinel_vm.solve_turnstile_dx 解 turnstile dx 拿 t token
    - 返回 JSON 字符串 '{"p":...,"t":...,"c":...,"id":did,"flow":flow}'
      (这是 OpenAI 期望的 header 值格式)

    旧版：Node helper 只返回单个 token 字符串。
    """
    device_id = device_id or uuid.uuid4().hex
    flow = flow or "authorize_continue"

    # Python 路径：完整解 turnstile，信任分高
    try:
        from curl_cffi import requests as cffi_requests  # type: ignore
        ua = DEFAULT_HTTP_HEADERS.get("User-Agent", "")
        generator = _SentinelTokenGenerator(device_id, ua)
        sent_p = generator.generate_requirements_token()
        body = json.dumps({"p": sent_p, "id": device_id, "flow": flow}, separators=(",", ":"))
        resp = cffi_requests.post(
            SENTINEL_REQ_URL,
            headers={
                "Origin": "https://sentinel.openai.com",
                "Referer": SENTINEL_FRAME_URL,
                "Content-Type": "text/plain;charset=UTF-8",
                "User-Agent": ua,
            },
            data=body,
            proxies=_cffi_proxies(proxy_url),
            impersonate=impersonate,
            timeout=30,
        )
        if resp.status_code == 200:
            data = resp.json() if isinstance(resp.json(), dict) else {}
            c_token = coerce_text(data.get("token"))
            turnstile = data.get("turnstile") if isinstance(data.get("turnstile"), dict) else {}
            pow_meta = data.get("proofofwork") if isinstance(data.get("proofofwork"), dict) else {}
            initial_p = sent_p
            if pow_meta.get("required") and pow_meta.get("seed"):
                sent_p = generator.generate_token(
                    str(pow_meta.get("seed") or ""),
                    str(pow_meta.get("difficulty") or "0"),
                )
            t_value = ""
            dx_b64 = coerce_text(turnstile.get("dx"))
            if dx_b64:
                try:
                    from .sentinel_vm import solve_turnstile_dx  # type: ignore
                    t_value = solve_turnstile_dx(
                        dx_b64, initial_p, user_agent=ua, sdk_url=SENTINEL_SDK_URL,
                    )
                except Exception as exc:
                    _diag_fallback_log(f"sentinel: solve_turnstile_dx failed: {exc}")
            payload = {
                "p": sent_p,
                "t": t_value,
                "c": c_token,
                "id": device_id,
                "flow": flow,
            }
            return json.dumps(payload, separators=(",", ":"))
        else:
            _diag_fallback_log(f"sentinel: HTTP {resp.status_code} body={resp.text[:200]}")
    except Exception as exc:
        _diag_fallback_log(f"sentinel: python path failed: {type(exc).__name__}: {exc}")

    # 回退到旧 Node 路径
    token = _generate_openai_sentinel_token_node(device_id, flow, proxy_url=proxy_url)
    return token


def access_token_email(token: str) -> str:
    payload = jwt_payload(token)
    profile = payload.get("https://api.openai.com/profile")
    if isinstance(profile, dict):
        return coerce_text(profile.get("email")).lower()
    return coerce_text(payload.get("email")).lower()


def oauth_base64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii").rstrip("=")


def generate_openai_code_verifier() -> str:
    return secrets.token_hex(64)


def openai_code_challenge(code_verifier: str) -> str:
    return oauth_base64url(hashlib.sha256(code_verifier.encode("ascii")).digest())


def build_openai_oauth_authorize_url(state: str, code_challenge: str) -> str:
    params = urllib.parse.urlencode({
        "response_type": "code",
        "client_id": OPENAI_CODEX_CLIENT_ID,
        "redirect_uri": OPENAI_OAUTH_REDIRECT_URI,
        "scope": OPENAI_OAUTH_SCOPE,
        "state": state,
        "code_challenge": code_challenge,
        "code_challenge_method": "S256",
        "id_token_add_organizations": "true",
        "codex_cli_simplified_flow": "true",
    })
    return f"{OPENAI_OAUTH_AUTHORIZE_URL}?{params}"


def exchange_openai_oauth_code(
    code: str,
    code_verifier: str,
    *,
    proxy_url: str = "",
) -> tuple[int, dict[str, Any], str]:
    """code→token。必须用 curl_cffi（与登录链路同指纹同代理栈）：urllib+PySocks
    会在本地解析域名、把裸 IP 发给住宅网关（iproyal），被 ruleset 拒绝（SOCKS 0x02）。"""
    try:
        from curl_cffi import requests as cffi_requests  # type: ignore
    except Exception as exc:
        msg = f"curl_cffi 未安装：{exc}"
        return 0, {"error": msg}, msg
    try:
        resp = cffi_requests.post(
            OPENAI_OAUTH_TOKEN_URL,
            data={
                "grant_type": "authorization_code",
                "client_id": OPENAI_CODEX_CLIENT_ID,
                "code": code,
                "redirect_uri": OPENAI_OAUTH_REDIRECT_URI,
                "code_verifier": code_verifier,
            },
            headers={
                "Accept": "application/json",
                "Content-Type": "application/x-www-form-urlencoded",
                "User-Agent": CPA_PROBE_USER_AGENT,
            },
            proxies=_cffi_proxies(proxy_url),
            impersonate=OPENAI_IMPERSONATE,
            timeout=60,
        )
    except Exception as exc:
        # 网络失败按 status=0 返回，交给调用方瞬时重试（授权码有效期内重试安全）
        msg = network_error_message(OPENAI_OAUTH_TOKEN_URL, exc)
        return 0, {"error": msg}, msg
    raw = resp.text or ""
    try:
        payload = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        payload = {"raw": raw}
    payload = payload if isinstance(payload, dict) else {"data": payload}
    token_response(int(resp.status_code or 0), payload)
    return int(resp.status_code or 0), payload, raw


def message_six_digit_codes(message: dict[str, Any]) -> list[str]:
    raw_codes = [coerce_text(code) for code in message.get("codes") or []]
    if not raw_codes:
        raw_codes = extract_codes("\n".join([
            coerce_text(message.get("subject")),
            coerce_text(message.get("preview")),
            coerce_text(message.get("body")),
            strip_html(coerce_text(message.get("html_body"))),
        ]))
    return [code for code in raw_codes if re.fullmatch(r"\d{6}", code)]


def count_six_digit_codes(messages: list[dict[str, Any]]) -> int:
    return sum(len(message_six_digit_codes(message)) for message in messages)


def find_latest_code(messages: list[dict[str, Any]], *, after_ts: float = 0, skew_seconds: int = 30, exclude_codes: set[str] | None = None) -> str:
    excluded = exclude_codes or set()
    sorted_messages = sorted(messages, key=message_sort_value, reverse=True)
    for message in sorted_messages:
        received_at = coerce_text(message.get("received_at"))
        if after_ts and received_at:
            parsed = parse_message_datetime(received_at)
            if parsed and parsed.timestamp() + max(0, skew_seconds) < after_ts:
                continue
        for code in message_six_digit_codes(message):
            if code in excluded:
                continue
            return code
    return ""


def directurl_baseline_codes(messages: list[dict[str, Any]]) -> set[str]:
    """接码链接邮件在发码前已有的 6 位码，作为去旧码基线。
    覆盖全部接码适配器(directurl/msgviewer/weimail/thefindnet/aisvip)——
    曾只认 directurl,新适配器的旧码没人排除,登录取到旧码提交 401(生产实测)。"""
    codes: set[str] = set()
    for message in messages:
        if coerce_text(message.get("provider")) in ("directurl", "msgviewer", "weimail", "thefindnet", "aisvip"):
            codes.update(message_six_digit_codes(message))
    return codes


def login_payload_has_directurl(payload: dict[str, Any]) -> bool:
    # 仅 directurl 需要 baseline 去旧码（无时间戳）；mailtoken 有 date 走 since 过滤，不在此列。
    for item in payload.get("generic_accounts", []):
        if not isinstance(item, dict):
            continue
        mode = normalize_generic_mail_mode(item.get("mode") or item.get("provider"))
        if mode == "directurl":
            return True
        if mode == "mailtoken":
            continue
        url = coerce_text(item.get("imap_host") or item.get("base_url")).lower()
        if url.startswith(("http://", "https://")) and "#" not in url:
            return True
    return False


def login_payload_has_pickup_link(payload: dict[str, Any]) -> bool:
    """是否含接码链接类邮箱（mailtoken/directurl）。这类经第三方服务取信，
    有邮件投递延迟 + 约 30s 缓存窗口，取码节奏要放宽（发码后先等、间隔跨缓存周期）。"""
    for item in payload.get("generic_accounts", []):
        if not isinstance(item, dict):
            continue
        if normalize_generic_mail_mode(item.get("mode") or item.get("provider")) in {"mailtoken", "directurl"}:
            return True
        if coerce_text(item.get("imap_host") or item.get("base_url")).lower().startswith(("http://", "https://")):
            return True
    return False


def prime_directurl_baseline(payload: dict[str, Any]) -> set[str]:
    """在触发 OpenAI 发码之前采集接码链接上已有的旧码，作为去旧码基线（directurl 无时间戳）。"""
    if not login_payload_has_directurl(payload):
        return set()
    try:
        data = fetch_transient_client_mail(login_mail_fetch_payload(payload))
        return directurl_baseline_codes(data.get("messages", []) if isinstance(data.get("messages"), list) else [])
    except Exception:
        return set()


def fetch_login_verification_code(payload: dict[str, Any], *, since: float = 0, attempts: int = 12,
                                  delay: float = 5, fallback_to_baseline: bool = False,
                                  initial_wait: float = 0) -> str:
    job_id = coerce_text(payload.get("job_id"))
    total_attempts = max(1, attempts)
    last_summary = ""
    fetch_payload = login_mail_fetch_payload(payload)
    live_messages: list[dict[str, Any]] = []
    # 累积历次轮询见过的所有邮件，供兜底用：接码服务可能某一次返回空（mailtoken 两步 API/网络抖动），
    # 不能只靠"最后一次"的结果，否则曾经取到过的邮件会因最后一次为空而丢失。
    seen_messages: dict[str, dict[str, Any]] = {}
    # directurl 接码链接无邮件时间戳，靠"旧码基线"只接受新码。
    # 优先用发码前预采集的基线（最准，见 run_chatgpt_login_with_protocol 提交邮箱前）；
    # 没有预采集时退回此刻采集（兜底，仍可能与本次新码竞态）。
    exclude_codes: set[str] = set()
    if login_payload_has_directurl(payload):
        pre = payload.get("_directurl_baseline_codes")
        if isinstance(pre, (list, set, tuple)):
            exclude_codes = {coerce_text(code) for code in pre if coerce_text(code)}
        else:
            exclude_codes = prime_directurl_baseline(payload)
            if job_id and exclude_codes:
                append_login_log(job_id, f"接码链接基线已记录 {len(exclude_codes)} 个旧验证码，将只接受新码", "info", "mail_code_baseline")
    # 接码链接邮箱：发码后先等一会再取——给邮件投递留时间，并避免在旧缓存窗口里空取。
    if initial_wait > 0:
        raise_if_login_job_cancelled(job_id)
        if job_id:
            append_login_log(job_id, f"接码链接邮箱：发码后先等待 {int(initial_wait)} 秒再取信（留邮件投递时间、避开旧缓存窗口）", "info", "mail_code_poll")
        time.sleep(initial_wait)
    for attempt in range(1, total_attempts + 1):
        raise_if_login_job_cancelled(job_id)
        manual_code = manual_email_code_for_payload(payload)
        if manual_code:
            if job_id:
                append_login_log(job_id, "使用手动填写的邮箱验证码", "info", "manual_email_code")
            return manual_code
        data = fetch_transient_client_mail(fetch_payload)
        live_messages = data.get("messages", []) if isinstance(data.get("messages"), list) else []
        for _m in live_messages:
            _key = coerce_text(_m.get("mid")) or f"{coerce_text(_m.get('account'))}|{coerce_text(_m.get('received_at'))}|{coerce_text(_m.get('subject'))}"
            seen_messages[_key] = _m
        errors = data.get("errors", []) if isinstance(data.get("errors"), list) else []
        latest = live_messages[0] if live_messages else {}
        latest_subject = coerce_text(latest.get("subject"))[:80] if isinstance(latest, dict) else ""
        latest_at = coerce_text(latest.get("received_at") or latest.get("cached_at"))[:32] if isinstance(latest, dict) else ""
        code_count = count_six_digit_codes(live_messages)
        error_summary = "; ".join(coerce_text(error)[:80] for error in errors[:2])
        last_summary = (
            f"第 {attempt}/{total_attempts} 次，实时取信 {len(live_messages)} 封，"
            f"识别码 {code_count} 个，最新 {latest_subject or '-'}"
            f"{f'（{latest_at}）' if latest_at else ''}"
            f"{f'，错误：{error_summary}' if error_summary else ''}"
        )
        if job_id and (attempt == 1 or attempt == total_attempts or attempt % 4 == 0 or errors):
            append_login_log(job_id, f"查收邮箱：{last_summary}", "info" if live_messages else "warning", "mail_code_poll")
        code = find_latest_code(live_messages, after_ts=since, exclude_codes=exclude_codes)
        if code:
            if job_id:
                append_login_log(job_id, "已从邮箱取到 6 位验证码", "success", "mail_code_poll")
            return code
        time.sleep(max(1, delay))
    # 轮询多次仍未发现"新"验证码：回退提交已见过的最近一封邮件里的验证码。
    # 覆盖两种情况：① OpenAI 短时间重试复用上一个仍有效的旧码（被时间/基线过滤掉）；
    #              ② 接码服务某次返回空导致"最后一次"无邮件——用历次累积的全集兜底，不丢已取到过的邮件。
    if fallback_to_baseline:
        pool = list(seen_messages.values()) or live_messages
        fb_code = find_latest_code(pool, after_ts=0, exclude_codes=None)
        if fb_code:
            if job_id:
                append_login_log(
                    job_id,
                    f"{total_attempts} 次未发现新验证码，回退提交已见过的最近一封邮件里的验证码（可能是仍有效的旧码）",
                    "warning", "mail_code_poll")
            return fb_code
    if job_id and last_summary:
        append_login_log(job_id, f"邮箱验证码查收结束，仍未找到可提交的 6 位验证码：{last_summary}", "warning", "mail_code_missing")
    return ""


def jwt_payload(token: str) -> dict[str, Any]:
    try:
        part = str(token or "").split(".")[1]
        padded = part.replace("-", "+").replace("_", "/")
        padded += "=" * (-len(padded) % 4)
        payload = json.loads(base64.b64decode(padded).decode("utf-8", errors="replace"))
        return payload if isinstance(payload, dict) else {}
    except Exception:
        return {}


def merge_session_with_oauth(session: dict[str, Any], oauth_payload: dict[str, Any]) -> dict[str, Any]:
    merged = dict(session or {})
    merged["access_token"] = coerce_text(oauth_payload.get("access_token")) or first_text(
        session.get("access_token"),
        session.get("accessToken"),
    )
    merged["accessToken"] = merged["access_token"]
    merged["refresh_token"] = coerce_text(oauth_payload.get("refresh_token"))
    merged["refreshToken"] = merged["refresh_token"]
    merged["id_token"] = coerce_text(oauth_payload.get("id_token")) or first_text(
        session.get("id_token"),
        session.get("idToken"),
    )
    if merged["id_token"]:
        merged["idToken"] = merged["id_token"]
    if oauth_payload.get("expires_in"):
        try:
            expires_at = datetime.fromtimestamp(
                time.time() + int(oauth_payload["expires_in"]),
                timezone.utc,
            ).isoformat(timespec="seconds")
            merged["expires_at"] = expires_at
            merged["expires"] = expires_at
        except Exception:
            pass
    merged["oauth_token_type"] = coerce_text(oauth_payload.get("token_type"))
    merged["oauth_scope"] = coerce_text(oauth_payload.get("scope"))
    return merged

_PICKUP_ADAPTERS = [
    ("api798", _api798_match, _fetch_api798_messages),
    ("weimail", lambda url, account: _weimail_api_url(url), _fetch_weimail_messages),
    ("msgviewer", lambda url, account: _msgviewer_api(url), _fetch_msgviewer_messages),
    ("thefindnet", _thefindnet_match, _fetch_thefindnet_messages),
    ("aisvip", _aisvip_match, _fetch_aisvip_messages),
]

def _is_transient_login_error(err_text: str) -> bool:
    """判断登录异常是否为瞬时网络/代理错误（可换 IP 重登），区别于业务失败（封禁/密码错等）。"""
    t = (err_text or "").lower()
    return any(m in t for m in [
        "proxy connect aborted", "curl: (7)", "curl: (16)", "curl: (28)", "curl: (35)",
        "curl: (52)", "curl: (55)", "curl: (56)", "curl: (95)", "curl: (97)",
        "connection reset", "connection aborted", "connection refused", "connection closed",
        "timed out", "timeout", "empty reply", "recv failure", "send failure",
        "network error", "temporarily unavailable", "proxy connect",
    ])
