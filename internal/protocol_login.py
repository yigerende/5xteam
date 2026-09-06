import base64
import html
import json
import re
import sys
import time
import uuid
from datetime import datetime, timezone
from urllib.parse import urlencode, urlparse

from curl_cffi import requests


UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
OTP_RE = re.compile(r"\b\d{6}\b")
OTP_CONTEXT_RE = re.compile(
    r"(?i)(?:code\s*(?:is|:)?|verification\s*code\s*(?:is|:)?|login\s*code\s*(?:is|:)?|"
    r"验证码|驗證碼|安全代码|認証コード|確認コード|Bestätigungscode|Verifizierungscode)\D{0,80}(\d{6})"
)
OTP_REVERSE_CONTEXT_RE = re.compile(
    r"(?i)(\d{6})\D{0,80}(?:code|验证码|驗證碼|安全代码|認証コード|確認コード|Bestätigungscode|Verifizierungscode)"
)
OTP_NOISE = {"000000", "202123", "353740", "ffffff"}


def progress(message):
    # Progress is sent on stderr so stdout remains a single machine-readable
    # JSON result for the Go caller.
    print(f"[protocol] {message}", file=sys.stderr, flush=True)


def fail(message):
    print(json.dumps({"success": False, "error": str(message)[:800]}, ensure_ascii=False))
    raise SystemExit(0)


def check(resp, step):
    if resp.status_code >= 400:
        body = (resp.text or "").strip().replace("\n", " ")
        raise RuntimeError(f"{step} HTTP {resp.status_code}: {body[:500]}")
    return resp


def json_body(resp):
    try:
        return resp.json()
    except Exception:
        text = (resp.text or "").strip()
        if text.startswith("<"):
            raise RuntimeError(f"HTTP {resp.status_code}: 上游返回 Cloudflare/HTML 拒绝页")
        raise RuntimeError(f"HTTP {resp.status_code}: {text[:500]}")


def next_url(value):
    if isinstance(value, dict):
        for key in ("continue_url", "external_url", "url"):
            if value.get(key):
                return str(value[key])
        for child in value.values():
            found = next_url(child)
            if found:
                return found
    elif isinstance(value, list):
        for child in value:
            found = next_url(child)
            if found:
                return found
    return ""


def _clean_mail_text(value):
    text = str(value or "").strip()
    if not text:
        return ""
    text = re.sub(r"(?is)<(script|style).*?</\1>", " ", text)
    text = re.sub(r"(?is)<[^>]+>", " ", text)
    text = html.unescape(text)
    text = re.sub(r"#[0-9a-fA-F]{6}\b", " ", text)
    return re.sub(r"\s+", " ", text).strip()


def _flatten_mail(value, out):
    if isinstance(value, dict):
        for child in value.values():
            _flatten_mail(child, out)
    elif isinstance(value, list):
        for child in value:
            _flatten_mail(child, out)
    elif isinstance(value, str):
        out.append(value)


def _code_candidates(text):
    clean = _clean_mail_text(text)
    if not clean:
        return []
    candidates = []
    for match in OTP_CONTEXT_RE.finditer(clean):
        if match.group(1) not in OTP_NOISE:
            candidates.append(match.group(1))
    for match in OTP_REVERSE_CONTEXT_RE.finditer(clean):
        if match.group(1) not in OTP_NOISE:
            candidates.append(match.group(1))
    if candidates:
        return candidates
    return [code for code in OTP_RE.findall(clean) if code.lower() not in OTP_NOISE]


def _parse_mail_ts(value):
    if value is None:
        return None
    if isinstance(value, (int, float)):
        value = float(value)
        return value / 1000.0 if value > 10_000_000_000 else value
    text = str(value).strip()
    if re.fullmatch(r"\d+(?:\.\d+)?", text):
        value = float(text)
        return value / 1000.0 if value > 10_000_000_000 else value
    try:
        iso = text[:-1] + "+00:00" if text.endswith("Z") else text
        parsed = datetime.fromisoformat(iso)
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed.timestamp()
    except Exception:
        return None


def find_codes(value):
    """Extract the newest useful six-digit OTP from any pickup JSON/text shape."""
    direct = []
    fields = ("code", "verification_code", "verificationCode", "otp", "email_code", "emailCode", "verify_code", "verifyCode")
    if isinstance(value, dict):
        for key in fields:
            raw = value.get(key)
            if raw is not None:
                direct.extend(_code_candidates(raw))
        if direct:
            return direct
        pieces = []
        _flatten_mail(value, pieces)
        candidates = _code_candidates("\n".join(pieces))
    elif isinstance(value, list):
        pieces = []
        _flatten_mail(value, pieces)
        candidates = _code_candidates("\n".join(pieces))
    else:
        candidates = _code_candidates(value)
    return candidates


def find_code(value, exclude=None, after_ts=None):
    excluded = {str(item) for item in (exclude or set())}
    if isinstance(value, dict):
        # Mailbox APIs commonly wrap messages in data.messages/items. Process
        # each message independently and prefer the newest received timestamp;
        # this prevents an old direct `verificationCode` field from winning over
        # a newer message later in the same response.
        nested = value.get("messages") or value.get("items")
        if isinstance(nested, list):
            ordered = sorted(
                (item for item in nested if isinstance(item, dict)),
                key=lambda item: _parse_mail_ts(next((item.get(k) for k in ("received_at", "receivedAt", "created_at", "createdAt", "timestamp", "time", "date") if item.get(k) is not None), None)) or 0,
                reverse=True,
            )
            for item in ordered:
                code = find_code(item, exclude=excluded, after_ts=after_ts)
                if code:
                    return code
        data = value.get("data")
        if isinstance(data, (dict, list)):
            code = find_code(data, exclude=excluded, after_ts=after_ts)
            if code:
                return code
    candidates = find_codes(value)
    if after_ts and isinstance(value, dict):
        timestamp = None
        for key in ("received_at", "receivedAt", "created_at", "createdAt", "timestamp", "time", "date"):
            if key in value:
                timestamp = _parse_mail_ts(value.get(key))
                if timestamp is not None:
                    break
        if timestamp is not None and timestamp + 2 < after_ts:
            return ""
    for code in reversed(candidates):
        if code not in excluded:
            return code
    return ""


def main():
    payload = json.load(sys.stdin)
    email = str(payload.get("email") or "").strip()
    pickup = str(payload.get("pickup_url") or "").strip()
    proxy = str(payload.get("proxy") or "").strip()
    if not email or "@" not in email:
        fail("邮箱地址无效")
    if not pickup:
        fail("邮箱未配置取件链接")

    # turb's current browser config: curl_cffi's Chrome 146 TLS profile with
    # the captured Chrome 149/macOS HTTP and Client-Hints profile.
    s = requests.Session(impersonate="chrome146")
    if proxy:
        s.proxies = {"http": proxy, "https": proxy}
    s.headers.update({
        "User-Agent": UA,
        "Accept-Language": "ja,en-US;q=0.9,en;q=0.8",
        "sec-ch-ua": '"Google Chrome";v="149", "Chromium";v="149", "Not)A;Brand";v="24"',
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": '"macOS"',
        "sec-ch-ua-full-version-list": '"Google Chrome";v="149.0.0.0", "Chromium";v="149.0.0.0", "Not)A;Brand";v="24.0.0.0"',
        "sec-ch-ua-platform-version": '"15.7.0"',
        "sec-ch-ua-arch": '"arm"',
        "sec-ch-ua-bitness": '"64"',
        "sec-ch-ua-model": '""',
    })
    device_id = str(uuid.uuid4())
    for domain in ("chatgpt.com", "auth.openai.com", "sentinel.openai.com"):
        s.cookies.set("oai-did", device_id, domain=domain, path="/")

    def nav(url, referer):
        headers = {
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
            "Referer": referer, "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate",
            "Sec-Fetch-Dest": "document", "Upgrade-Insecure-Requests": "1",
        }
        return check(s.get(url, headers=headers, allow_redirects=True, timeout=45), f"{url}")

    # Match turb's protocol preflight before touching NextAuth. Besides warming
    # Cloudflare cookies, these navigations establish the same cross-site
    # browser context used by the subsequent CSRF/signin requests.
    progress("预热 ChatGPT 登录页")
    nav("https://chatgpt.com/login", "https://chatgpt.com/")
    progress("预热 OpenAI 登录页")
    nav("https://auth.openai.com/log-in", "https://chatgpt.com/login")
    progress("获取 ChatGPT CSRF token")
    csrf = s.get("https://chatgpt.com/api/auth/csrf", headers={
        "Accept": "*/*", "Content-Type": "application/json",
        "Referer": "https://chatgpt.com/", "Origin": "https://chatgpt.com",
        "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
        "Priority": "u=1, i",
    }, timeout=45)
    check(csrf, "GET /api/auth/csrf")
    csrf_token = str((json_body(csrf) or {}).get("csrfToken") or "")
    if not csrf_token:
        raise RuntimeError("未取得 CSRF token")

    query = {
        "prompt": "login", "ext-oai-did": device_id,
        "auth_session_logging_id": str(uuid.uuid4()),
        "ext-passkey-client-capabilities": "11111",
        "screen_hint": "login_or_signup", "login_hint": email,
    }
    signin = s.post("https://chatgpt.com/api/auth/signin/openai?" + urlencode(query), headers={
        "Accept": "*/*", "Content-Type": "application/x-www-form-urlencoded",
        "Referer": "https://chatgpt.com/", "Origin": "https://chatgpt.com",
        "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
    }, data=urlencode({"callbackUrl": "https://chatgpt.com/", "csrfToken": csrf_token, "json": "true"}), timeout=45)
    check(signin, "POST /api/auth/signin/openai")
    progress("提交 OpenAI signin，获取授权地址")
    auth_url = str((json_body(signin) or {}).get("url") or "")
    if not auth_url:
        raise RuntimeError("signin 未返回 authorize URL")

    progress("打开 OpenAI authorize 页面")
    final = nav(auth_url, "https://chatgpt.com/")
    final_url = str(final.url or "")
    if "/create-account/password" in final_url or "/api/accounts/user/register" in final_url:
        raise RuntimeError("登录流程落入注册路径，当前邮箱可能尚未注册")

    poll_url = pickup + ("&" if "?" in pickup else "?") + "json=1"
    otp_headers = {
        "Accept": "application/json", "Content-Type": "application/json",
        "Referer": "https://auth.openai.com/email-verification", "Origin": "https://auth.openai.com",
        "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
    }
    validated_data = None
    last_error = ""
    attempted_codes = set()
    for attempt in range(3):
        code = ""
        attempt_started = time.time()
        progress(f"等待邮箱验证码（第 {attempt + 1}/3 次）")
        for _ in range(40):
            mail = s.get(poll_url, headers={"Accept": "application/json,text/plain,*/*", "Referer": pickup}, timeout=30)
            if mail.status_code < 400:
                payload = json_body(mail)
                code = find_code(payload, exclude=attempted_codes, after_ts=attempt_started)
                if not code:
                    code = find_code(mail.text or "", exclude=attempted_codes)
                if not code:
                    # msg.linlanyu/yangyang style pickup pages expose the
                    # actual message body through /api/messages even when the
                    # shortcut JSON endpoint only returns a 4-digit year in
                    # its top-level `code` field.
                    parsed_pickup = urlparse(pickup)
                    if parsed_pickup.scheme and parsed_pickup.netloc:
                        api_url = f"{parsed_pickup.scheme}://{parsed_pickup.netloc}/api/messages"
                        api_mail = s.get(api_url, params={
                            "email": email,
                            "token": parsed_pickup.path.split("/messages/", 1)[1].split("/", 1)[0] if "/messages/" in parsed_pickup.path else "",
                            "limit": 20,
                        }, headers={"Accept": "application/json", "Referer": pickup}, timeout=30)
                        if api_mail.status_code < 400:
                            code = find_code(json_body(api_mail), exclude=attempted_codes, after_ts=attempt_started)
                if not code:
                    # Some pickup APIs expose a misleading top-level `code`
                    # (often the year) while the real six-digit OTP is only in
                    # the rendered HTML message.
                    plain_url = pickup + ("&" if "?" in pickup else "?") + "_otp_html=1"
                    html_mail = s.get(plain_url, headers={"Accept": "text/html,application/xhtml+xml,text/plain,*/*", "Referer": pickup}, timeout=30)
                    if html_mail.status_code < 400:
                        code = find_code(html_mail.text or "", exclude=attempted_codes)
                if code:
                    break
            time.sleep(3)
        if not code:
            raise RuntimeError("等待邮箱验证码超时")
        attempted_codes.add(code)
        progress("已提取 6 位验证码，提交 email-otp/validate")
        validated = s.post("https://auth.openai.com/api/accounts/email-otp/validate", headers=otp_headers, json={"code": code}, timeout=45)
        if validated.status_code == 200:
            validated_data = json_body(validated)
            progress("邮箱验证码验证成功")
            break
        body = (validated.text or "").lower()
        last_error = f"HTTP {validated.status_code}: {(validated.text or '')[:300]}"
        if "wrong_email_otp_code" not in body and "wrong code" not in body and "expired" not in body:
            check(validated, "POST /api/accounts/email-otp/validate")
        if attempt < 2:
            # Same recovery as turb: explicitly resend, then use a new poll
            # window so a stale message is not submitted again.
            resend = s.get("https://auth.openai.com/api/accounts/email-otp/send", headers={
                "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                "Referer": "https://auth.openai.com/email-verification",
                "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document",
            }, allow_redirects=True, timeout=45)
            if resend.status_code >= 400:
                raise RuntimeError(f"重新发送邮箱验证码失败 HTTP {resend.status_code}")
            progress("验证码无效，已重新发送邮箱验证码")
            time.sleep(2)
    if validated_data is None:
        raise RuntimeError(f"邮箱验证码验证失败: {last_error}")
    continue_url = next_url(validated_data)
    page = validated_data.get("page") if isinstance(validated_data, dict) else {}
    page_type = str((page or {}).get("type") or "") if isinstance(page, dict) else ""
    if not continue_url or "about-you" in continue_url or page_type in ("about_you", "about-you"):
        raise RuntimeError("OTP 已通过，但没有已注册账号的 OAuth 回调地址")

    progress("执行 OAuth callback")
    callback = nav(continue_url, "https://auth.openai.com/email-verification")
    if callback.status_code >= 400:
        raise RuntimeError(f"OAuth callback HTTP {callback.status_code}")
    progress("读取 ChatGPT session 获取 accessToken")
    session_resp = s.get("https://chatgpt.com/api/auth/session", headers={
        "Accept": "*/*", "Referer": "https://chatgpt.com/", "Sec-Fetch-Site": "same-origin",
        "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
    }, timeout=45)
    check(session_resp, "GET /api/auth/session")
    session_data = json_body(session_resp)
    token = str((session_data or {}).get("accessToken") or "").strip()
    if not token:
        raise RuntimeError("/api/auth/session 响应缺少 accessToken")
    progress("accessToken 获取成功")
    print(json.dumps({"success": True, "access_token": token, "session": session_data}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        fail(f"{type(exc).__name__}: {exc}")
