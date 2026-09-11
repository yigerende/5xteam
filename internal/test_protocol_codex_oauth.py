"""Offline HTTP fixtures exercise real bootstrap, cookies, PKCE, OTP and consent."""
import ast
import base64
import contextlib
import hashlib
import io
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import upstream as core, bridge
from manager_oauth.adapter import ProjectProtocolLogin, run


def encoded(data):
    return base64.urlsafe_b64encode(json.dumps(data).encode()).decode().rstrip("=")


class Headers(dict):
    def __init__(self, cookies=(), **kwargs):
        super().__init__(**kwargs)
        self.cookies = cookies

    def get_list(self, name):
        return list(self.cookies) if name.lower() == "set-cookie" else []


class Response:
    def __init__(self, url, status=200, body=None, cookies=(), location=""):
        self.url, self.status_code = url, status
        self.headers = Headers(cookies, Location=location)
        self.text = body if isinstance(body, str) else json.dumps(body or {})

    def json(self):
        return json.loads(self.text)


class Scenario:
    def __init__(self, **options):
        self.o = options
        self.calls, self.mail_calls, self.sleeps = [], [], []
        self.state = self.challenge = ""
        self.identifiers = self.tokens = self.phones = 0
        self.logs = io.StringIO()

    def callback(self):
        return "http://localhost:1455/auth/callback?code=private-code&state=" + self.state

    def consent(self, url):
        cookie = "oai-client-auth-session=" + encoded({"workspaces": [{"id": "ws-1"}]}) + "; Path=/; Secure"
        return Response(url, body={"continue_url": "/sign-in-with-chatgpt/codex/consent"}, cookies=[cookie])

    def mfa(self, url):
        factors = [{"id": "recovery", "factor_type": "totp", "is_recovery": True}, {"id": "sms", "factor_type": "sms"}, {"id": "factor-1", "factor_type": "totp"}]
        payload = {"factors": [] if self.o.get("missing_factor") else factors}
        if self.o.get("fallback_factor"):
            payload = {"factor_id": "factor-1"}
        return Response(url, body={"page": {"type": "" if self.o.get("mfa_url_only") else "mfa_challenge", "payload": payload},
                                   "continue_url": "" if self.o.get("mfa_page_only") else "/mfa-challenge"},
                        cookies=["mfa_session=private-mfa-cookie; Path=/; Secure"])

    def request(self, method, url, **kw):
        path = urlsplit(url).path
        raw = kw.get("data")
        data = json.loads(raw) if isinstance(raw, (str, bytes)) else raw
        self.calls.append({"method": method, "path": path, "body": data, **kw})
        if path == "/backend-api/sentinel/req":
            return Response(url, body={"token": "private-sentinel"})
        if path == "/oauth/token":
            self.tokens += 1
            if self.tokens <= self.o.get("token_failures", 0):
                return Response(url, self.o.get("token_status", 502), {"error": "temporary"})
            assert base64.urlsafe_b64encode(hashlib.sha256(data["code_verifier"].encode()).digest()).decode().rstrip("=") == self.challenge
            assert data["code"] == "private-code"
            return Response(url, body={"access_token": "private-at", "refresh_token": "private-rt", "id_token": "x." + encoded({"https://api.openai.com/auth": {"chatgpt_account_id": "account-1"}}) + ".x"})
        if path in ("/oauth/authorize", "/api/oauth/oauth2/auth"):
            q = parse_qs(urlsplit(url).query)
            self.state, self.challenge = q["state"][0], q["code_challenge"][0]
            if self.o.get("network_failure"):
                raise RuntimeError("curl: (56) Connection closed")
            if self.o.get("both_blocked") or (path == "/oauth/authorize" and self.o.get("first_blocked")):
                return Response(url, 403, "<html>Access denied</html>")
            if self.o.get("bootstrap_json"):
                return Response(url, body={"continue_url": "/log-in"})
            return Response(url, cookies=["login_session=private-cookie; Path=/; Secure"])
        if path == "/log-in":
            return Response(url, cookies=["login_session=private-cookie; Path=/; Secure"])
        if path == "/api/accounts/authorize/continue":
            self.identifiers += 1
            if self.o.get("invalid_auth_step") and self.identifiers == 1:
                return Response(url, 400, {"error": {"code": "invalid_auth_step"}})
            if self.o.get("passkey"):
                return Response(url, body={"page": {"type": "auth_challenge"}, "continue_url": "/auth-challenge"})
            if self.o.get("direct_consent"):
                return self.consent(url)
            if self.o.get("direct_mfa"):
                return self.mfa(url)
            if self.o.get("password") or self.o.get("passwordless"):
                return Response(url, body={"page": {"type": "login_password"}, "continue_url": "/log-in/password"})
            return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        if path == "/api/accounts/password/verify":
            if self.o.get("password_error"):
                return Response(url, 401, {"error": {"code": "invalid_password", "message": data["password"]}})
            if self.o.get("password_consent"):
                return self.consent(url)
            if self.o.get("totp"):
                return self.mfa(url)
            return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        if path.endswith(("email-otp/resend", "email-otp/send", "passwordless/send-otp")):
            return Response(url, 404 if self.o.get("resend_fallback") and path.endswith("resend") else 200)
        if path == "/api/accounts/mfa/issue_challenge":
            return Response(url, 500 if self.o.get("issue_failed") else 200)
        if path.endswith(("email-otp/validate", "mfa/verify")):
            if self.o.get("validate_error"):
                return Response(url, self.o.get("validate_status", 403), {"error": {"code": self.o["validate_error"], "message": self.o["validate_error"] + " " + str(data.get("code", ""))}})
            if path.endswith("email-otp/validate") and self.o.get("email_mfa"):
                return self.mfa(url)
            if path.endswith("mfa/verify") and self.o.get("mfa_error"):
                return Response(url, self.o.get("mfa_status", 400), {"error": {"code": self.o["mfa_error"], "message": self.o["mfa_error"] + " " + str(data.get("code", ""))}})
            if self.o.get("phone"):
                return Response(url, body={"page": {"type": "add_phone"}, "continue_url": "/add-phone"})
            return self.consent(url)
        if path == "/add-phone":
            return Response(url)
        if path == "/api/accounts/add-phone/send":
            self.phones += 1
            if self.phones <= self.o.get("reject_phones", 0):
                return Response(url, 400, {"error": {"code": "fraud_guard", "message": "fraud_guard"}})
            return Response(url, body={"continue_url": "/phone-verification"})
        if path == "/api/accounts/phone-otp/validate":
            if self.o.get("phone_rate_limit"):
                return Response(url, 429, {"error": {"code": "rate_limit"}})
            return self.consent(url)
        if path == "/sign-in-with-chatgpt/codex/consent":
            return Response(url, body="<html>Consent</html>")
        if path == "/api/accounts/workspace/select":
            return Response(url, body={"data": {"orgs": [{"id": "org-1", "projects": [{"id": "project-1"}]}]}})
        if path == "/api/accounts/organization/select":
            return Response(url, 302, location=self.callback())
        raise AssertionError("Unexpected HTTP request: " + method + " " + path)

    def post(self, url, **kwargs):
        return self.request("POST", url, **kwargs)

    def mail(self, url, **kwargs):
        self.mail_calls.append(self.identifiers)
        code = "111111" if not self.identifiers or self.o.get("old_code_only") else "234567"
        return "<title>OpenAI verification code</title><p>Your code is " + code + "</p>"

    def run(self, **payload):
        with patch("curl_cffi.requests.request", side_effect=self.request), patch("curl_cffi.requests.post", side_effect=self.post), patch.object(core, "http_request_text", side_effect=self.mail), patch("urllib.request.OpenerDirector.open", side_effect=AssertionError("unexpected real network in fixture")), patch.object(core.time, "sleep", side_effect=self.sleeps.append), contextlib.redirect_stderr(self.logs):
            return run({"email": "fixture@example.com", "pickup_url": "https://mail.example/pickup/private", "proxy": "http://user:proxy-password@proxy.example:8080", **payload})


class FakeProvider:
    def __init__(self):
        self.count, self.released = 0, []

    def config_ready(self, cfg):
        return True

    def acquire(self, cfg):
        self.count += 1
        return {"ok": True, "activation_id": str(self.count), "phone_number": "+1555000000" + str(self.count)}

    def fetch_code(self, row):
        return {"found": True, "code": "456789"}

    def release(self, row, ok):
        self.released.append((row["id"], ok))
        return {"ok": True}


class ProtocolFlowTests(unittest.TestCase):
    def success(self, s, **payload):
        result = s.run(**payload)
        self.assertTrue(result.get("success"), result)
        self.assertEqual(result["refresh_token"], "private-rt")
        self.assertEqual(result["account_id"], "account-1")

    def test_full_http_chain_headers_cookie_pkce(self):
        s = Scenario()
        self.success(s)
        self.assertEqual(s.mail_calls[:2], [0, 1])
        self.assertIn(16, s.sleeps)
        api = [c for c in s.calls if c["path"].startswith("/api/accounts/")]
        self.assertTrue(all("login_session=private-cookie" in c["headers"].get("Cookie", "") for c in api))
        self.assertTrue(all(c["impersonate"] == "chrome131" for c in s.calls))
        self.assertTrue(all(c["proxies"]["https"] == "http://user:proxy-password@proxy.example:8080" for c in s.calls))
        self.assertTrue(all(c.get("allow_redirects") is False for c in api))
        otp = next(c for c in api if c["path"].endswith("email-otp/validate"))
        self.assertIn("openai-sentinel-token", otp["headers"])
        self.assertEqual(otp["body"], {"code": "234567"})
        self.assertEqual(next(c for c in api if c["path"].endswith("organization/select"))["body"], {"org_id": "org-1", "project_id": "project-1"})

    def test_bootstrap_fallback_json_and_reinitialization(self):
        for options in ({"first_blocked": True}, {"bootstrap_json": True}, {"invalid_auth_step": True}):
            with self.subTest(options=options):
                s = Scenario(**options)
                self.success(s)
                self.assertNotIn(900, s.sleeps)
                if options.get("first_blocked"):
                    self.assertEqual([c["path"] for c in s.calls[:2]], ["/oauth/authorize", "/api/oauth/oauth2/auth"])
                if options.get("invalid_auth_step"):
                    self.assertEqual(s.identifiers, 2)

    def test_both_authorize_routes_blocked_do_not_send_otp(self):
        s = Scenario(both_blocked=True)
        r = s.run()
        self.assertEqual(r["error_code"], "oauth_session_missing")
        self.assertTrue(r["retryable"])
        self.assertEqual(r["http_status"], 403)
        self.assertFalse(r.get("dead"))
        self.assertEqual(s.identifiers, 0)
        self.assertEqual(s.mail_calls, [])

    def test_network_retry_same_context(self):
        s = Scenario(network_failure=True)
        self.assertTrue(s.run()["retryable"])
        self.assertEqual(len(s.calls), 6)
        self.assertEqual([round(value, 2) for value in s.sleeps], [0.6, 1.3, 0.6, 1.3])
        self.assertEqual(len({c["headers"].get("Cookie") for c in s.calls}), 1)

    def test_password_passwordless_totp_and_direct_consent(self):
        for options, payload in [({"password": True}, {"login_mode": "password_totp", "gpt_password": "private-password", "totp_secret": "JBSWY3DPEHPK3PXP"}), ({"passwordless": True, "resend_fallback": True}, {}), ({"password": True, "totp": True}, {"login_mode": "password_totp", "gpt_password": "private-password", "totp_secret": "JBSWY3DPEHPK3PXP"}), ({"direct_consent": True}, {})]:
            with self.subTest(options=options):
                s = Scenario(**options)
                self.success(s, **payload)
                if options.get("direct_consent"):
                    self.assertEqual(s.mail_calls, [0])
                    self.assertNotIn("/api/accounts/email-otp/validate", [c["path"] for c in s.calls])

    def test_passkey_and_missing_totp_not_retryable(self):
        for options, payload, code in [({"passkey": True}, {}, "passkey_or_challenge"), ({"direct_mfa": True}, {}, "totp_secret_missing")]:
            with self.subTest(code=code):
                result = Scenario(**options).run(**payload)
                self.assertEqual(result["error_code"], code)
                self.assertFalse(result["retryable"])
                self.assertFalse(result.get("dead"))

    def test_default_email_mode_does_not_submit_saved_password_or_totp(self):
        s = Scenario(password=True, totp=True)
        r = s.run(gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
        self.assertTrue(r["success"], r)
        self.assertEqual(r["login_method"], "email_otp")
        self.assertEqual(r["login_auth_status"], "succeeded")
        paths = [c["path"] for c in s.calls]
        self.assertNotIn("/api/accounts/password/verify", paths)
        self.assertNotIn("/api/accounts/mfa/verify", paths)
        self.assertIn("/api/accounts/email-otp/validate", paths)

    def test_totp_reference_requests_headers_body_and_continuation(self):
        for extra in ({}, {"issue_failed": True}, {"fallback_factor": True}):
            with self.subTest(extra=extra), patch("pyotp.TOTP.now", return_value="765432"):
                s = Scenario(password=True, totp=True, **extra)
                r = s.run(login_mode="password_totp", gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
                self.assertTrue(r["success"], r)
                self.assertEqual(r["login_method"], "password_totp")
                self.assertEqual(r["login_auth_status"], "succeeded")
                paths = [c["path"] for c in s.calls]
                expected = ["/api/accounts/password/verify", "/api/accounts/mfa/issue_challenge", "/api/accounts/mfa/verify", "/api/accounts/workspace/select", "/api/accounts/organization/select", "/oauth/token"]
                self.assertEqual([p for p in paths if p in expected], expected)
                self.assertNotIn("/api/accounts/email-otp/validate", paths)
                password, issue, verify = [next(c for c in s.calls if c["path"] == p) for p in expected[:3]]
                self.assertEqual(password["body"], {"password": "private-password"})
                self.assertIn("openai-sentinel-token", password["headers"])
                self.assertEqual(issue["body"], {"id": "factor-1", "type": "totp", "force_fresh_challenge": False})
                self.assertEqual(verify["body"], {"id": "factor-1", "type": "totp", "code": "765432"})
                self.assertEqual(issue["headers"]["Referer"], "https://auth.openai.com/log-in/password")
                self.assertEqual(verify["headers"]["Referer"], "https://auth.openai.com/mfa-challenge/factor-1")
                for call in (password, issue, verify):
                    self.assertEqual(call["headers"]["Origin"], "https://auth.openai.com")
                    self.assertEqual(call["method"], "POST")
                for secret in ("private-password", "JBSWY3DPEHPK3PXP", "765432"):
                    self.assertNotIn(secret, s.logs.getvalue())

    def test_email_otp_then_totp_uses_existing_mfa_and_oauth_context(self):
        for extra in ({}, {"mfa_url_only": True}, {"mfa_page_only": True}, {"issue_failed": True}, {"fallback_factor": True}):
            with self.subTest(extra=extra), patch("pyotp.TOTP.now", return_value="765432"):
                s = Scenario(email_mfa=True, **extra)
                r = s.run(totp_secret="JBSWY3DPEHPK3PXP")
                self.assertTrue(r["success"], r)
                self.assertEqual(r["refresh_token"], "private-rt")
                self.assertEqual(r["login_method"], "email_otp_totp")
                self.assertEqual(r["login_auth_status"], "succeeded")
                paths = [c["path"] for c in s.calls]
                expected = ["/api/accounts/email-otp/validate", "/api/accounts/mfa/issue_challenge", "/api/accounts/mfa/verify", "/api/accounts/workspace/select", "/api/accounts/organization/select", "/oauth/token"]
                self.assertEqual([p for p in paths if p in expected], expected)
                self.assertNotIn("/api/accounts/password/verify", paths)
                self.assertEqual(s.identifiers, 1)
                issue, verify = [next(c for c in s.calls if c["path"] == p) for p in expected[1:3]]
                self.assertEqual(issue["body"], {"id": "factor-1", "type": "totp", "force_fresh_challenge": False})
                self.assertEqual(verify["body"], {"id": "factor-1", "type": "totp", "code": "765432"})
                for call in (issue, verify):
                    self.assertIn("login_session=private-cookie", call["headers"]["Cookie"])
                    self.assertIn("mfa_session=private-mfa-cookie", call["headers"]["Cookie"])
                    self.assertEqual(call["proxies"]["https"], "http://user:proxy-password@proxy.example:8080")
                    self.assertEqual(call["impersonate"], "chrome131")
                for secret in ("JBSWY3DPEHPK3PXP", "765432", "234567", "private-mfa-cookie"):
                    self.assertNotIn(secret, s.logs.getvalue())

    def test_email_totp_fallback_and_password_email_totp_metadata(self):
        for payload, expected in [
            ({"login_mode": "email_otp", "configured_login_mode": "password_totp", "login_fallback_reason": "missing_password"}, "email_otp_totp"),
            ({"login_mode": "email_otp", "gpt_password": "private-password"}, "email_otp_totp"),
            ({"login_mode": "password_totp", "gpt_password": "private-password"}, "password_email_otp_totp"),
        ]:
            with self.subTest(payload=payload), patch("pyotp.TOTP.now", return_value="765432"):
                s = Scenario(password=True, email_mfa=True)
                r = s.run(totp_secret="JBSWY3DPEHPK3PXP", **payload)
                self.assertTrue(r["success"], r)
                self.assertEqual(r["login_method"], expected)
                self.assertEqual(r["login_fallback_reason"], payload.get("login_fallback_reason", ""))
                self.assertEqual(any(c["path"].endswith("password/verify") for c in s.calls), payload["login_mode"] == "password_totp")

    def test_email_mfa_missing_invalid_secret_or_factor_stops_before_tokens(self):
        for secret, extra, error in [("", {}, "totp_secret_missing"), (" \t", {}, "totp_secret_missing"), ("not-base32!", {}, "totp_secret_invalid"), ("JBSWY3DPEHPK3PXP", {"missing_factor": True}, "totp_factor_missing")]:
            with self.subTest(error=error, secret_present=bool(secret)):
                s = Scenario(email_mfa=True, **extra)
                r = s.run(totp_secret=secret)
                self.assertFalse(r["success"], r)
                self.assertEqual(r["error_code"], error)
                self.assertFalse(r["retryable"])
                self.assertFalse(r.get("dead"))
                self.assertEqual(r["login_auth_status"], "failed")
                self.assertEqual(s.tokens, 0)
                self.assertNotIn("/api/accounts/mfa/verify", [c["path"] for c in s.calls])

    def test_email_mfa_errors_preserve_error_handling_and_redact_codes(self):
        for code, status in [("invalid_code", 400), ("account_deactivated", 403), ("forbidden", 403), ("rate_limit", 429)]:
            with self.subTest(code=code), patch("pyotp.TOTP.now", return_value="765432"):
                s = Scenario(email_mfa=True, mfa_error=code, mfa_status=status)
                r = s.run(totp_secret="JBSWY3DPEHPK3PXP")
                self.assertFalse(r["success"], r)
                self.assertEqual(bool(r.get("dead")), code == "account_deactivated")
                self.assertEqual(r["login_auth_status"], "failed")
                self.assertEqual(r["login_method"], "email_otp_totp")
                self.assertEqual(s.tokens, 0)
                self.assertNotIn("765432", s.logs.getvalue())
                self.assertNotIn("765432", r["error"])

    def test_email_totp_then_phone_reuses_existing_sms_flow(self):
        provider = FakeProvider()
        s = Scenario(email_mfa=True, phone=True)
        with patch.object(core.sp, "get_sms_provider", return_value=provider), patch("pyotp.TOTP.now", return_value="765432"):
            r = s.run(totp_secret="JBSWY3DPEHPK3PXP", sms_provider="hero_sms", sms_config={"enabled": True})
        self.assertTrue(r["success"], r)
        self.assertEqual(r["login_method"], "email_otp_totp")
        self.assertEqual(provider.count, 1)
        self.assertEqual(provider.released, [("hero_sms:1", True)])

    def test_invalid_password_totp_secret_and_factor_never_downgrade(self):
        for options, secret in [({"password_error": True}, "JBSWY3DPEHPK3PXP"), ({"validate_error": "invalid_code", "validate_status": 400}, "JBSWY3DPEHPK3PXP"), ({}, "not-base32!"), ({"missing_factor": True}, "JBSWY3DPEHPK3PXP")]:
            with self.subTest(options=options, invalid_secret=secret == "not-base32!"):
                s = Scenario(password=True, totp=True, **options)
                r = s.run(login_mode="password_totp", gpt_password="private-password", totp_secret=secret)
                self.assertFalse(r["success"], r)
                self.assertFalse(r["retryable"], r)
                self.assertEqual(r["login_auth_status"], "failed")
                self.assertNotIn("/api/accounts/email-otp/validate", [c["path"] for c in s.calls])
                self.assertEqual(s.tokens, 0)
                self.assertNotIn("private-password", s.logs.getvalue())
                self.assertNotIn(secret, s.logs.getvalue())

    def test_actual_login_metadata_and_context_reset(self):
        for options, mode, expected in [({"password": True, "password_consent": True}, "password_totp", "password"), ({"password": True}, "password_totp", "password_email_otp"), ({"direct_consent": True}, "email_otp", "existing_session"), ({"both_blocked": True}, "password_totp", "not_started"), ({}, "email_otp", "email_otp")]:
            with self.subTest(options=options):
                r = Scenario(**options).run(login_mode=mode, gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
                self.assertEqual(r["login_method"], expected, r)
                self.assertEqual(r["configured_login_mode"], mode)
                self.assertEqual(r["login_auth_status"], "not_started" if expected == "not_started" else "succeeded")

    def test_auth_success_is_not_lost_when_token_exchange_fails(self):
        s = Scenario(password=True, totp=True, token_failures=2, token_status=400)
        r = s.run(login_mode="password_totp", gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
        self.assertFalse(r["success"])
        self.assertEqual(r["login_auth_status"], "succeeded")
        self.assertEqual(r["login_method"], "password_totp")

    def test_totp_error_redaction_and_dead_account_handling(self):
        for code, status in [("account_deactivated", 403), ("invalid_code", 400), ("rate_limit", 429), ("forbidden", 403)]:
            with self.subTest(code=code), patch("pyotp.TOTP.now", return_value="765432"):
                s = Scenario(password=True, totp=True, validate_error=code, validate_status=status)
                r = s.run(login_mode="password_totp", gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
                self.assertEqual(bool(r.get("dead")), code == "account_deactivated", r)
                self.assertEqual(r["login_auth_status"], "failed")
                self.assertNotIn("765432", s.logs.getvalue())
                self.assertNotIn("765432", r["error"])
                self.assertEqual(s.tokens, 0)

    def test_old_code_fallback_after_all_polls(self):
        s = Scenario(old_code_only=True)
        self.success(s)
        self.assertEqual(len(s.mail_calls), 4)
        self.assertEqual(s.sleeps.count(30), 3)
        self.assertEqual(next(c for c in s.calls if c["path"].endswith("email-otp/validate"))["body"], {"code": "111111"})

    def test_dead_and_non_dead_errors(self):
        for code in sorted(bridge.DEAD_CODES):
            with self.subTest(code=code):
                s = Scenario(validate_error=code)
                result = s.run()
                self.assertTrue(result["dead"])
                self.assertEqual(result["error_code"], code)
                self.assertFalse(result["retryable"])
                self.assertEqual(s.tokens, 0)
        for status, code in [(403, "forbidden"), (429, "rate_limit"), (400, "invalid_auth_step"), (400, "invalid_code")]:
            with self.subTest(code=code):
                result = Scenario(validate_error=code, validate_status=status).run()
                self.assertFalse(result["success"])
                self.assertFalse(result.get("dead"))

    def test_token_5xx_retry_and_4xx_stop(self):
        s = Scenario(token_failures=2)
        self.success(s)
        self.assertEqual(s.tokens, 3)
        self.assertEqual(s.sleeps.count(1.5), 2)
        s = Scenario(token_failures=2, token_status=400)
        self.assertFalse(s.run()["success"])
        self.assertEqual(s.tokens, 1)

    def test_sms_only_on_addphone_replaces_rejected_number(self):
        provider = FakeProvider()
        with patch.object(core.sp, "get_sms_provider", return_value=provider):
            self.success(Scenario(), sms_provider="hero_sms", sms_config={"enabled": True})
        self.assertEqual(provider.count, 0)
        s = Scenario(phone=True, reject_phones=1)
        with patch.object(core.sp, "get_sms_provider", return_value=provider):
            self.success(s, sms_provider="hero_sms", sms_config={"enabled": True})
        self.assertEqual(provider.count, 2)
        self.assertEqual(provider.released, [("hero_sms:1", False), ("hero_sms:2", True)])
        for c in s.calls:
            if c["path"].endswith(("add-phone/send", "phone-otp/validate")):
                self.assertNotIn("openai-sentinel-token", c["headers"])

    def test_sms_rate_limit_and_three_rejections_stop(self):
        for options, count, code, retry in [({"phone_rate_limit": True}, 1, "phone_2fa_rate_limited", True), ({"reject_phones": 3}, 3, "phone_2fa_failed", False)]:
            with self.subTest(options=options):
                p = FakeProvider()
                s = Scenario(phone=True, **options)
                with patch.object(core.sp, "get_sms_provider", return_value=p):
                    r = s.run(sms_provider="hero_sms", sms_config={"enabled": True})
                self.assertEqual(r["error_code"], code)
                self.assertEqual(r["retryable"], retry)
                self.assertEqual(p.count, count)
                self.assertEqual(s.tokens, 0)

    def test_logs_have_routing_but_no_credentials(self):
        s = Scenario(first_blocked=True)
        self.success(s, gpt_password="private-password")
        text = s.logs.getvalue()
        for value in ("private-at", "private-rt", "private-cookie", "private-sentinel", "private-password", "proxy-password", "code=private-code"):
            self.assertNotIn(value, text)
        self.assertIn('"http_status":403', text)
        self.assertIn("login_session", text)

    def test_account_cookie_pkce_and_proxy_isolation(self):
        a = ProjectProtocolLogin("a", {"proxy": "http://a.example:8080"})
        b = ProjectProtocolLogin("b", {"proxy": "socks5://b.example:1080"})
        a.prepare_oauth_authorize_url()
        b.prepare_oauth_authorize_url()
        a.set_cookie("login_session", "account-a", "auth.openai.com")
        self.assertEqual(b.cookie_value("login_session"), "")
        self.assertNotEqual(a.oauth_code_verifier, b.oauth_code_verifier)
        self.assertNotEqual(a.oauth_state, b.oauth_state)
        self.assertEqual(core._cffi_proxies(b.proxy_url)["https"], "socks5h://b.example:1080")
        with self.assertRaisesRegex(RuntimeError, "state mismatch"):
            a.exchange_oauth_callback("http://localhost:1455/auth/callback?code=x&state=wrong")

    def test_upstream_source_manifest(self):
        directory = Path(core.__file__).parent
        manifest = json.loads((directory / "parity.json").read_text(encoding="utf-8"))
        tree = ast.parse(Path(core.__file__).read_text(encoding="utf-8"))
        nodes = {}
        for n in tree.body:
            if isinstance(n, (ast.FunctionDef, ast.ClassDef)):
                nodes[n.name] = n
                if isinstance(n, ast.ClassDef):
                    for m in n.body:
                        if isinstance(m, ast.FunctionDef):
                            nodes[n.name + "." + m.name] = m
        for name, expected in manifest["identical_ast_sha256"].items():
            with self.subTest(method=name):
                self.assertEqual(hashlib.sha256(ast.dump(nodes[name], include_attributes=False).encode()).hexdigest(), expected)


if __name__ == "__main__":
    unittest.main()
