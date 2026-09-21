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
        bridge.emit("recharge", "session_held", "使用首次登录 AT 交由 GPTPay 开通；GPT 会话保留，供应商连接不参与一致性对比")
        # This RPC blocks without closing the curl Session or proxy lease.
        # No bank/API-key data enters the Python login process.
        result = bridge.call("pro_recharge", session=initial, **flow.exit_details())
        if not result or result.get("status") != "success":
            raise RuntimeError("供应商尚未确认开通成功，停止后续授权")
        flow.record_exit("第三步获取凭证开始")
        flow.record_oauth_context("第三步开始")
        tokens = flow.continue_codex()
        bridge.protect_code(tokens.get("access_token"))
        bridge.protect_code(tokens.get("refresh_token"))
        web_session = flow._chatgpt_web_session()
        flow.record_exit("第三步 RT/AT/Session 保存前")
        flow.record_oauth_context("第三步凭证获取完成")
        bridge.call("pro_tokens", tokens=tokens, web_session=web_session, **flow.exit_details())
        flow.record_oauth_context("第三步凭证保存完成", credentials_saved=True)
        return {"success": True, "session_reused": True}
    except (Exception, bridge.DeadAccountError) as exc:
        bridge.emit("pro_auto", "failed", "Pro 连续会话流程停止",
                    details={"error": bridge.redact(exc)}, level="error")
        return {"success": False, "retryable": False, "error": bridge.redact(exc),
                "session_lost": True, "dead": isinstance(exc, bridge.DeadAccountError)}
    finally:
        if flow is not None:
            flow.close()
            bridge.emit("pro_auto", "session_released", "Pro 登录会话已释放", details={"client_id": flow.client_id})
