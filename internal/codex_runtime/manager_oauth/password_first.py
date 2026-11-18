"""Password-first continuation shared by every Space OAI login entry."""
from urllib.parse import urlsplit

from . import upstream


class PasswordFirstLogin:
    def can_continue_with_password(self, step, password):
        data = step if isinstance(step, dict) else {}
        return (self.payload.get("selected_login_mode") == "password_totp" and bool(password)
                and not data.get("error")
                and self.extract_page_type(data) == "email_otp_verification"
                and self.extract_email_verification_mode(data) == "passwordless_login"
                and not getattr(self, "_password_first_password_submitted", False)
                and not getattr(self, "_password_first_password_continued", False))

    def mark_required_email_step(self, step):
        data = step if isinstance(step, dict) else {}
        url = self.normalize_auth_url(self.extract_continue_url(data))
        if self.extract_page_type(data) == "email_otp_verification" or urlsplit(url).path == "/email-verification":
            self.payload["_email_code_required"] = True

    def complete_modern_login(self, step, password, issued_after):
        self._password_first_login_issued_after = issued_after
        data = step if isinstance(step, dict) else {}
        url = self.normalize_auth_url(self.extract_continue_url(data))
        self.raise_if_passkey_or_challenge(data, url)
        if self.can_continue_with_password(data, password):
            self._password_first_password_continued = True
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
        self.mark_required_email_step(data)
        return super().complete_modern_login(data, password, issued_after)

    def submit_modern_password(self, password):
        if getattr(self, "_password_first_password_submitted", False):
            raise upstream.LoginFlowError("本次登录已提交密码，未取得后续验证步骤", code="login_step_unsupported")
        self._password_first_password_submitted = True
        step = super().submit_modern_password(password)
        url = self.normalize_auth_url(self.extract_continue_url(step))
        if self.extract_page_type(step) == "login_password" or "/log-in/password" in url:
            raise upstream.LoginFlowError("密码验证未进入下一步", code="login_step_unsupported")
        self.mark_required_email_step(step)
        return step

    def complete_totp_challenge(self, step, secret):
        url = super().complete_totp_challenge(step, secret)
        parsed = urlsplit(self.normalize_auth_url(url))
        if parsed.hostname == "auth.openai.com" and parsed.path == "/email-verification":
            if getattr(self, "_password_first_mfa_email_continued", False):
                raise upstream.LoginFlowError("邮箱和两步验证后仍需继续验证", code="login_step_unsupported")
            self._password_first_mfa_email_continued = True
            self.log("waiting_code", "两步验证后仍需邮箱验证码，继续取码")
            return self.complete_modern_login(
                {"page": {"type": "email_otp_verification"}, "continue_url": url},
                "", self._password_first_login_issued_after,
            )
        return url

    def submit_mfa_verify(self, factor_id, code):
        step = super().submit_mfa_verify(factor_id, code)
        # OAI can return the email page without a continuation URL.
        if self.extract_page_type(step) == "email_otp_verification" and not self.extract_continue_url(step):
            step = {**step, "continue_url": "https://auth.openai.com/email-verification"}
        return step

    def submit_modern_code(self, code):
        step = super().submit_modern_code(code)
        url = self.normalize_auth_url(self.extract_continue_url(step))
        if (self.extract_page_type(step) in {"email_otp_verification", "login_password"}
                or urlsplit(url).path in {"/email-verification", "/log-in/password"}):
            raise upstream.LoginFlowError("邮箱验证后仍返回登录验证页面", code="login_step_unsupported")
        return step
