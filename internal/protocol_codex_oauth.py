import base64, json, re, sys, time, html, logging
from datetime import datetime
from curl_cffi import requests
from urllib.parse import urlparse, urljoin, unquote, parse_qs

OTP = re.compile(r"\b\d{6}\b")
CTX = re.compile(r"(?i)(?:code|验证码|認証コード|verification code)\D{0,80}(\d{6})")

def log(msg): print("[protocol] "+str(msg), file=sys.stderr, flush=True)
def clean(v):
    s = html.unescape(str(v or "")); s = re.sub(r"(?is)<[^>]+>", " ", s); return re.sub(r"\s+", " ", s)

PROTOCOL_EVENT_PREFIX = "[protocol-event] "

def safe_url(value):
    """Keep routing evidence while dropping OAuth codes and query values."""
    value = str(value or "").strip()
    if not value:
        return ""
    try:
        parsed = urlparse(value)
        if parsed.scheme and parsed.netloc:
            return f"{parsed.scheme}://{parsed.netloc}{parsed.path or '/'}"
        return parsed.path or value.split("?", 1)[0]
    except Exception:
        return value.split("?", 1)[0][:300]

def cookie_snapshot(session):
    """Return Cookie metadata only. Values are intentionally never emitted."""
    rows = []
    seen = set()
    try:
        for cookie in session.session.cookies.jar:
            row = {
                "name": str(getattr(cookie, "name", "") or ""),
                "domain": str(getattr(cookie, "domain", "") or ""),
                "path": str(getattr(cookie, "path", "") or "/"),
                "secure": bool(getattr(cookie, "secure", False)),
            }
            key = (row["name"], row["domain"], row["path"])
            if row["name"] and key not in seen:
                seen.add(key)
                rows.append(row)
    except Exception:
        pass
    return sorted(rows, key=lambda item: (item["name"], item["domain"], item["path"]))

def response_cookie_names(resp):
    names = set()
    try:
        for cookie in resp.cookies.jar:
            if getattr(cookie, "name", ""):
                names.add(str(cookie.name))
    except Exception:
        pass
    try:
        raw = str(resp.headers.get("set-cookie") or "")
        names.update(re.findall(r"(?:^|,\s*)([!#$%&'*+.^_`|~0-9A-Za-z-]+)=", raw))
    except Exception:
        pass
    return sorted(names)

def response_shape(resp, session=None):
    headers = getattr(resp, "headers", {}) or {}
    result = {
        "http_status": int(getattr(resp, "status_code", 0) or 0),
        "url": safe_url(getattr(resp, "url", "")),
        "location": safe_url(headers.get("location") or headers.get("Location")),
        "set_cookie_names": response_cookie_names(resp),
    }
    try:
        data = resp.json() if getattr(resp, "text", "") else {}
    except Exception:
        data = {}
    if isinstance(data, dict):
        page = data.get("page") if isinstance(data.get("page"), dict) else {}
        result.update({
            "response_keys": sorted(str(key) for key in data.keys()),
            "page_type": str(page.get("type") or data.get("page_type") or ""),
            "continue_url": safe_url(next((data.get(key) for key in ("continue_url", "continueUrl", "redirect_url", "redirectUrl", "url") if data.get(key)), "")),
        })
        auth_session = data.get("oai-client-auth-session")
        result["auth_session_body_type"] = type(auth_session).__name__
        if isinstance(auth_session, dict):
            result["auth_session_body_fields"] = sorted(auth_session.keys())
        error = data.get("error")
        if isinstance(error, dict):
            result["error_code"] = str(error.get("code") or "")
            result["error_type"] = str(error.get("type") or "")
            result["error_message"] = str(error.get("message") or "")[:500]
        elif isinstance(error, str):
            result["error_code"] = error[:120]
            result["error_message"] = str(data.get("error_description") or data.get("message") or "")[:500]
    if session is not None:
        cookies = cookie_snapshot(session)
        result["cookie_jar"] = cookies
        result["auth_session_cookie_present"] = any(item["name"] == "oai-client-auth-session" for item in cookies)
    return result

def proxy_shape(proxy):
    parsed = urlparse(str(proxy or ""))
    return {
        "configured": bool(proxy),
        "scheme": parsed.scheme,
        "endpoint": f"{parsed.hostname or ''}:{parsed.port}" if parsed.port else (parsed.hostname or ""),
    }

def emit_event(stage, event, message="", http_status=0, attempt=0, request=None, response=None, details=None, level="info"):
    payload = {
        "schema_version": 1,
        "stage": str(stage or "oauth"),
        "event": str(event or "trace"),
        "message": str(message or event or "OAuth 协议诊断"),
        "http_status": int(http_status or 0),
        "attempt": int(attempt or 0),
        "level": str(level or "info"),
        "request": request or {},
        "response": response or {},
        "details": details or {},
    }
    print(PROTOCOL_EVENT_PREFIX + json.dumps(payload, ensure_ascii=False, separators=(",", ":")), file=sys.stderr, flush=True)

DEAD_CODES = {"account_deactivated", "account_deleted", "account_banned", "account_disabled", "account_suspended"}
DEAD_MARKERS = (
    "deleted or deactivated", "account has been deleted", "account has been deactivated",
    "account was deleted", "account was deactivated", "account deactivated",
    "account deleted", "account banned", "account disabled", "account suspended",
    "access deactivated", "账号已删除", "账号已停用", "账号已禁用", "账号被封",
    "账户已删除", "账户已停用", "账户已禁用", "账户被封",
)

def dead_code(text):
    low = clean(text).lower()
    for code in DEAD_CODES:
        if code in low:
            return code
    for marker in DEAD_MARKERS:
        if marker in low:
            if "delete" in low or "删除" in low:
                return "account_deleted"
            if "ban" in low or "封" in low:
                return "account_banned"
            return "account_deactivated"
    return ""


def next_auth_url(data):
    if not isinstance(data, dict):
        return ""
    page = data.get("page") if isinstance(data.get("page"), dict) else {}
    payload = page.get("payload") if isinstance(page.get("payload"), dict) else {}
    for source in (data, payload):
        for key in ("continue_url", "continueUrl", "redirect_url", "redirectUrl", "url"):
            if isinstance(source.get(key), str) and source[key]:
                return source[key]
    return ""


class OAuthStepError(RuntimeError):
    def __init__(self, stage, response):
        self.stage = stage
        self.http_status = int(response.status_code)
        self.error_code = dead_code(response.text)
        shape = response_shape(response)
        detail = shape.get("error_message") or shape.get("error_code") or "OpenAI request failed"
        super().__init__(f"{stage} HTTP {self.http_status}: {detail}")


class ManagerConsentFlow:
    """Local port of gpt-manager's ChatGPTProtocolLogin callback methods.

    Keep the authenticated session and PKCE pair across consent and the
    authorize fallback. A missing workspace cookie is not a failed login.
    """

    redirects = {301, 302, 303, 307, 308}

    def __init__(self, session, authorize_url, redirect_uri, state):
        self.session = session
        self.authorize_url = authorize_url
        self.redirect_uri = redirect_uri
        self.state = state

    def normalize(self, value):
        return urljoin("https://auth.openai.com/", value) if value else ""

    def is_callback(self, value):
        if not value:
            return False
        parsed, target = urlparse(value), urlparse(self.redirect_uri)
        def endpoint(url):
            return (url.scheme, url.hostname, url.port or (443 if url.scheme == "https" else 80), url.path.rstrip("/"))
        if endpoint(parsed) != endpoint(target):
            return False
        query = parse_qs(parsed.query)
        if query.get("error"):
            raise RuntimeError("OAuth callback error: " + query["error"][0])
        returned_state = (query.get("state") or [""])[0]
        if self.state and returned_state and returned_state != self.state:
            raise RuntimeError("OpenAI OAuth state mismatch")
        if not query.get("code"):
            raise RuntimeError("OAuth callback missing authorization code")
        return True

    def request(self, stage, url, referer, payload=None, attempt=0):
        method = "POST" if payload is not None else "GET"
        headers = (self.session.get_auth_headers(referer=referer) if payload is not None
                   else self.session.get_auth_navigate_headers(referer=referer))
        request = {"method": method, "url": safe_url(url), "referer": safe_url(referer),
                   "payload_fields": sorted(payload.keys()) if payload is not None else [],
                   "allow_redirects": False, "timeout_seconds": 45}
        try:
            if payload is None:
                response = self.session.get(url, headers=headers, timeout=45, allow_redirects=False)
            else:
                response = self.session.post(url, headers=headers, data=json.dumps(payload), timeout=45, allow_redirects=False)
        except Exception as exc:
            emit_event(stage, "request_error", "OAuth 后续页面请求异常", attempt=attempt,
                       request=request, details={"error_type": type(exc).__name__, "error": str(exc)[:500]}, level="error")
            raise
        emit_event(stage, "request_complete", "OAuth 后续授权请求已返回", http_status=response.status_code,
                   attempt=attempt, request=request, response=response_shape(response, self.session),
                   level="warning" if response.status_code >= 400 else "info")
        return response

    def session_data(self):
        # The running gpt-manager supports URL-encoded and JWT/itsdangerous cookies.
        raw = next((item.value for item in self.session.session.cookies.jar
                    if item.name == "oai-client-auth-session" and item.value), "")
        for value in (raw, unquote(raw)):
            for part in value.strip().strip("\"'").split(".")[:2]:
                try:
                    data = json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))
                    if isinstance(data, dict):
                        return data
                except (ValueError, UnicodeError):
                    continue
        return {}

    def submit_workspace_and_org(self, referer):
        data = self.session_data()
        workspaces = data.get("workspaces") if isinstance(data.get("workspaces"), list) else []
        wid = data.get("workspace_id") or data.get("workspaceId")
        if not wid:
            wid = next((item.get("id") for item in workspaces if isinstance(item, dict) and item.get("id")), "")
        cookies = cookie_snapshot(self.session)
        emit_event("workspace_context", "cookie_snapshot", "授权页面访问后检查工作区会话",
                   response={"cookie_jar": cookies,
                             "auth_session_cookie_present": any(item["name"] == "oai-client-auth-session" for item in cookies),
                             "session_decoded": bool(data), "session_fields": sorted(data.keys()),
                             "workspace_count": len(workspaces), "workspace_id_present": bool(wid)})
        if not wid:
            return ""
        url = "https://auth.openai.com/api/accounts/workspace/select"
        response = self.request("workspace_select", url, referer, {"workspace_id": wid})
        if response.status_code in self.redirects and response.headers.get("location"):
            return urljoin(url, response.headers["location"])
        payload = self.response_json(response)
        # The reference's no-valid-organizations branch resumes the authorize chain.
        error = payload.get("error") if isinstance(payload.get("error"), dict) else {}
        if response.status_code == 400 and error.get("code") == "no_valid_organizations":
            return ""
        if response.status_code >= 400:
            raise OAuthStepError("workspace_select", response)
        next_url = next_auth_url(payload)
        if next_url:
            return self.normalize(next_url)
        nested = payload.get("data") if isinstance(payload.get("data"), dict) else {}
        orgs = nested.get("orgs") or data.get("orgs") or []
        if not isinstance(orgs, list) or not orgs or not isinstance(orgs[0], dict):
            return ""
        org = orgs[0]
        if not org.get("id"):
            return ""
        body = {"org_id": org["id"]}
        projects = org.get("projects")
        if isinstance(projects, list) and projects and isinstance(projects[0], dict) and projects[0].get("id"):
            body["project_id"] = projects[0]["id"]
        org_url = "https://auth.openai.com/api/accounts/organization/select"
        response = self.request("organization_select", org_url, referer, body)
        if response.status_code >= 400:
            raise OAuthStepError("organization_select", response)
        if response.status_code in self.redirects and response.headers.get("location"):
            return urljoin(org_url, response.headers["location"])
        return self.normalize(next_auth_url(self.response_json(response)))

    @staticmethod
    def response_json(response):
        try:
            data = response.json()
            return data if isinstance(data, dict) else {}
        except ValueError:
            return {}

    def choose_account(self, text, referer):
        match = re.search(r"us_[A-Za-z0-9_-]{12,}", text or "")
        if not match:
            return ""
        url = "https://auth.openai.com/api/accounts/session/select"
        response = self.request("session_select", url, referer, {"session_id": match.group(0)})
        if response.status_code >= 400:
            raise OAuthStepError("session_select", response)
        if response.status_code in self.redirects and response.headers.get("location"):
            return urljoin(url, response.headers["location"])
        return self.normalize(next_auth_url(self.response_json(response))) or referer

    def capture(self, start_url, max_hops=18):
        current_url = self.normalize(start_url)
        last_url = current_url
        chose_account = False
        for hop in range(max_hops):
            if self.is_callback(current_url):
                return current_url, current_url
            try:
                response = self.request("consent", current_url, last_url if hop else "https://chatgpt.com/", attempt=hop + 1)
            except Exception as exc:
                match = re.search(r"https?://(?:localhost|127\.0\.0\.1):1455/auth/callback[^\s'\"<>]+", str(exc))
                if match and self.is_callback(match.group(0)):
                    return match.group(0), match.group(0)
                raise
            last_url = str(response.url or current_url)
            if self.is_callback(last_url):
                return last_url, last_url
            if response.status_code >= 400:
                raise OAuthStepError("consent", response)
            if response.status_code == 200:
                if any(part in current_url.lower() for part in ("/workspace", "/sign-in-with-chatgpt/", "/consent", "/organization")):
                    next_url = self.submit_workspace_and_org(current_url)
                    if next_url:
                        current_url = self.normalize(next_url)
                        continue
                if "/choose-an-account" in current_url and not chose_account:
                    chose_account = True
                    next_url = self.choose_account(response.text, current_url)
                    if next_url:
                        current_url = self.normalize(next_url)
                        continue
            if response.status_code not in self.redirects or not response.headers.get("location"):
                break
            current_url = urljoin(current_url, response.headers["location"])
        return "", last_url

    def resolve(self, continue_url):
        callback, last_url = self.capture(continue_url or self.authorize_url)
        if not callback and continue_url:
            emit_event("consent", "authorize_fallback", "后续页面未返回授权码，沿用当前会话重新跟随原 authorize 链",
                       details={"last_url": safe_url(last_url), "same_session": True, "same_pkce": True}, level="warning")
            callback, last_url = self.capture(self.authorize_url)
        if not callback:
            raise RuntimeError(f"OAuth flow did not return callback code; final={safe_url(last_url)}")
        return callback


def code_from(v):
    if isinstance(v, dict):
        for k in ("code","otp","verification_code","verificationCode","email_code","emailCode"):
            if k in v:
                c=code_from(v[k])
                if c:return c
        return code_from(" ".join(str(x) for x in v.values()))
    if isinstance(v,list): return code_from(" ".join(str(x) for x in v))
    s=clean(v)
    m=CTX.search(s)
    return m.group(1) if m else (OTP.search(s).group(0) if OTP.search(s) else "")

def main():
    p=json.load(sys.stdin); email=str(p.get("email") or "").strip(); pickup=str(p.get("pickup_url") or "").strip(); proxy=str(p.get("proxy") or "").strip()
    if not email or not pickup: raise RuntimeError("邮箱或取件链接未配置")
    logging.basicConfig(level=logging.INFO, format="[protocol] %(message)s")
    import config.codex as cfg
    cfg.ENABLE_CODEX_AUTO=True; cfg.CODEX_OAUTH_DRIVER="protocol"; cfg.CODEX_AUTH_URL_SOURCE="local"; cfg.CPA_MANAGEMENT_URL=""; cfg.CPA_MANAGEMENT_KEY=""
    cfg.SMS_PROVIDER=str(p.get("sms_provider") or ""); cfg.SMS_CONFIG=p.get("sms_config") or {}
    from core import codex_oauth
    sess=requests.Session(impersonate="chrome136")
    if proxy: sess.proxies={"http":proxy,"https":proxy}
    def otp_provider(account, after_ts=None):
        deadline=time.time()+180; seen=set()
        while time.time()<deadline:
            try:
                u=pickup+('&' if '?' in pickup else '?')+'json=1'; r=sess.get(u,timeout=25)
                c=code_from(r.json() if 'json' in r.headers.get('content-type','') else r.text)
                if c and c not in seen:return c
                # common mailbox API exposes /api/messages behind /messages/<token>
                if '/messages/' in pickup:
                    base=pickup.split('/messages/',1)[0]; token=pickup.split('/messages/',1)[1].split('/',1)[0]
                    rr=sess.get(base+'/api/messages',params={'email':account,'token':token,'limit':20},timeout=25)
                    c=code_from(rr.text)
                    if c and c not in seen:return c
            except Exception: pass
            time.sleep(3)
        raise RuntimeError("等待邮箱验证码超时")
    log("初始化 gpt-manager 风格 Codex OAuth 协议")
    result = manager_style_oauth(email, proxy, otp_provider, cfg, codex_oauth, str(p.get("gpt_password") or ""))
    if not isinstance(result,dict) or not result.get('success'):
        if isinstance(result, dict) and result.get("status") == "deactivated":
            print(json.dumps({"success": False, "status": "deactivated", "dead": True,
                              "error_code": result.get("error_code") or "account_deactivated",
                              "stage": result.get("stage") or "oauth",
                              "http_status": result.get("http_status") or 0,
                              "error": result.get("error") or "账号已删除或停用"}, ensure_ascii=False))
            return
        raise RuntimeError(str(result.get('error') if isinstance(result,dict) else result))
    out={'success':True,'access_token':result.get('access_token',''),'refresh_token':result.get('refresh_token',''),'account_id':result.get('account_id',''),'result':result}
    print(json.dumps(out,ensure_ascii=False))

def manager_style_oauth(email, proxy, otp_provider, cfg, co, password=""):
    """Shared protocol login with local mailbox, proxy and SMS adapters.

    Continuation follows gpt-manager server.py ChatGPTProtocolLogin:
    capture_oauth_callback, submit_workspace_and_org, and authorize fallback.
    """
    from core.session import BrowserSession
    from urllib.parse import urlparse, parse_qs
    session=BrowserSession(proxy=proxy, fingerprint_seed=f"account:{email.lower()}")
    cv, cc = co._generate_pkce(); state=co._generate_state()
    auth=co._build_authorize_url(state, cc, prompt="login")
    emit_event("session", "created", "OAuth 隔离会话已创建",
               request={"driver":"protocol","proxy":proxy_shape(proxy)},
               response={"cookie_jar":cookie_snapshot(session)})
    try:
        # gpt-manager L1：authorize 直接建登录会话，不做 chatgpt.com 预检。
        bootstrap=co._bootstrap_authorize(session, state, cc, auth_url=auth)
        emit_event("authorize", "bootstrap_complete", "Codex authorize 会话初始化完成",
                   http_status=getattr(bootstrap,"status_code",0),
                   request={"method":"GET","url":safe_url(auth),"query_fields":["client_id","code_challenge","code_challenge_method","redirect_uri","response_type","scope","state"]},
                   response=response_shape(bootstrap,session))
        co.human_delay("api")
        # L2 authorize/continue，保留 sentinel + RUM/Datadog 头。
        email_step_resp = co._submit_email(session, email)
        email_step = co._resp_json(email_step_resp)
        emit_event("authorize_continue", "email_submitted", "登录邮箱已提交",
                   http_status=email_step_resp.status_code,
                   request={"method":"POST","url":"https://auth.openai.com/api/accounts/authorize/continue","payload_fields":["username.kind","username.value"]},
                   response=response_shape(email_step_resp,session))
        page = email_step.get("page") if isinstance(email_step,dict) else {}
        payload = page.get("payload") if isinstance(page,dict) and isinstance(page.get("payload"),dict) else {}
        passwordless_allowed = payload.get("passwordless_disabled") is False
        if passwordless_allowed or not password:
            # Existing-account login follows gpt-manager's modern OTP kickoff:
            # try resend/send first, then passwordless. OpenAI may return 409
            # for passwordless when an OTP challenge is already active.
            otp_attempts = [
                ("POST", "https://auth.openai.com/api/accounts/email-otp/resend", "https://auth.openai.com/email-verification", None),
                ("GET", "https://auth.openai.com/api/accounts/email-otp/send", "https://auth.openai.com/email-verification", None),
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp", "https://auth.openai.com/email-verification", {}),
            ]
            send_ok = False
            last_status = 0
            for method, url, referer, body in otp_attempts:
                send_headers = session.get_auth_headers(referer=referer)
                try:
                    if method == "GET":
                        send = session.get(url, headers=send_headers, allow_redirects=False)
                    else:
                        send = session.post(url, headers=send_headers, data=json.dumps(body) if body is not None else None, allow_redirects=False)
                    last_status = send.status_code
                    emit_event("email_otp_send", "request_complete", "邮箱验证码发送端点已返回",
                               http_status=send.status_code,
                               request={"method":method,"url":safe_url(url),"payload_fields":sorted((body or {}).keys())},
                               response=response_shape(send,session))
                    if send.status_code == 200:
                        log(f"{url.rsplit('/', 1)[-1]} 成功")
                        send_ok = True
                        break
                    code_dead = dead_code(send.text)
                    if code_dead:
                        return {"success": False, "status": "deactivated", "dead": True,
                                "error_code": code_dead, "stage": "email_otp_send",
                                "http_status": send.status_code, "error": (send.text or "")[:500]}
                    log(f"{url.rsplit('/', 1)[-1]} HTTP {send.status_code}，尝试下一个发码端点")
                except Exception as exc:
                    log(f"{url.rsplit('/', 1)[-1]} 请求异常：{str(exc)[:160]}")
            if not send_ok:
                if last_status == 409 and password:
                    passwordless_allowed = False
                else:
                    raise RuntimeError(f"OTP 发码失败，最后 HTTP {last_status}")
        if not passwordless_allowed and password:
            log("passwordless 不可用，按 gpt-manager 回退 password/verify")
            ph=session.get_auth_headers(referer="https://auth.openai.com/log-in/password")
            pr=session.post("https://auth.openai.com/api/accounts/password/verify", headers=ph, data=json.dumps({"password":password}), allow_redirects=False)
            emit_event("password_verify", "request_complete", "密码验证端点已返回",
                       http_status=pr.status_code,
                       request={"method":"POST","url":"https://auth.openai.com/api/accounts/password/verify","payload_fields":["password"]},
                       response=response_shape(pr,session))
            if pr.status_code != 200:
                code_dead = dead_code(pr.text)
                if code_dead:
                    return {"success": False, "status": "deactivated", "dead": True,
                            "error_code": code_dead, "stage": "password_verify",
                            "http_status": pr.status_code, "error": (pr.text or "")[:500]}
                raise RuntimeError(f"password/verify HTTP {pr.status_code}: {(pr.text or '')[:180]}")
            pstep=co._resp_json(pr); ptype=((pstep.get("page") or {}).get("type") if isinstance(pstep,dict) else "") or ""
            if "email" not in ptype and "verification" not in str(pstep.get("continue_url", "")) and "consent" not in ptype:
                raise RuntimeError("password/verify 未返回邮箱验证或 consent 页面")
        # gpt-manager keeps a used-code set and resends when a stale OTP is
        # returned by the mailbox provider. Do the same here.
        h=session.get_auth_headers(referer="https://auth.openai.com/email-verification")
        session._attach_oai_context_headers(h)
        val=None; used=set()
        for attempt in range(1,4):
            code=otp_provider(email, after_ts=time.time())
            if code in used:
                code=otp_provider(email, after_ts=time.time())
            used.add(code)
            log(f"提交 email-otp/validate（第 {attempt}/3 次，不携带 sentinel）")
            val=session.post("https://auth.openai.com/api/accounts/email-otp/validate", headers=h, data=json.dumps({"code":code}), allow_redirects=False)
            emit_event("email_otp_validate", "request_complete", "邮箱验证码验证端点已返回",
                       http_status=val.status_code, attempt=attempt,
                       request={"method":"POST","url":"https://auth.openai.com/api/accounts/email-otp/validate","payload_fields":["code"],"sentinel_attached":False},
                       response=response_shape(val,session),
                       level="info" if val.status_code == 200 else "warning")
            if val.status_code == 200: break
            body=(val.text or "").lower()
            code_dead = dead_code(body)
            if code_dead:
                return {"success": False, "status": "deactivated", "dead": True,
                        "error_code": code_dead, "stage": "email_otp_validate",
                        "http_status": val.status_code, "error": (val.text or "")[:500]}
            if "wrong_email_otp_code" not in body and "wrong code" not in body and "expired" not in body:
                raise RuntimeError(f"email-otp/validate HTTP {val.status_code}: {(val.text or '')[:180]}")
            if attempt < 3:
                resend=session.post("https://auth.openai.com/api/accounts/email-otp/resend", headers=h, data="{}", allow_redirects=False)
                emit_event("email_otp_send", "resend_complete", "邮箱验证码重新发送端点已返回",
                           http_status=resend.status_code, attempt=attempt,
                           request={"method":"POST","url":"https://auth.openai.com/api/accounts/email-otp/resend","payload_fields":[]},
                           response=response_shape(resend,session),
                           level="info" if resend.status_code == 200 else "warning")
                log(f"email-otp/resend HTTP {resend.status_code}，等待新验证码")
                time.sleep(2)
        if val is None or val.status_code != 200: raise RuntimeError(f"email-otp/validate HTTP {val.status_code if val else 0}: {(val.text if val else '')[:180]}")
        step=val.json() if val.text else {}; cont=co._extract_continue_url_from_step(step); page=((step.get("page") or {}).get("type") if isinstance(step,dict) else "") or ""
        log(f"OTP validate → page={page or '-'} continue_url={cont[:100] if cont else '-'}")
        needs_phone=co._needs_phone_verification(step,cont); needs_add_phone=co._needs_add_phone(step,cont)
        current_cookies=cookie_snapshot(session)
        emit_event("post_email_otp", "route_decision", "邮箱验证后的认证分支已解析",
                   response={"page_type":page,"continue_url":safe_url(cont),"response_keys":sorted(step.keys()) if isinstance(step,dict) else [],"cookie_jar":current_cookies,"auth_session_cookie_present":any(item["name"]=="oai-client-auth-session" for item in current_cookies)},
                   details={"needs_phone_verification":needs_phone,"needs_add_phone":needs_add_phone})
        if needs_phone:
            if not needs_add_phone: raise RuntimeError("OpenAI 要求已绑定手机号验证，未发现 add_phone")
            log("检测到 add_phone，进入本项目接码适配")
            emit_event("phone_verification", "required", "OpenAI 明确要求 add_phone，开始接码")
            phone_step=co._do_phone_verification(session)
            cont=next_auth_url(phone_step) or auth
            phone_cookies=cookie_snapshot(session)
            emit_event("phone_verification", "completed", "add_phone 手机验证完成",
                       response={"cookie_jar":phone_cookies,"auth_session_cookie_present":any(item["name"]=="oai-client-auth-session" for item in phone_cookies),"continue_url":safe_url(cont)})
        callback=ManagerConsentFlow(session,auth,cfg.CODEX_REDIRECT_URI,state).resolve(cont)
        code=co._extract_code(callback,state)
        emit_event("oauth_callback", "captured", "OAuth 回调已捕获",
                   response={"url":safe_url(callback),"has_code":bool(code),"state_validated":True})
        # gpt-manager token 交换：独立 session、form-urlencoded、最多 3 次。
        token=None
        for attempt in range(1,4):
            log(f"oauth/token 交换（第 {attempt}/3 次）")
            tx=None
            try:
                tx=requests.Session(impersonate="chrome136")
                if proxy: tx.proxies={"http":proxy,"https":proxy}
                trr=tx.post(cfg.CODEX_TOKEN_URL, headers={"Content-Type":"application/x-www-form-urlencoded","Accept":"application/json"}, data={"grant_type":"authorization_code","code":code,"redirect_uri":cfg.CODEX_REDIRECT_URI,"client_id":cfg.CODEX_CLIENT_ID,"code_verifier":cv}, timeout=60)
                token_shape=response_shape(trr)
                try:
                    token_payload=trr.json() if trr.text else {}
                except Exception:
                    token_payload={}
                if isinstance(token_payload,dict):
                    token_shape["returned_fields"]=sorted(token_payload.keys())
                    token_shape["access_token_present"]=bool(token_payload.get("access_token"))
                    token_shape["refresh_token_present"]=bool(token_payload.get("refresh_token"))
                    token_shape["id_token_present"]=bool(token_payload.get("id_token"))
                emit_event("oauth_token", "exchange_complete", "OAuth Token 交换端点已返回",
                           http_status=trr.status_code, attempt=attempt,
                           request={"method":"POST","url":safe_url(cfg.CODEX_TOKEN_URL),"payload_fields":["grant_type","code","redirect_uri","client_id","code_verifier"],"proxy":proxy_shape(proxy)},
                           response=token_shape,
                           level="info" if trr.status_code==200 else "warning")
                if trr.status_code == 200:
                    tr=trr.json()
                    if tr.get("access_token"): token=tr; break
                code_dead = dead_code(trr.text)
                if code_dead:
                    return {"success": False, "status": "deactivated", "dead": True,
                            "error_code": code_dead, "stage": "oauth_token",
                            "http_status": trr.status_code, "error": (trr.text or "")[:500]}
                log(f"oauth/token HTTP {trr.status_code}")
                if 400 <= trr.status_code < 500:
                    raise OAuthStepError("oauth_token",trr)
            except OAuthStepError:
                raise
            except Exception as exc:
                emit_event("oauth_token", "network_error", "OAuth Token 交换出现网络错误",
                           attempt=attempt, details={"error_type":type(exc).__name__,"error":str(exc)[:500]}, level="error")
                log(f"oauth/token 网络错误：{str(exc)[:160]}")
            finally:
                if tx is not None:
                    tx.close()
            if attempt < 3:
                time.sleep(1.5)
        if not token or not token.get("refresh_token"): raise RuntimeError("oauth/token 未返回完整 AT/RT")
        emit_event("oauth", "completed", "Codex OAuth 协议流程完成",
                   response={"access_token_present":bool(token.get("access_token")),"refresh_token_present":bool(token.get("refresh_token")),"id_token_present":bool(token.get("id_token"))})
        return {'success':True,'access_token':token.get('access_token',''),'refresh_token':token.get('refresh_token',''),'account_id':(co._parse_id_token(token.get('id_token','')) or {}).get('account_id',''),'callback_url':callback}
    except Exception as exc:
        emit_event("oauth", "exception", "Codex OAuth 协议流程异常",
                   details={"error_type":type(exc).__name__,"error":str(exc)[:500],"cookie_jar":cookie_snapshot(session)}, level="error")
        if isinstance(exc,OAuthStepError) and exc.error_code:
            return {"success":False,"status":"deactivated","dead":True,
                    "error_code":exc.error_code,"stage":exc.stage,
                    "http_status":exc.http_status,"error":str(exc)}
        raise
    finally:
        try: session.close()
        except Exception: pass

if __name__=='__main__':
    try: main()
    except Exception as e:
        message=f'{type(e).__name__}: {e}'
        code=dead_code(message)
        if code:
            print(json.dumps({'success':False,'status':'deactivated','dead':True,
                              'error_code':code,'stage':'oauth','http_status':0,
                              'error':message},ensure_ascii=False))
        else:
            print(json.dumps({'success':False,'error':message},ensure_ascii=False))
