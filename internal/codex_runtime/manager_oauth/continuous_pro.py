"""Pro purchase continuation in one live ChatGPT login session.

Only this entry keeps the web client open while Go waits for GPTPay. Existing
standalone Web AT and Codex login entry points remain unchanged.
"""
import ipaddress
import json
import time
import uuid
from urllib.parse import parse_qs, urlsplit

from . import bridge, upstream
from .adapter import ProjectProtocolLogin


class ContinuousProLogin(ProjectProtocolLogin):
    def __init__(self, payload):
        super().__init__("pro", payload)
        self.exit_ip = str(payload.get("expected_exit_ip") or "").strip()
        try:
            self.exit_ip = str(ipaddress.ip_address(self.exit_ip))
        except ValueError:
            self.exit_ip = ""
        self.observed_ip = ""
        self.client_id = uuid.uuid4().hex
        self.login_client = self.web_session
        self.login_exit_ip = ""
        self.paid_account_id = ""
        self.proxy_transport_error = None

    def web_request_attempt_limit(self):
        # Go owns Pro's cancellable, configured retry wait and recreates the
        # proxy session before payment. Never replay HTTP writes invisibly here.
        return 1

    def complete_modern_login(self, step, password, issued_after):
        self._pro_login_issued_after = issued_after
        data = step if isinstance(step, dict) else {}
        url = self.normalize_auth_url(self.extract_continue_url(data))
        self.raise_if_passkey_or_challenge(data, url)
        if (self.payload.get("selected_login_mode") == "password_totp" and password
                and self.extract_page_type(data) == "email_otp_verification"
                and self.extract_email_verification_mode(data) == "passwordless_login"
                and not getattr(self, "_pro_password_submitted", False)
                and not getattr(self, "_pro_password_continued", False)):
            self._pro_password_continued = True
            self.log("password", "邮箱验证码页，先使用密码继续登录")
            endpoint = "https://auth.openai.com/api/accounts/client_auth_session_dump"
            response = self.request(endpoint, headers=self.headers(endpoint, {
                "Accept": "application/json", "Origin": "https://auth.openai.com",
                "Referer": "https://auth.openai.com/log-in/password",
            }))
            if response.status != 200:
                raise upstream.LoginFlowError("读取当前登录会话失败", code="login_auth_failed", status=response.status)
            body = response.json()
            session = body.get("client_auth_session") if isinstance(body, dict) else None
            session = session if isinstance(session, dict) else {}
            username = session.get("username")
            identities = [session.get("email")]
            if isinstance(username, dict) and username.get("kind") == "email":
                identities.append(username.get("value"))
            identities = [v for v in identities if v is not None]
            expected = str(self.payload.get("email") or "").strip().casefold()
            if (not expected or not identities or any(not isinstance(v, str) or v.strip().casefold() != expected for v in identities)
                    or session.get("email_verification_mode") != "passwordless_login" or body.get("error")):
                raise upstream.LoginFlowError("当前登录会话与账号或登录步骤不一致", code="login_auth_failed")
            self.response_auth_session = session
            self.sentinel_token = upstream.generate_openai_sentinel_token(self.device_id, "password_verify", self.proxy_url)
            data = self.submit_modern_password(password)
        # A mandatory email challenge after password uses the existing mailbox
        # picker, wait budget and validation protocol, within this same client.
        return super().complete_modern_login(data, password, issued_after)

    def submit_modern_password(self, password):
        if getattr(self, "_pro_password_submitted", False):
            raise upstream.LoginFlowError("本次登录已提交密码，未取得后续验证步骤", code="login_step_unsupported")
        self._pro_password_submitted = True
        step = super().submit_modern_password(password)
        url = self.normalize_auth_url(self.extract_continue_url(step))
        if self.extract_page_type(step) == "login_password" or "/log-in/password" in url:
            raise upstream.LoginFlowError("密码验证未进入下一步", code="login_step_unsupported")
        return step

    def complete_totp_challenge(self, step, secret):
        url = super().complete_totp_challenge(step, secret)
        parsed = urlsplit(self.normalize_auth_url(url))
        if parsed.hostname == "auth.openai.com" and parsed.path == "/email-verification":
            if getattr(self, "_pro_mfa_email_continued", False):
                raise upstream.LoginFlowError("邮箱和两步验证后仍需继续验证", code="login_step_unsupported")
            self._pro_mfa_email_continued = True
            self.log("waiting_code", "两步验证后仍需邮箱验证码，继续取码")
            return super().complete_modern_login(
                {"page": {"type": "email_otp_verification"}, "continue_url": url},
                "", self._pro_login_issued_after,
            )
        return url

    def request(self, url, **kwargs):
        if self.proxy_transport_error is not None:
            raise self.proxy_transport_error
        try:
            return super().request(url, **kwargs)
        except Exception as exc:
            message = str(exc).lower()
            if any(marker in message for marker in (
                "connect tunnel failed", "proxy connect aborted",
                "could not resolve proxy", "failed to connect to proxy",
            )):
                # Optional warmup may swallow an error. Do not let it send the
                # next login request through the same broken tunnel. IP trace
                # observations remain diagnostic and do not enter this guard.
                self.proxy_transport_error = exc
            raise

    def client_details(self):
        reused = self.web_session is self.login_client
        return {"initial_client_id": self.client_id,
                "client_id": self.client_id if reused else f"gpt-client-{id(self.web_session):x}",
                "session_reused": reused, "client_consistent": reused,
                "impersonate": upstream.OPENAI_IMPERSONATE}

    def record_login_context(self):
        self.login_exit_ip = self.observed_ip
        bridge.emit("login", "pro_login_context", "首次登录成功，记录 GPT 客户端及出口，保留登录会话供第三步使用",
                    details={**self.client_details(), "login_exit_ip": self.login_exit_ip,
                             "expected_exit_ip": self.exit_ip})

    def record_oauth_context(self, phase, credentials_saved=False):
        client = self.client_details()
        consistent = self.observed_ip == self.login_exit_ip if self.observed_ip and self.login_exit_ip else None
        label = "无法判断" if consistent is None else "一致" if consistent else "不一致"
        bridge.emit("oauth", "pro_login_oauth_comparison",
                    f"{phase}：首次登录与第三步 GPT 客户端{'一致' if client['client_consistent'] else '不一致'}，"
                    f"出口 IP {label}（首次登录 {self.login_exit_ip or '未知'}，第三步 {self.observed_ip or '未知'}）；"
                    + ("RT/AT/Session 已保存" if credentials_saved else "沿用已登录会话直接授权，不重新登录"),
                    details={**client, "phase": phase, "login_exit_ip": self.login_exit_ip,
                             "oauth_exit_ip": self.observed_ip, "ip_consistent": consistent,
                             "credentials_saved": credentials_saved, "relogin": False, "diagnostic_only": True})

    def first_workspace_id(self, data):
        if not self.paid_account_id:
            return super().first_workspace_id(data)
        workspaces = data.get("workspaces") or []
        for item in workspaces:
            if isinstance(item, dict) and item.get("id") == self.paid_account_id:
                return self.paid_account_id
        if data.get("workspace_id") == self.paid_account_id:
            return self.paid_account_id
        raise RuntimeError("OAuth 可选空间中没有本次开通的账号，停止授权其他空间")

    def record_exit(self, phase):
        # Use the SAME curl client, fingerprint, proxy and cookies as the login.
        # Diagnostics only: an unavailable trace or changed IP must not stop Pro.
        self.observed_ip = ""
        status, consistent, error = "unavailable", None, ""
        try:
            response = self.web_session.get("https://www.cloudflare.com/cdn-cgi/trace", timeout=15)
            values = dict(line.split("=", 1) for line in response.text.splitlines() if "=" in line)
            if response.status_code != 200:
                raise ValueError(f"IP 查询 HTTP {response.status_code}")
            self.observed_ip = str(ipaddress.ip_address(values.get("ip", "").strip()))
            if not self.exit_ip:
                self.exit_ip = self.observed_ip
                status = "baseline"
            else:
                consistent = self.observed_ip == self.exit_ip
                status = "consistent" if consistent else "changed"
        except Exception as exc:
            error = bridge.redact(exc)
        label = {"baseline": "记录首次可用出口 IP", "consistent": "出口 IP 与最初记录一致",
                 "changed": "出口 IP 与最初记录不一致", "unavailable": "出口 IP 未能检测"}[status]
        bridge.emit("proxy_check", "pro_ip_observation",
                    f"{phase}：{label}（基准 {self.exit_ip or '未知'}，当前 {self.observed_ip or '未知'}，客户端 {self.client_id[:12]}）；仅记录，继续全自动流程",
                    details={"phase": phase, "expected_exit_ip": self.exit_ip,
                             "exit_ip": self.observed_ip, "ip_check_status": status,
                             "ip_consistent": consistent, **self.client_details(),
                             "diagnostic_only": True, "error": error},
                    level="warning" if status in ("changed", "unavailable") else "info")

    def exit_details(self):
        return {"exit_ip": self.observed_ip, "expected_exit_ip": self.exit_ip,
                "login_exit_ip": self.login_exit_ip, **self.client_details()}

    def continue_codex(self):
        # Generate a new PKCE authorization transaction within the authenticated
        # login session. Never call login()/authorize_continue() a second time.
        self.oauth_state = self.oauth_code_verifier = self.oauth_cpa_state = ""
        self.oauth_client_id = upstream.OPENAI_CODEX_CLIENT_ID
        self.oauth_redirect_uri = upstream.OPENAI_OAUTH_REDIRECT_URI
        self.auth_url = upstream.ChatGPTProtocolLogin.prepare_oauth_authorize_url(self)
        bridge.emit("oauth", "session_reuse", "开通成功，沿用首次登录会话继续 Codex 授权；不重新登录")
        callback, final = self.capture_oauth_callback(self.auth_url)
        if not callback:
            raise RuntimeError("原登录会话未能直接完成 OAuth 授权，可能已失效或要求重新验证；已停止，未重新登录。final=" + bridge.safe_url(final))
        query = parse_qs(urlsplit(callback).query)
        if query.get("state", [""])[0] != self.oauth_state:
            raise RuntimeError("OAuth state 校验失败")
        code = query.get("code", [""])[0]
        if not code or not self.oauth_code_verifier:
            raise RuntimeError("原会话 OAuth 未返回授权码")
        url = upstream.OPENAI_OAUTH_TOKEN_URL
        data = {}
        for attempt in range(3):
            self.record_exit("交换 RT/AT 前")
            response = self.request(url, method="POST", form_data={
                "grant_type": "authorization_code", "client_id": upstream.OPENAI_CODEX_CLIENT_ID,
                "code": code, "redirect_uri": upstream.OPENAI_OAUTH_REDIRECT_URI,
                "code_verifier": self.oauth_code_verifier,
            }, headers=self.headers(url, {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"}), timeout=60)
            data = response.json()
            upstream.token_response(response.status, data)
            if response.status == 200:
                break
            if 400 <= response.status < 500 or attempt == 2:
                raise RuntimeError(f"原会话 OAuth token 交换失败 HTTP {response.status}")
            time.sleep(1.5)
        if not data.get("access_token") or not data.get("refresh_token"):
            raise RuntimeError("开通后 OAuth 结果缺少新的 AT 或 RT")
        return upstream.merge_session_with_oauth({}, data)


def local_payload(payload):
    email = str(payload.get("email") or "").strip()
    pickup = str(payload.get("pickup_url") or "").strip()
    mode = "password_totp" if payload.get("login_mode") == "password_totp" else "email_otp"
    return {**payload, "email": email, "password": str(payload.get("gpt_password") or "") if mode == "password_totp" else "",
            "_totp_secret": str(payload.get("totp_secret") or ""),
            "force_email_code": mode == "email_otp", "email_code_login": mode == "email_otp",
            "selected_login_mode": mode, "credential_mode": "chatgpt_at", "allow_sms": False,
            "skip_phone_verification": True, "hero_managed": True,
            "generic_accounts": [{"email": email, "pickup_url": pickup, "imap_host": pickup,
                                  "mode": "mailtoken" if urlsplit(pickup).fragment else "directurl"}] if pickup else []}


def run(payload, rpc):
    local = local_payload(payload)
    bridge.configure(local, rpc)
    flow = None
    try:
        flow = ContinuousProLogin(local)
        flow.record_exit("首次登录前第 1 次")
        time.sleep(0.8)
        flow.record_exit("首次登录前第 2 次")
        initial = flow.login()
        if flow.rate_limit_error is not None:
            raise flow.rate_limit_error
        if not initial.get("access_token"):
            raise RuntimeError("首次登录没有返回 AT")
        flow.record_exit("第一步登录完成")
        flow.record_login_context()
        claims = upstream.jwt_payload(initial["access_token"])
        flow.paid_account_id = (claims.get("https://api.openai.com/auth") or {}).get("chatgpt_account_id") or (initial.get("account") or {}).get("id", "")
        bridge.protect_code(initial["access_token"])
        if payload.get("paid_recovery"):
            bridge.emit("oauth", "session_held", "使用原账号登录 AT 核验原成功订单，仅恢复授权；不提交开通或付款")
        else:
            bridge.emit("recharge", "session_held", "使用首次登录 AT 交由 GPTPay 开通；GPT 会话保留，供应商连接不参与一致性对比")
        # This RPC blocks without closing the curl Session or proxy lease.
        # No bank/API-key data enters the Python login process.
        result = bridge.call("pro_recharge", session=initial, **flow.exit_details())
        if not result or result.get("status") != "success":
            raise RuntimeError("供应商尚未确认开通成功，停止后续授权")
        flow.record_exit("第三步获取凭证开始")
        flow.record_oauth_context("第三步开始")
        if payload.get("credential_mode") == "siwc":
            from .siwc import SIWCProtocolLogin
            web_before = flow._chatgpt_web_session() if payload.get("siwc_authorization_source") == "sub2api" else None
            tokens = SIWCProtocolLogin.continue_authenticated(flow, web_before)
        else:
            tokens = flow.continue_codex()
        bridge.protect_code(tokens.get("access_token"))
        bridge.protect_code(tokens.get("refresh_token"))
        web_session = flow._chatgpt_web_session()
        if tokens.get("auth_mode") == "siwc":
            tokens["web_access_token"] = web_session.get("access_token") or web_session.get("accessToken") or ""
            tokens["web_account_id"] = flow.paid_account_id
        flow.record_exit("第三步 RT/AT/Session 保存前")
        flow.record_oauth_context("第三步凭证获取完成")
        bridge.call("pro_tokens", tokens=tokens, web_session=web_session, **flow.exit_details())
        flow.record_oauth_context("第三步凭证保存完成", credentials_saved=True)
        return {"success": True, "session_reused": True}
    except (Exception, bridge.DeadAccountError) as exc:
        bridge.emit("pro_auto", "failed", "Pro 连续会话流程停止",
                    details={"error": bridge.redact(exc)}, level="error")
        return {"success": False, "retryable": False, "error": bridge.redact(exc),
                "error_code": exc.code if isinstance(exc, upstream.LoginFlowError) else "",
                "http_status": exc.status if isinstance(exc, upstream.LoginFlowError) else None,
                "session_lost": True, "dead": isinstance(exc, bridge.DeadAccountError)}
    finally:
        if flow is not None:
            flow.close()
            bridge.emit("pro_auto", "session_released", "Pro 登录会话已释放", details={"client_id": flow.client_id})
