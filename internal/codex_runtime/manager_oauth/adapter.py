"""Keep project diagnostics, dead-account handling and SMS persistence local."""
import json
import re
import time
import uuid
from urllib.parse import parse_qs, urlencode, urljoin, urlsplit

from . import bridge, upstream


def cookie_snapshot(flow):
    return sorted([{"name": c.name, "domain": c.domain, "path": c.path, "secure": c.secure}
                   for c in flow.cookie_jar], key=lambda c: (c["name"], c["domain"], c["path"]))


def response_shape(response, flow):
    data = response.json()
    page = data.get("page") if isinstance(data.get("page"), dict) else {}
    error = data.get("error") if isinstance(data.get("error"), dict) else {}
    cookies = cookie_snapshot(flow)
    return {"http_status": response.status, "url": bridge.safe_url(response.url),
            "location": bridge.safe_url(response.location()), "response_keys": sorted(data),
            "page_type": str(page.get("type") or data.get("page_type") or ""),
            "continue_url": bridge.safe_url(flow.extract_continue_url(data)),
            "error_code": str(error.get("code") or ""), "error_type": str(error.get("type") or ""),
            "error_message": bridge.redact(error.get("message") or "")[:500],
            "cookie_jar": cookies,
            "auth_session_cookie_present": any(c["name"] == "oai-client-auth-session" for c in cookies),
            "login_session_cookie_present": any(c["name"] == "login_session" for c in cookies)}


class ProjectProtocolLogin(upstream.ChatGPTProtocolLogin):
    def __init__(self, job_id, payload):
        super().__init__(job_id, payload)
        self.web_session = None
        if payload.get("credential_mode") == "chatgpt_at":
            from curl_cffi import requests as cffi_requests
            self.web_session = cffi_requests.Session(
                impersonate=upstream.OPENAI_IMPERSONATE,
                proxies=upstream._cffi_proxies(self.proxy_url),
            )
        self.last_stage = "oauth_init"
        self.last_status = 0
        self.login_details = {
            "configured_login_mode": payload.get("configured_login_mode", "email_otp"),
            "selected_login_mode": payload.get("selected_login_mode", "email_otp"),
            "login_fallback_reason": payload.get("login_fallback_reason", ""),
            "login_method_reason": payload.get("login_method_reason", ""),
            "login_method": "not_started", "login_method_label": "尚未执行登录",
            "login_auth_status": "not_started",
        }
        bridge.set_login_details(self.login_details)

    def close(self):
        if self.web_session is not None:
            try:
                self.web_session.close()
            except Exception:
                pass

    def set_cookie(self, name, value, domain, path="/"):
        super().set_cookie(name, value, domain, path)
        if not value or self.web_session is None:
            return
        try:
            self.web_session.cookies.set(name, value, domain=domain, path=path)
        except Exception:
            pass

    def _sync_web_session_cookies(self):
        if self.web_session is None:
            return
        try:
            for cookie in self.web_session.cookies.jar:
                upstream.ChatGPTProtocolLogin.set_cookie(
                    self,
                    cookie.name,
                    cookie.value or "",
                    upstream.coerce_text(cookie.domain) or "chatgpt.com",
                    upstream.coerce_text(cookie.path) or "/",
                )
        except Exception:
            pass

    def is_chatgpt_at(self):
        return self.payload.get("credential_mode") == "chatgpt_at"

    def login(self):
        if not self.is_chatgpt_at():
            return super().login()

        email_addr = upstream.coerce_text(self.payload.get("email"))
        password = upstream.coerce_text(self.payload.get("password"))
        force_email_code = str(upstream.first_text(
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

        self.log("oauth_init", "后端协议：开始 ChatGPT 临时 AT 登录")
        self.auth_url = self.prepare_oauth_authorize_url()
        self.log("authorize", "后端协议：打开 ChatGPT 登录入口并建立 login_session")
        login_state = self.bootstrap_oauth_session(self.auth_url)
        if not login_state.get("ok"):
            raw_error = login_state.get("error") or "ChatGPT 登录入口没有建立 auth.openai.com 登录会话。"
            raw_hint = login_state.get("hint") or "协议链路没有拿到 auth.openai.com 的 login_session；请检查代理出口后重试。"
            raise upstream.LoginFlowError(
                raw_error,
                code="oauth_session_missing",
                hint=raw_hint,
                status=login_state.get("status") if isinstance(login_state.get("status"), int) else None,
                retryable=True,
            )

        issued_after = time.time()
        self.log("sentinel", "Protocol login: generate Sentinel token")
        self.sentinel_token = upstream.generate_openai_sentinel_token(
            self.device_id, "authorize_continue", self.proxy_url)
        if not self.sentinel_token:
            self.log("sentinel", "Sentinel token helper returned empty token; continuing once", "warning")

        if upstream.login_payload_has_directurl(self.payload):
            baseline = upstream.prime_directurl_baseline(self.payload)
            self.payload["_directurl_baseline_codes"] = sorted(baseline)
            self.log("mail_code_baseline", f"接码链接基线已记录 {len(baseline)} 个旧验证码（已在发码前采集，只接受新码）")

        self.log("identifier", "Protocol login: submit email")
        step = self.authorize_continue(email_addr)
        continue_url = self.complete_modern_login(step, password, issued_after)
        session = self._finish_chatgpt_login(continue_url)
        email_from_token = upstream.access_token_email(session.get("access_token", ""))
        session["email"] = email_from_token or email_addr
        session["user"] = {
            **(session.get("user") if isinstance(session.get("user"), dict) else {}),
            "email": session["email"],
        }
        if self.changed_password:
            session["changed_password"] = self.changed_password
        self.log("success", "Protocol login succeeded", "success")
        return session

    def prepare_oauth_authorize_url(self):
        if not self.is_chatgpt_at():
            return super().prepare_oauth_authorize_url()
        self.oauth_authorize_source = "chatgpt_web"
        self.log("oauth_init", "后端协议：生成 ChatGPT Web 登录会话")
        self.device_id = self.device_id or uuid.uuid4().hex
        try:
            self.request(
                "https://chatgpt.com/",
                headers=self.headers("https://chatgpt.com/", {
                    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                    "Referer": "https://chatgpt.com/",
                    "Upgrade-Insecure-Requests": "1",
                }),
                timeout=20,
                allow_redirects=True,
            )
        except Exception:
            pass
        csrf_token = self.get_csrf_token()
        query = urlencode({
            "prompt": "login",
            "ext-oai-did": self.device_id,
            "auth_session_logging_id": str(uuid.uuid4()),
            "ext-passkey-client-capabilities": "0111",
            "screen_hint": "login_or_signup",
            "login_hint": str(self.payload.get("email") or "").strip(),
        })
        signin_url = "https://chatgpt.com/api/auth/signin/openai?" + query
        response = self.request(
            signin_url,
            method="POST",
            form_data={"callbackUrl": "https://chatgpt.com/", "csrfToken": csrf_token, "json": "true"},
            headers=self.headers(signin_url, {
                "Accept": "*/*",
                "Content-Type": "application/x-www-form-urlencoded",
                "Origin": "https://chatgpt.com",
                "Referer": "https://chatgpt.com/",
            }),
            timeout=45,
        )
        data = response.json()
        authorize_url = upstream.first_text(data.get("url"), response.location())
        if response.status >= 400 or not authorize_url:
            summary = upstream.protocol_compact_error(data)
            raise upstream.LoginFlowError(
                f"ChatGPT signin 未返回授权地址：HTTP {response.status} - {summary or 'empty response'}",
                code="chatgpt_signin_failed",
                hint="请检查全局代理出口是否允许访问 ChatGPT 登录接口后重试。",
                status=response.status,
                retryable=True,
            )
        authorize_url = urljoin(signin_url, authorize_url)
        self.remember_oauth_params_from_authorize_url(authorize_url)
        authorize_response = self.request(
            authorize_url,
            headers=self.headers(authorize_url, {
                "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                "Referer": "https://chatgpt.com/",
                "Upgrade-Insecure-Requests": "1",
            }),
            timeout=45,
            allow_redirects=True,
        )
        if authorize_response.status >= 400:
            raise upstream.LoginFlowError(
                f"打开 ChatGPT 授权页面失败：HTTP {authorize_response.status}",
                code="chatgpt_authorize_failed",
                hint="请检查全局代理出口后重试。",
                status=authorize_response.status,
                retryable=True,
            )
        self.login_url = authorize_response.url
        return authorize_url

    def bootstrap_oauth_session(self, authorize_url):
        if self.is_chatgpt_at() and self.has_auth_session_cookie():
            return {"ok": True, "final_url": self.login_url or authorize_url}
        return super().bootstrap_oauth_session(authorize_url)

    def exchange_oauth_callback(self, callback_url):
        if not self.is_chatgpt_at():
            return super().exchange_oauth_callback(callback_url)
        query = parse_qs(urlsplit(callback_url).query)
        returned_state = upstream.first_text(query.get("state", [""])[0])
        if self.oauth_state and returned_state and returned_state != self.oauth_state:
            raise RuntimeError("OpenAI OAuth state mismatch")
        self.follow_callback(callback_url)
        return self._chatgpt_web_session()

    def _chatgpt_web_session(self):
        session = {}
        access_token = ""
        for attempt in range(1, 7):
            session = self.get_session()
            access_token = upstream.first_text(session.get("accessToken"), session.get("access_token"))
            if access_token:
                break
            if attempt < 6:
                self.log("session", f"ChatGPT Session 暂无 AT，等待后重读（{attempt}/6）", "warning")
                time.sleep(1.5 * attempt)
        if not access_token:
            raise upstream.LoginFlowError(
                "ChatGPT Web Session 连续 6 次未返回 accessToken",
                code="chatgpt_session_missing",
                hint="登录验证已通过，但 ChatGPT Session 尚未建立；请更换代理出口后重试。",
                retryable=True,
            )
        session["access_token"] = access_token
        session["accessToken"] = access_token
        session_token = self.get_session_cookie()
        if session_token:
            session["session_token"] = session_token
        return session

    def _finish_chatgpt_login(self, continue_url):
        if continue_url:
            self.log("session", "ChatGPT 登录验证通过，完成 Web Session 跳转")
            self.follow_callback(continue_url)
        self.log("session", "直接读取 ChatGPT Web Session 并提取 AT")
        return self._chatgpt_web_session()

    def resolve_realtime_phone_source(self, account_email):
        if not self.payload.get("hero_managed") or self._sms_realtime_provider() != "hero_sms":
            return super().resolve_realtime_phone_source(account_email)
        previous = self.payload.get("_hero_activation")
        try:
            result = bridge.call("hero_replace", id=previous["activation_id"]) if previous else bridge.call("hero_acquire")
        except Exception as exc:
            raise upstream.LoginFlowError(
                "Hero 申请或换号失败，已停止继续申请：" + str(exc),
                code="sms_provider_failed", retryable=False,
            ) from exc
        act_id = str(result["id"])
        phone = "+" + upstream.normalize_phone_digits(result["phone"])
        row = {"id": "hero_sms:" + act_id, "activation_id": act_id, "card_code": act_id,
               "phone_number": phone, "provider": "hero_sms", "realtime": True}
        self.payload["_hero_activation"] = row
        self.payload["_resolved_sms_phone"] = row
        self.log("phone_pool", ("Hero 更换号码成功" if previous else "Hero 申请号码成功") + "，激活 ID " + act_id)
        return {"id": row["id"], "mode": "realtime", "provider": "hero_sms", "phone": phone,
                "phone_digits": upstream.normalize_phone_digits(phone), "card_code": act_id,
                "api_url": "", "account_email": account_email}

    def fetch_phone_verification_code(self, phone_hint="", attempts=24, delay=5):
        if not self.payload.get("hero_managed") or not self.payload.get("_hero_activation"):
            return super().fetch_phone_verification_code(phone_hint, attempts, delay)
        act_id = self.payload["_hero_activation"]["activation_id"]
        deadline = time.monotonic() + 90
        for attempt in range(min(attempts, 18)):
            if time.monotonic() >= deadline:
                break
            manual_code = upstream.manual_phone_code_for_payload(self.payload)
            if manual_code:
                bridge.protect_code(manual_code)
                self.log("manual_phone_code", "使用手动填写的手机验证码")
                return manual_code
            result = bridge.call("hero_fetch", id=act_id, timeout_seconds=max(0.01, min(10, deadline - time.monotonic())))
            code = str(result.get("code") or "").strip()
            if result.get("found") and re.fullmatch(r"\d{4,8}", code):
                bridge.protect_code(code)
                self.log("phone_code", "Hero 已收到短信验证码", "success")
                return code
            remaining = max(0, deadline - time.monotonic())
            self.log("phone_code", f"Hero 激活 {act_id} 等待短信，第 {attempt + 1} 次，剩余 {int(remaining)} 秒")
            time.sleep(min(max(1, delay), remaining))
        self.log("phone_code", "Hero 单号等待短信已超时", "warning")
        return ""

    def _release_realtime_phone(self, phone, ok):
        if self.payload.get("hero_managed") and phone.get("provider") == "hero_sms":
            bridge.call("hero_release", id=phone["activation_id"], finish=ok)
            self.payload.pop("_hero_activation", None)
            self.log("phone_pool", "Hero 已提交后台" + ("完成" if ok else "取消") + "任务，等待平台确认")
            return
        return super()._release_realtime_phone(phone, ok)

    def set_login_method(self, method, label):
        self.login_details.update(login_method=method, login_method_label=label, login_auth_status="running")
        bridge.set_login_details(self.login_details)

    def complete_modern_login(self, step, password, issued_after):
        # Observe authentication only; the reference owns the entire protocol.
        try:
            result = super().complete_modern_login(step, password, issued_after)
        except BaseException:
            self.login_details["login_auth_status"] = "failed"
            bridge.set_login_details(self.login_details)
            raise
        if self.login_details["login_method"] == "not_started":
            self.set_login_method("existing_session", "已有认证会话（无需验证码）")
        elif self.login_details["login_method"] == "password":
            self.login_details["login_method_label"] = "ChatGPT 密码登录（本次未触发 2FA）"
        self.login_details["login_auth_status"] = "succeeded"
        bridge.set_login_details(self.login_details)
        self.log("login_method", "登录验证通过：" + self.login_details["login_method_label"])
        return result

    def submit_modern_password(self, password):
        self.set_login_method("password", "ChatGPT 密码登录")
        self.log("password", "正在提交 ChatGPT 密码")
        return super().submit_modern_password(password)

    def complete_totp_challenge(self, step, secret):
        method, label = {
            "email_otp": ("email_otp_totp", "邮箱验证码 + OpenAI TOTP 登录"),
            "password_email_otp": ("password_email_otp_totp", "ChatGPT 密码 + 邮箱验证码 + OpenAI TOTP 登录"),
            "password": ("password_totp", "ChatGPT 密码 + OpenAI TOTP 登录"),
        }.get(self.login_details["login_method"], ("totp", "OpenAI TOTP 验证"))
        self.set_login_method(method, label)
        self.log("totp", "OpenAI 要求 TOTP，开始验证器验证")
        return super().complete_totp_challenge(step, secret)

    def submit_mfa_verify(self, factor_id, code):
        bridge.protect_code(code)
        self.log("totp", "正在提交 OpenAI TOTP 动态验证码")
        return super().submit_mfa_verify(factor_id, code)

    def submit_modern_code(self, code):
        bridge.protect_code(code)
        return super().submit_modern_code(code)

    def log(self, step, message, level="info"):
        self.last_stage = step
        if step in {"waiting_code", "send_code"}:
            if self.login_details["login_method"] in {"password", "password_email_otp"}:
                self.set_login_method("password_email_otp", "ChatGPT 密码 + 邮箱验证码登录")
            else:
                self.set_login_method("email_otp", "邮箱验证码登录")
        super().log(step, message, level)

    def request(self, url, **kwargs):
        stage = urlsplit(url).path.strip("/").replace("/", "_") or "authorize"
        self.last_stage, self.last_status = stage, 0
        headers = kwargs.get("headers") or {}
        payload = kwargs.get("json_data") or kwargs.get("form_data") or {}
        request = {"method": kwargs.get("method", "GET"), "url": bridge.safe_url(url),
                   "referer": bridge.safe_url(headers.get("Referer")),
                   "payload_fields": sorted(payload), "header_names": sorted(headers),
                   "sentinel_attached": bool(headers.get("openai-sentinel-token")),
                   "impersonate": upstream.OPENAI_IMPERSONATE,
                   "allow_redirects": bool(kwargs.get("allow_redirects", False)),
                   "timeout_seconds": kwargs.get("timeout", 60)}
        bridge.emit(stage, "request_start", "OAuth 请求开始", request=request)
        try:
            if self.is_chatgpt_at():
                response = self._web_session_request(url, **kwargs)
            else:
                kwargs.pop("allow_redirects", None)
                response = super().request(url, **kwargs)
        except Exception as exc:
            bridge.emit(stage, "request_error", "OAuth 请求异常", request=request,
                        details={"error": bridge.redact(exc)[:500], "error_type": type(exc).__name__}, level="error")
            raise
        self.last_status = response.status
        bridge.emit(stage, "request_complete", "OAuth 请求已返回", http_status=response.status,
                    request=request, response=response_shape(response, self),
                    level="warning" if response.status >= 400 else "info")
        code = bridge.dead_code(response.text) if response.status >= 400 else ""
        if code:
            raise bridge.DeadAccountError(code, stage, response.status, upstream.protocol_compact_error(response.json()))
        return response

    def _web_session_request(self, url, **kwargs):
        if self.web_session is None:
            raise RuntimeError("ChatGPT Web Session 未初始化")
        method = kwargs.get("method", "GET")
        json_data = kwargs.get("json_data")
        form_data = kwargs.get("form_data")
        final_headers = dict(kwargs.get("headers") or {})
        body = None
        if json_data is not None:
            body = json.dumps(json_data, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            final_headers.setdefault("Content-Type", "application/json")
        elif form_data is not None:
            body = urlencode(form_data).encode("utf-8")
            final_headers.setdefault("Content-Type", "application/x-www-form-urlencoded")
        for header_name in (
            "User-Agent", "sec-ch-ua", "sec-ch-ua-full-version-list", "sec-ch-ua-mobile",
            "sec-ch-ua-platform", "sec-ch-ua-arch", "sec-ch-ua-bitness", "sec-ch-ua-model",
            "sec-ch-ua-platform-version",
        ):
            final_headers.pop(header_name, None)
        last_error = ""
        attempts = 3 if self.proxy_url else 2
        for attempt in range(attempts):
            try:
                response = self.web_session.request(
                    method,
                    url,
                    headers=final_headers,
                    data=body,
                    timeout=kwargs.get("timeout", 60),
                    allow_redirects=bool(kwargs.get("allow_redirects", False)),
                    default_headers=True,
                )
                self._sync_web_session_cookies()
                return upstream.ProtocolResponse(
                    int(response.status_code),
                    str(response.url or url),
                    response.headers,
                    response.text,
                )
            except Exception as exc:
                last_error = str(exc)
                if attempt + 1 < attempts:
                    time.sleep(0.6 + attempt * 0.7)
                    continue
                raise RuntimeError(f"network error: {upstream.network_error_message(url, exc)}") from exc
        raise RuntimeError(last_error or "请求失败")

    def resolve_db_phone_source(self, account_email):
        row = bridge.call("sms_pick", provider=str(self.payload.get("sms_provider") or ""),
                          exclude_ids=self.payload.get("_sms_tried_ids") or [])
        if not row:
            self.log("phone_pool", "号码池没有可用手机号（绑满、冷却、过期、停用或正在使用）", "warning")
            return {}
        self.payload["_resolved_sms_phone"] = row
        self.log("phone_pool", "从本项目号码池选取手机号 " + str(row.get("phone_number") or ""))
        return self._source_from_sms_row(row, account_email)

    def _handle_rejected_phone(self, exc):
        phone = self.payload.get("_resolved_sms_phone") or {}
        if self.payload.get("hero_managed") and "invalid_auth_step" in str(exc.reason).lower():
            raise upstream.LoginFlowError(
                "手机验证的授权步骤已失效，停止换号并回收当前激活：" + str(exc.reason),
                code="phone_session_invalid", retryable=True,
            ) from exc
        phone_id = str(phone.get("id") or "")
        tried = self.payload.setdefault("_sms_tried_ids", [])
        if phone_id and phone_id not in tried:
            tried.append(phone_id)
        if phone.get("realtime") or phone.get("activation_id"):
            if not (self.payload.get("hero_managed") and phone.get("provider") == "hero_sms"):
                self._release_realtime_phone(phone, ok=False)
        elif phone_id:
            bridge.call("sms_rejected", id=phone_id, disable=exc.disable, reason=exc.reason)
        self.log("phone_pool", "当前号码未通过，换号重试：" + str(exc.reason), "warning")

    def persist_sms_binding(self):
        phone = self.payload.get("_resolved_sms_phone")
        if not isinstance(phone, dict):
            return
        if phone.get("realtime") or phone.get("activation_id"):
            self._release_realtime_phone(phone, ok=True)
            return
        try:
            bridge.call("sms_bind", id=phone.get("id"))
            self.log("phone_pool", "已保存本项目手机号与账号的绑定", "success")
        except Exception as exc:
            self.log("phone_pool", "写绑定关系失败：" + str(exc), "warning")


def run(payload, rpc=None):
    # Allowlisted input prevents an injected manager URL from enabling remote OAuth.
    email = str(payload.get("email") or "").strip()
    pickup = str(payload.get("pickup_url") or "").strip()
    provider = str(payload.get("sms_provider") or "").strip()
    mode = "password_totp" if payload.get("login_mode") == "password_totp" else "email_otp"
    credential_mode = "chatgpt_at" if payload.get("credential_mode") == "chatgpt_at" else "codex_rt"
    local = {"email": email, "password": str(payload.get("gpt_password") or "") if mode == "password_totp" else "",
             "_totp_secret": str(payload.get("totp_secret") or ""),
             "force_email_code": mode == "email_otp", "email_code_login": mode == "email_otp",
             "configured_login_mode": payload.get("configured_login_mode") or mode,
             "selected_login_mode": mode,
             "login_fallback_reason": payload.get("login_fallback_reason") or "",
             "login_method_reason": payload.get("login_method_reason") or "",
             "proxy": payload.get("proxy"),
             "job_id": "local", "allow_sms": bool(payload.get("allow_sms", True)) and credential_mode != "chatgpt_at",
             "skip_phone_verification": credential_mode == "chatgpt_at",
             "sms_config": payload.get("sms_config") or {}, "credential_mode": credential_mode,
             "sms_realtime_provider": provider if provider in {"hero_sms", "nextpro", "congou", "chatai"} else "",
             "sms_provider": provider if provider in {"generic", "chongpt", "chong10666"} else "",
             "hero_managed": rpc is not None,
             "generic_accounts": [{"email": email, "pickup_url": pickup, "imap_host": pickup,
                                   "mode": "mailtoken" if urlsplit(pickup).fragment else "directurl"}] if pickup else []}
    bridge.configure({**payload, **local}, rpc)
    flow = None
    try:
        flow = ProjectProtocolLogin("local", local)
        bridge.emit("login_method", "login_start", "开始本次 ChatGPT 登录" if credential_mode == "chatgpt_at" else "开始本次 OAuth 登录")
        session = flow.login()
        token = upstream.jwt_payload(session.get("id_token") or session.get("access_token") or "")
        auth = token.get("https://api.openai.com/auth") or {}
        return {**session, "success": True, "account_id": auth.get("chatgpt_account_id", ""), **flow.login_details}
    except bridge.DeadAccountError as exc:
        bridge.emit(exc.stage, "dead_account", "OpenAI 返回明确死号状态", http_status=exc.status,
                    details={"error_code": exc.code}, level="error")
        return {"success": False, "dead": True, "status": "deactivated", "retryable": False,
                "error_code": exc.code, "stage": exc.stage, "http_status": exc.status, "error": bridge.redact(exc),
                **(flow.login_details if flow else {})}
    except Exception as exc:
        typed = isinstance(exc, upstream.LoginFlowError)
        retryable = bool(exc.retryable) if typed else upstream._is_transient_login_error(str(exc))
        stage = flow.last_stage if flow else "oauth_init"
        status = (exc.status or 0) if typed else (flow.last_status if flow else 0)
        bridge.emit(stage, "exception", "Codex OAuth 失败", http_status=status,
                    details={"error": bridge.redact(exc)[:800], "error_type": type(exc).__name__,
                             "retryable": retryable, "cookie_jar": cookie_snapshot(flow) if flow else []}, level="error")
        return {"success": False, "error": bridge.redact(exc), "retryable": retryable,
                "error_code": exc.code if typed else "login_failed", "stage": stage, "http_status": status,
                "hint": bridge.redact(exc.hint) if typed else "", **(flow.login_details if flow else {})}
    finally:
        if flow is not None:
            flow.close()
