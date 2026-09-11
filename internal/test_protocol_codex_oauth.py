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
            if self.o.get("password") or self.o.get("passwordless"):
                return Response(url, body={"page": {"type": "login_password"}, "continue_url": "/log-in/password"})
            return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        if path == "/api/accounts/password/verify":
            if self.o.get("totp"):
                return Response(url, body={"page": {"type": "mfa_challenge", "payload": {"factors": [{"id": "factor-1", "factor_type": "totp"}]}}, "continue_url": "/mfa-challenge"})
            return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        if path.endswith(("email-otp/resend", "email-otp/send", "passwordless/send-otp")):
            return Response(url, 404 if self.o.get("resend_fallback") and path.endswith("resend") else 200)
        if path == "/api/accounts/mfa/issue_challenge":
            return Response(url)
        if path.endswith(("email-otp/validate", "mfa/verify")):
            if self.o.get("validate_error"):
                return Response(url, self.o.get("validate_status", 403), {"error": {"code": self.o["validate_error"], "message": self.o["validate_error"]}})
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
        for options, payload in [({"password": True}, {"gpt_password": "private-password"}), ({"passwordless": True, "resend_fallback": True}, {}), ({"password": True, "totp": True}, {"gpt_password": "private-password", "totp_secret": "JBSWY3DPEHPK3PXP"}), ({"direct_consent": True}, {})]:
            with self.subTest(options=options):
                s = Scenario(**options)
                self.success(s, **payload)
                if options.get("direct_consent"):
                    self.assertEqual(s.mail_calls, [0])
                    self.assertNotIn("/api/accounts/email-otp/validate", [c["path"] for c in s.calls])

    def test_passkey_and_missing_totp_not_retryable(self):
        for options, payload, code in [({"passkey": True}, {}, "passkey_or_challenge"), ({"password": True, "totp": True}, {"gpt_password": "pw"}, "totp_secret_missing")]:
            with self.subTest(code=code):
                result = Scenario(**options).run(**payload)
                self.assertEqual(result["error_code"], code)
                self.assertFalse(result["retryable"])
                self.assertFalse(result.get("dead"))

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
