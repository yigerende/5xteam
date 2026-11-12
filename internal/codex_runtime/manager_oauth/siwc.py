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


class SIWCProtocolLogin(ProjectProtocolLogin):
    @classmethod
    def continue_authenticated(cls, flow, web_session=None):
        # A protocol adapter over the existing live client, never a new login.
        adapter = object.__new__(cls)
        adapter.__dict__ = flow.__dict__
        adapter.siwc_web_session = web_session or {}
        adapter.auth_url = adapter.prepare_oauth_authorize_url()
        callback, _ = adapter.capture_oauth_callback(adapter.auth_url)
        if not callback:
            raise RuntimeError("原登录会话未返回 SIWC 授权码")
        return adapter.exchange_oauth_callback(callback)

    def request(self, url, **kwargs):
        if urlsplit(url).path in {"/api/accounts/session/select", "/api/accounts/client_auth_session_dump",
                                  "/api/accounts/dynamic-agent-clients/consent/grant"} and self.sentinel_token:
            kwargs["headers"] = {**(kwargs.get("headers") or {}), "openai-sentinel-token": self.sentinel_token}
        return super().request(url, **kwargs)

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
        current = self.normalize_auth_url(start_url)
        previous = self.auth_url
        chose_account = False
        for _ in range(max_hops):
            parsed = urlsplit(current)
            if (parsed.scheme, parsed.netloc, parsed.path) == ("http", "127.0.0.1:1456", "/auth/callback"):
                # Capture even an error callback without connecting to loopback.
                return current, current
            if parsed.scheme != "https" or parsed.netloc != "auth.openai.com":
                raise RuntimeError("SIWC 授权跳转地址无效")
            response = self.request(current, headers=self.headers(current, {
                "Accept": "text/html,application/xhtml+xml", "Referer": previous,
            }), timeout=45)
            next_url = response.location() if response.status in {301, 302, 303, 307, 308} else ""
            if response.status == 200:
                if parsed.path == "/choose-an-account" and not chose_account:
                    chose_account = True
                    next_url = self.choose_account_from_html(response.text, current)
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
            previous, current = current, urljoin(current, next_url)
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
            raise upstream.LoginFlowError(f"SIWC 确认授权失败：HTTP {response.status}",
                                         status=response.status, retryable=response.status >= 500)
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
        for attempt in range(3):
            response = self.request(TOKEN, method="POST", form_data={
                "grant_type": "authorization_code", "client_id": client_id,
                "code": query["code"][0], "redirect_uri": self.oauth_redirect_uri,
                "code_verifier": self.oauth_code_verifier, "resource": RESOURCE,
            }, headers=self.headers(TOKEN, {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"}), timeout=60)
            if response.status < 500:
                break
            if attempt < 2:
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
