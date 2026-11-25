"""SIWC authorization adapter; all account authentication stays in the shared login engine.

Authorization/token parameters follow sub2api's openai_siwc_flow.go. The consent
transition follows the SIWC HAR, not Codex's workspace-selection endpoint.
"""
import hmac
import secrets
import time
from urllib.parse import parse_qs, urlencode, urljoin, urlsplit

from . import bridge, upstream
from .adapter import ProjectProtocolLogin

ISSUER = "https://auth.openai.com"
AUTHORIZE = ISSUER + "/api/accounts/authorize"
TOKEN = ISSUER + "/api/accounts/oauth/token"
RESOURCE = "https://api.openai.com/v1"
SCOPES = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
REDIRECT = "http://127.0.0.1:1456/auth/callback"
MANIFEST_SYNC = ("https", "chatgpt.com", "/api/auth/session-manifest-sync")


class SIWCProtocolLogin(ProjectProtocolLogin):
    def web_request_attempt_limit(self):
        return 1 if self.payload.get("pro_oauth_retry_managed") else super().web_request_attempt_limit()

    @classmethod
    def continue_authenticated(cls, flow, web_session=None):
        # A protocol adapter over the existing live client, never a new login.
        adapter = object.__new__(cls)
        adapter.__dict__ = flow.__dict__
        adapter.siwc_web_session = web_session or {}
        for attempt in range(2):
            adapter.auth_url = adapter.prepare_oauth_authorize_url()
            callback, _ = adapter.capture_oauth_callback(adapter.auth_url)
            if not callback:
                raise RuntimeError("原登录会话未返回 SIWC 授权码")
            result = adapter.exchange_oauth_callback(callback)
            if result.get("reauthorize") is not True:
                return result
            if adapter.payload.get("pro_oauth_retry_managed"):
                raise upstream.LoginFlowError("下游确认未创建账号，需要重新取得 SIWC 授权", code="siwc_reauthorize_required")
            if adapter.payload.get("siwc_authorization_source") != "sub2api" or attempt:
                raise RuntimeError("SIWC 重新授权一次后仍失败")
            adapter.log("siwc_recovery", "下游确认未创建账号，沿用当前登录会话重新获取授权链接并授权一次")
        raise RuntimeError("SIWC 授权恢复未完成")

    def request(self, url, **kwargs):
        parsed = urlsplit(url)
        if (getattr(self, "oauth_authorize_source", "") == "siwc"
                and (parsed.scheme, parsed.netloc) == ("https", "auth.openai.com")
                and parsed.path in {"/api/accounts/session/select", "/api/accounts/client_auth_session_dump",
                                    "/api/accounts/dynamic-agent-clients/consent/grant"}):
            # Each SIWC action gets a fresh authorize_continue challenge. The
            # password/email token belongs to the earlier login transaction.
            # Keep the live auth client, its cookies, device and sticky proxy.
            self.sentinel_token = ""
            started = time.monotonic()
            try:
                token = upstream.generate_openai_sentinel_token(
                    self.device_id, "authorize_continue", self.proxy_url)
            except Exception:
                token = ""
            if not isinstance(token, str) or not token.strip():
                bridge.emit("siwc_sentinel", "refresh_failed", "SIWC 授权校验令牌生成失败",
                            details={"endpoint": parsed.path, "flow": "authorize_continue"}, level="warning",
                            duration_ms=int((time.monotonic() - started) * 1000))
                raise upstream.LoginFlowError("SIWC 授权校验令牌生成失败，请重试",
                                             code="siwc_sentinel_failed", retryable=True)
            self.sentinel_token = token
            bridge.protect_code(token)
            kwargs["headers"] = {**(kwargs.get("headers") or {}), "openai-sentinel-token": token}
            bridge.emit("siwc_sentinel", "refreshed", "SIWC 授权校验令牌已刷新",
                        details={"endpoint": parsed.path, "flow": "authorize_continue"},
                        duration_ms=int((time.monotonic() - started) * 1000))
        response = super().request(url, **kwargs)
        if (getattr(self, "oauth_authorize_source", "") == "siwc"
                and (parsed.scheme, parsed.netloc) == ("https", "auth.openai.com")
                and parsed.path in {"/api/accounts/session/select", "/api/accounts/password/verify",
                                    "/api/accounts/mfa/verify"}):
            try:
                data = response.json()
            except (ValueError, TypeError):
                data = {}
            if isinstance(data, dict):
                error = data.get("error")
                if isinstance(error, dict) and error.get("code") in {
                        "session_expired", "invalid_session", "session_not_found", "login_required"}:
                    raise upstream.LoginFlowError("SIWC 身份验证会话已失效，需要恢复已付款授权",
                                                 code="siwc_session_expired", status=response.status)
                if response.status == 200:
                    # Keep the MFA factors and page type, not only continue_url.
                    self._siwc_next_step = data
        return response

    def siwc_identity_next(self, step, fallback=""):
        """Advance only the challenge actually returned by the current transaction."""
        step = step if isinstance(step, dict) else {}
        if step.get("error") or step.get("success") is False:
            raise upstream.LoginFlowError("SIWC 身份验证未通过", code="siwc_identity_failed")
        page = self.extract_page_type(step)
        target = self.extract_continue_url(step)
        if not target and page == "sign_in_with_chatgpt_consent":
            target = ISSUER + "/sign-in-with-chatgpt/consent"
        target = target or fallback
        path = urlsplit(urljoin(ISSUER, target)).path if target else ""
        self.raise_if_passkey_or_challenge(step, target)
        if page in {"login", "login_identifier"} or path in {"/log-in", "/log-in/", "/login"}:
            raise upstream.LoginFlowError("SIWC 原会话要求重新登录，需要恢复已付款授权",
                                         code="siwc_session_expired")
        kind = "password" if page == "login_password" or path == "/log-in/password" else (
            "totp" if page == "mfa_challenge" or path.startswith("/mfa-challenge") else "")
        if not kind:
            if target:
                parsed = urlsplit(target)
                if self._siwc_verified and (parsed.scheme, parsed.netloc, parsed.path) in {
                        ("https", "chatgpt.com", "/"), ("https", "chatgpt.com", "/api/auth/callback/openai")}:
                    return self.siwc_revisit_authorize()
                return target
            if page == "sign_in_with_chatgpt_consent":
                return ISSUER + "/sign-in-with-chatgpt/consent"
            if not page and self._siwc_verified:
                return self.siwc_revisit_authorize()
            return ""
        if kind in self._siwc_challenges:
            raise upstream.LoginFlowError("SIWC 身份验证后仍重复要求验证，需要恢复已付款授权",
                                         code="siwc_session_expired")
        self._siwc_challenges.add(kind)
        self.log("siwc_identity", "正在补充身份验证：" + ("提交密码" if kind == "password" else "提交 2FA"))
        self._siwc_next_step = None
        if kind == "password":
            password = str(self.payload.get("password") or self.payload.get("gpt_password") or "")
            if not password:
                raise upstream.LoginFlowError("SIWC 要求密码验证，但账号未配置密码", code="siwc_password_missing")
            self.sentinel_token = upstream.generate_openai_sentinel_token(self.device_id, "password_verify", self.proxy_url)
            if not self.sentinel_token:
                raise upstream.LoginFlowError("SIWC 密码验证校验令牌生成失败", code="siwc_sentinel_failed")
            bridge.protect_code(self.sentinel_token)
            # The shared login mixin deliberately permits one password write
            # per login. SIWC is a later authorization transaction; its own
            # challenge guard above permits one explicitly requested write.
            result = upstream.ChatGPTProtocolLogin.submit_modern_password(self, password)
            self._siwc_verified = True
            return self.siwc_identity_next(result)
        secret = str(self.payload.get("_totp_secret") or "")
        if not secret:
            raise upstream.LoginFlowError("SIWC 要求 2FA，但账号未配置 2FA 密钥", code="totp_secret_missing")
        # Shared factor selection, TOTP generation and verification; the response
        # is retained by request() so a further page is never silently discarded.
        next_url = self.complete_totp_challenge(step, secret)
        self._siwc_verified = True
        return self.siwc_identity_next(self._siwc_next_step, next_url)

    def submit_mfa_verify(self, factor_id, code):
        data = super().submit_mfa_verify(factor_id, code)
        if data.get("error"):
            raise upstream.LoginFlowError("SIWC 2FA 验证未通过", code="siwc_identity_failed")
        if not self.extract_continue_url(data) and self.extract_page_type(data) == "sign_in_with_chatgpt_consent":
            data = {**data, "continue_url": ISSUER + "/sign-in-with-chatgpt/consent"}
            self._siwc_next_step = data
        if not self.extract_continue_url(data) and not self.extract_page_type(data) and not data.get("error"):
            # A successful verification can leave navigation to the caller.
            data = {**data, "continue_url": self.siwc_revisit_authorize()}
            self._siwc_next_step = data
        return data

    def siwc_revisit_authorize(self):
        if self._siwc_revisited:
            raise upstream.LoginFlowError("SIWC 验证后无法恢复授权会话", code="siwc_session_expired")
        self._siwc_revisited = True
        self.log("siwc_identity", "身份验证已通过，沿用原授权链接继续授权")
        return self.auth_url

    def prepare_oauth_authorize_url(self):
        self.oauth_authorize_source = "siwc"
        if self.payload.get("siwc_authorization_source") == "sub2api":
            result = bridge.call("siwc_generate")
            url = str(result.get("auth_url") or "")
            parsed = urlsplit(url)
            query = parse_qs(parsed.query, keep_blank_values=True)
            if (parsed.scheme, parsed.netloc, parsed.path) != ("https", "auth.openai.com", "/api/accounts/authorize") or parsed.fragment:
                raise RuntimeError("s2接口返回的 SIWC 授权地址无效")
            for key, expected in {"redirect_uri": REDIRECT, "client_id": "dynamic_agent_client", "response_type": "code", "resource": RESOURCE, "code_challenge_method": "S256", "ext_agent_host_id": self.payload["siwc_host_id"]}.items():
                if query.get(key) != [expected]:
                    raise RuntimeError("s2接口 SIWC 授权参数不匹配：" + key)
            for key in ("state", "nonce", "code_challenge"):
                if len(query.get(key, [])) != 1 or not query[key][0]:
                    raise RuntimeError("s2接口 SIWC 授权参数缺失")
                bridge.protect_code(query[key][0])
            if set(query.get("scope", [""])[0].split()) != set(SCOPES.split()):
                raise RuntimeError("s2接口 SIWC 授权范围不匹配")
            self.oauth_state = query["state"][0]
            self.siwc_nonce = query["nonce"][0]
            self.siwc_host_id = self.payload["siwc_host_id"]
            self.remember_oauth_params_from_authorize_url(url)
            return url
        self.oauth_state = secrets.token_urlsafe(32)
        self.oauth_code_verifier = upstream.generate_openai_code_verifier()
        self.siwc_nonce = secrets.token_urlsafe(32)
        self.siwc_host_id = self.payload["siwc_host_id"]
        for value in (self.oauth_state, self.oauth_code_verifier, self.siwc_nonce):
            bridge.protect_code(value)
        url = AUTHORIZE + "?" + urlencode({
            "client_id": "dynamic_agent_client", "response_type": "code",
            "redirect_uri": REDIRECT, "scope": SCOPES, "resource": RESOURCE,
            "code_challenge": upstream.openai_code_challenge(self.oauth_code_verifier),
            "code_challenge_method": "S256", "state": self.oauth_state,
            "nonce": self.siwc_nonce, "ext_agent_host_id": self.siwc_host_id,
            "agent_name_hint": "sub2api",
        })
        self.remember_oauth_params_from_authorize_url(url)
        return url

    def oauth2_auth_url_from_authorize(self, authorize_url):
        # Never fall back to Codex's authorize endpoint for a SIWC session.
        return authorize_url

    def capture_oauth_callback(self, start_url, max_hops=18):
        self._siwc_next_step = None
        self._siwc_challenges = set()
        self._siwc_verified = False
        self._siwc_revisited = False
        current = self.normalize_auth_url(start_url)
        previous = self.auth_url
        chose_account = False
        synced_urls = set()
        for _ in range(max_hops):
            parsed = urlsplit(current)
            if parsed.fragment:
                raise RuntimeError("SIWC 授权跳转地址无效")
            if (parsed.scheme, parsed.netloc, parsed.path) == ("http", "127.0.0.1:1456", "/auth/callback"):
                if parse_qs(parsed.query).get("error") == ["login_required"]:
                    raise upstream.LoginFlowError("SIWC 要求重新登录", code="siwc_session_expired")
                # Capture even an error callback without connecting to loopback.
                return current, current
            manifest_sync = (parsed.scheme, parsed.netloc, parsed.path) == MANIFEST_SYNC
            if not manifest_sync and (parsed.scheme, parsed.netloc) != ("https", "auth.openai.com"):
                raise RuntimeError("SIWC 授权跳转地址无效")
            if parsed.path == "/api/accounts/authorize" and current != self.auth_url:
                raise RuntimeError("SIWC 续验证不能替换原授权参数")
            if manifest_sync:
                if current in synced_urls or len(synced_urls) >= 3:
                    raise RuntimeError("SIWC 会话同步中转循环或次数超限")
                synced_urls.add(current)
                # Some grants sync the ChatGPT session before issuing the code.
                # Use the same live client, cookies, device and sticky proxy.
                bridge.emit("siwc_manifest_sync", "start", "沿用登录客户端完成 ChatGPT 会话同步中转")
            response = self.request(current, headers=self.headers(current, {
                "Accept": "text/html,application/xhtml+xml,application/json" if manifest_sync else "text/html,application/xhtml+xml",
                "Referer": previous,
            }), timeout=45)
            next_url = response.location() if response.status in {301, 302, 303, 307, 308} else ""
            if response.status == 200:
                if manifest_sync:
                    try:
                        data = response.json()
                    except (ValueError, TypeError):
                        data = {}
                    next_url = self.extract_continue_url(data) if isinstance(data, dict) else ""
                elif parsed.path == "/choose-an-account" and not chose_account:
                    chose_account = True
                    self._siwc_next_step = None
                    next_url = self.choose_account_from_html(response.text, current)
                    next_url = self.siwc_identity_next(self._siwc_next_step, next_url)
                elif parsed.path in {"/log-in", "/log-in/", "/login", "/log-in/password"} or parsed.path.startswith("/mfa-challenge"):
                    try:
                        page_data = response.json()
                    except (ValueError, TypeError):
                        page_data = None
                    next_url = self.siwc_identity_next(page_data if isinstance(page_data, dict) and self.extract_page_type(page_data) else self._siwc_next_step, current)
                elif parsed.path == "/sign-in-with-chatgpt/consent":
                    next_url = self.grant_siwc_consent(current)
                elif self.is_workspace_or_consent_url(current):
                    next_url = super().submit_workspace_and_org(current)
            if not next_url:
                raise upstream.LoginFlowError(
                    f"SIWC 授权未返回下一步：HTTP {response.status} ({parsed.path})",
                    code="siwc_consent_failed", status=response.status,
                    retryable=response.status == 429 or response.status >= 500,
                )
            if manifest_sync:
                bridge.emit("siwc_manifest_sync", "complete", "ChatGPT 会话同步已返回下一跳",
                            http_status=response.status,
                            details={"next_url": bridge.safe_url(urljoin(current, next_url))})
            previous, current = current, urljoin(current, next_url)
            if current == self.auth_url and self._siwc_verified:
                # A verified session may need account selection once more.
                chose_account = False
        raise RuntimeError("SIWC 授权跳转次数超限")

    def grant_siwc_consent(self, referer):
        self.log("siwc_consent", "读取 SIWC 授权会话并确认授权")
        headers = {"Accept": "application/json", "Origin": ISSUER, "Referer": referer}
        url = ISSUER + "/api/accounts/client_auth_session_dump"
        response = self.request(url, headers=self.headers(url, headers), timeout=45)
        if response.status != 200:
            raise upstream.LoginFlowError(f"读取 SIWC 授权会话失败：HTTP {response.status}",
                                         status=response.status, retryable=response.status >= 500)
        data = response.json()
        session = data.get("client_auth_session") or {}
        session_id = data.get("session_id") or session.get("session_id")
        if not isinstance(session_id, str) or not session_id:
            raise RuntimeError("SIWC 授权会话缺少 session_id")
        if session.get("email") and session["email"].strip().lower() != self.payload["email"].strip().lower():
            raise RuntimeError("SIWC 授权会话账号与当前邮件账号不一致")
        bridge.protect_code(session_id)
        url = ISSUER + "/api/accounts/dynamic-agent-clients/consent/grant"
        response = self.request(url, method="POST", json_data={
            "auth_session_id": session_id, "agent_name": "sub2api",
        }, headers=self.headers(url, headers), timeout=45)
        if response.status not in {200, 301, 302, 303, 307, 308}:
            code = "siwc_consent_failed"
            if response.status == 401:
                try:
                    body = response.json()
                    error = body.get("error") if isinstance(body, dict) else None
                    if isinstance(error, dict) and error.get("code") == "not_authorized":
                        code = "siwc_consent_not_authorized"
                except (ValueError, TypeError):
                    pass
            raise upstream.LoginFlowError(f"SIWC 确认授权失败：HTTP {response.status}",
                                         code=code, status=response.status, retryable=response.status >= 500)
        return response.location() or self.extract_continue_url(response.json())

    def exchange_oauth_callback(self, callback_url):
        parsed = urlsplit(callback_url)
        if (parsed.scheme, parsed.netloc, parsed.path) != ("http", "127.0.0.1:1456", "/auth/callback") or parsed.fragment:
            raise RuntimeError("SIWC 回调地址不匹配")
        query = parse_qs(parsed.query, keep_blank_values=True)
        if "error" in query:
            raise RuntimeError("SIWC 授权被拒绝或未完成")
        for field in ("code", "state", "client_id"):
            if len(query.get(field, [])) != 1 or not query[field][0].strip():
                raise RuntimeError("SIWC 回调缺少或重复参数：" + field)
        if not hmac.compare_digest(query["state"][0], self.oauth_state):
            raise RuntimeError("SIWC 回调 state 不匹配")
        client_id = query["client_id"][0]
        if client_id == "dynamic_agent_client":
            raise RuntimeError("SIWC 回调没有签发有效 client_id")
        bridge.protect_code(query["code"][0])
        if self.payload.get("siwc_authorization_source") == "sub2api":
            bridge.protect_code(callback_url)
            self.log("siwc_token", "已取得回调，交由 s2接口换取凭证并导入所选分组")
            tokens = bridge.call("siwc_callback", callback_url=callback_url, web_session=self.siwc_web_session)
            for key in ("access_token", "refresh_token", "id_token", "web_access_token"):
                bridge.protect_code(tokens.get(key))
            return tokens
        self.log("siwc_token", "已取得 SIWC 授权码，正在交换 RT / AT")
        token_attempts = 1 if self.payload.get("pro_oauth_retry_managed") else 3
        for attempt in range(token_attempts):
            response = self.request(TOKEN, method="POST", form_data={
                "grant_type": "authorization_code", "client_id": client_id,
                "code": query["code"][0], "redirect_uri": self.oauth_redirect_uri,
                "code_verifier": self.oauth_code_verifier, "resource": RESOURCE,
            }, headers=self.headers(TOKEN, {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"}), timeout=60)
            if response.status < 500:
                break
            if attempt < token_attempts - 1:
                self.log("siwc_token", f"SIWC Token 交换 HTTP {response.status}，重试 {attempt + 2}/3", "warning")
                time.sleep(1.5)
        if response.status != 200:
            raise upstream.LoginFlowError(f"SIWC Token 交换失败：HTTP {response.status}",
                                         status=response.status, retryable=response.status >= 500)
        data = response.json()
        for field in ("access_token", "refresh_token", "id_token"):
            if not isinstance(data.get(field), str) or not data[field].strip():
                raise RuntimeError("SIWC Token 结果缺少 " + field)
            bridge.protect_code(data[field])
        claims = self.verify_siwc_id_token(data["id_token"], client_id)
        if str(claims.get("email") or "").strip().lower() != self.payload["email"].strip().lower():
            raise RuntimeError("SIWC Token 账号与当前邮件账号不一致")
        scopes = str(data.get("scope") or "").split()
        expires_in = int(data.get("expires_in") or 0)
        if expires_in <= 0:
            raise RuntimeError("SIWC Token 结果缺少有效期")
        return {"access_token": data["access_token"], "refresh_token": data["refresh_token"],
                "id_token": data["id_token"], "client_id": client_id, "auth_mode": "siwc",
                "expires_at": int(time.time()) + expires_in, "expires_in": expires_in,
                "email": claims["email"], "subject": claims["sub"], "granted_scopes": scopes,
                "plan_use_enabled": "chatgpt.tokens.use.direct" in scopes,
                "ext_agent_host_id": self.siwc_host_id}

    def verify_siwc_id_token(self, token, client_id):
        import jwt
        self.log("siwc_verify", "验证 SIWC Token 的签名及账号信息")
        discovery = self.request(ISSUER + "/.well-known/openid-configuration", timeout=30)
        if discovery.status != 200:
            raise RuntimeError(f"SIWC OpenID 配置读取失败：HTTP {discovery.status}")
        config = discovery.json()
        jwks_url = str(config.get("jwks_uri") or "")
        parsed = urlsplit(jwks_url)
        if config.get("issuer") != ISSUER or parsed.scheme != "https" or parsed.netloc != "auth.openai.com":
            raise RuntimeError("SIWC OpenID 配置地址不匹配")
        jwks = self.request(jwks_url, timeout=30)
        if jwks.status != 200:
            raise RuntimeError(f"SIWC 签名公钥读取失败：HTTP {jwks.status}")
        header = jwt.get_unverified_header(token)
        keys = [k for k in jwks.json().get("keys", []) if k.get("kid") == header.get("kid")
                and k.get("kty") == "RSA" and k.get("use", "sig") == "sig" and k.get("alg", "RS256") == "RS256"]
        if header.get("alg") != "RS256" or len(keys) != 1:
            raise RuntimeError("SIWC Token 签名公钥不匹配")
        try:
            claims = jwt.decode(token, jwt.PyJWK.from_dict(keys[0]).key, algorithms=["RS256"],
                                audience=client_id, issuer=ISSUER, leeway=120,
                                options={"require": ["iss", "aud", "exp", "nonce", "sub", "email"]})
        except jwt.PyJWTError as exc:
            raise RuntimeError("SIWC ID Token 签名或声明校验失败") from exc
        if not hmac.compare_digest(str(claims.get("nonce") or ""), self.siwc_nonce):
            raise RuntimeError("SIWC ID Token nonce 不匹配")
        return claims
