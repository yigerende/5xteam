"""Every OAI login entry uses password/TOTP before a mandatory email code."""
import contextlib
import io
import json
import unittest
from urllib.parse import parse_qs, urlsplit
from unittest.mock import patch

from test_protocol_codex_oauth import Scenario, Response, encoded
from test_protocol_pro_auto import CookieStore
from manager_oauth import adapter, upstream, help_confirmation, merge_v2, merge_v2_plan, siwc


class PasswordScenario(Scenario):
    def __init__(self, **options):
        super().__init__(password=True, totp=True, **options)
        self.order, self.clients = [], []

    def request(self, method, url, **kw):
        path = urlsplit(url).path
        self.order.append(path)
        if path == "/api/accounts/authorize/continue" and not self.o.get("normal"):
            self.identifiers += 1
            return Response(url, body={"page": {"type": "email_otp_verification", "payload": {"email_verification_mode": "passwordless_login"}}, "continue_url": "/email-verification"})
        if path == "/api/accounts/client_auth_session_dump":
            assert method == "GET"
            if self.o.get("dump_network"):
                raise RuntimeError("CONNECT tunnel failed, response 504")
            return Response(url, self.o.get("dump_status", 200), {"client_auth_session": {
                "email": self.o.get("dump_email", "fixture@example.com"),
                "email_verification_mode": "passwordless_login"}}, cookies=["password_state=same-session; Path=/; Secure"])
        if path == "/api/accounts/password/verify":
            if not self.o.get("normal"):
                assert "password_state=same-session" in kw["headers"]["Cookie"]
            if self.o.get("password_email"):
                return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        if path == "/api/accounts/mfa/verify" and self.o.get("mfa_email"):
            return Response(url, body={"page": {"type": "email_otp_verification"},
                                      **({} if self.o.get("page_only") else {"continue_url": "/email-verification"})})
        if path == "/api/accounts/email-otp/validate" and self.o.get("repeat_email"):
            return Response(url, body={"page": {"type": "email_otp_verification"}, "continue_url": "/email-verification"})
        return super().request(method, url, **kw)

    def mail(self, url, **kw):
        self.order.append("mailbox")
        return super().mail(url, **kw)

    def web_run(self, entry="at", **payload):
        scenario = self
        class Session:
            def __init__(self, **kw):
                self.cookies = CookieStore()
                self.closed = False
                self.options = kw
                scenario.clients.append(self)
            def close(self):
                self.closed = True
            def request(self, method, url, **kw):
                assert not self.closed
                path = urlsplit(url).path
                if path == "/": return Response(url)
                if path == "/api/auth/csrf": return Response(url, body={"csrfToken": "csrf"})
                if path == "/api/auth/signin/openai":
                    return Response(url, body={"url": "https://auth.openai.com/authorize?state=web-state"})
                if path == "/authorize":
                    self.cookies.set("login_session", "auth-cookie", "auth.openai.com", "/")
                    return Response(url)
                if path == "/api/auth/callback/openai":
                    self.cookies.set("__Secure-next-auth.session-token", "web-cookie", "chatgpt.com", "/")
                    return Response(url)
                if path == "/api/auth/session":
                    account = "fixture-team" if entry == "team" else "personal"
                    claims = {"email": "fixture@example.com", "https://api.openai.com/auth": {"chatgpt_account_id": account, "chatgpt_user_id": "fixture-child"}}
                    return Response(url, body={"accessToken": "x." + encoded(claims) + ".x", "user": {"email": "fixture@example.com"}, "account": {"id": account}})
                # The cffi client sends its own cookie jar in production.
                kw["headers"]["Cookie"] = "; ".join(c.name + "=" + c.value for c in self.cookies.jar)
                if path == "/api/accounts/client_auth_session_dump":
                    self.cookies.set("password_state", "same-session", "auth.openai.com", "/")
                if path == "/api/accounts/password/verify" and scenario.o.get("rate_once") and len(scenario.clients) == 1:
                    scenario.order.append(path)
                    limited = Response(url, 429, {"error": {"code": "rate_limit_exceeded"}})
                    limited.headers["Retry-After"] = "1"
                    return limited
                response = scenario.request(method, url, **kw)
                # Stop at the validated Web Session; business work is mocked.
                if response.status_code == 200:
                    try: data = response.json()
                    except (ValueError, TypeError): data = {}
                    if data.get("continue_url") == "/sign-in-with-chatgpt/codex/consent":
                        return Response(url, body={"continue_url": "https://chatgpt.com/api/auth/callback/openai?code=fixture&state=web-state"})
                return response
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch("curl_cffi.requests.Session", Session))
            stack.enter_context(patch("curl_cffi.requests.request", side_effect=AssertionError("unexpected separate request")))
            stack.enter_context(patch("curl_cffi.requests.post", side_effect=AssertionError("unexpected network")))
            stack.enter_context(patch("urllib.request.OpenerDirector.open", side_effect=AssertionError("unexpected network")))
            stack.enter_context(patch.object(upstream, "generate_openai_sentinel_token", return_value="fixture-sentinel"))
            stack.enter_context(patch.object(upstream, "http_request_text", side_effect=self.mail))
            stack.enter_context(patch.object(upstream.time, "sleep", side_effect=self.sleeps.append))
            stack.enter_context(contextlib.redirect_stderr(self.logs))
            args = {"credential_mode": "chatgpt_at"}
            if entry == "oai":
                args["help_oai"] = {"request_id": "fixture", "prepare_only": True}
                stack.enter_context(patch.object(help_confirmation.HelpInquiry, "authorize", return_value=None))
            elif entry == "team":
                args["merge_v2"] = {"team_id": "fixture-team"}
                stack.enter_context(patch.object(merge_v2.TeamProtocolLogin, "submit_export", side_effect=lambda session: {"success": True}))
            elif entry == "personal":
                args["merge_v2_plan"] = {"team_id": "fixture-team"}
                stack.enter_context(patch.object(merge_v2_plan, "run", side_effect=lambda flow: {**flow.login(), "success": True}))
            elif entry == "siwc":
                args.update(credential_mode="siwc", siwc_host_id="fixture-host")
                stack.enter_context(patch.object(siwc.SIWCProtocolLogin, "continue_authenticated", return_value={"access_token": "siwc-at"}))
            return adapter.run({"email": "fixture@example.com", "pickup_url": "https://mail.example/fixture",
                                "proxy": "http://fixture:private@proxy.invalid:80", "login_mode": "password_totp",
                                "gpt_password": "fixture-password", "totp_secret": "JBSWY3DPEHPK3PXP", **args, **payload}, rpc=lambda method, args: True)


class SharedPasswordTests(unittest.TestCase):
    def assert_sequence(self, scenario, *, email=False, normal=False):
        order = scenario.order
        password = order.index("/api/accounts/password/verify")
        if normal:
            self.assertNotIn("/api/accounts/client_auth_session_dump", order)
            self.assertNotIn("先使用密码继续登录", scenario.logs.getvalue())
        else:
            self.assertLess(order.index("/api/accounts/client_auth_session_dump"), password)
        if email:
            self.assertLess(password, order.index("mailbox"))
            if not scenario.o.get("password_email"):
                self.assertLess(order.index("/api/accounts/mfa/verify"), order.index("mailbox"))
            self.assertEqual(order.count("/api/accounts/email-otp/validate"), 1)
        else:
            self.assertNotIn("mailbox", order)
            self.assertNotIn("/api/accounts/email-otp/validate", order)
        self.assertEqual(order.count("/api/accounts/password/verify"), 1)

    def test_all_web_login_entries_password_before_email(self):
        for entry in ("at", "oai", "team", "personal", "siwc"):
            for extra in ({}, {"password_email": True}, {"mfa_email": True}, {"mfa_email": True, "page_only": True}):
                with self.subTest(entry=entry, extra=extra):
                    scenario = PasswordScenario(**extra)
                    result = scenario.web_run(entry)
                    self.assertTrue(result["success"], result)
                    self.assert_sequence(scenario, email=bool(extra))
                    self.assertEqual(len(scenario.clients), 1)
                    self.assertTrue(scenario.clients[0].closed)

    def test_codex_oauth_and_normal_password(self):
        for extra in ({}, {"normal": True}, {"mfa_email": True}, {"password_email": True}):
            with self.subTest(extra=extra):
                scenario = PasswordScenario(**extra)
                result = scenario.run(login_mode="password_totp", gpt_password="fixture-password", totp_secret="JBSWY3DPEHPK3PXP")
                self.assertTrue(result["success"], result)
                self.assert_sequence(scenario, email=bool(extra.get("mfa_email") or extra.get("password_email")), normal=bool(extra.get("normal")))
                if extra.get("mfa_email"):
                    self.assertEqual(result["login_method"], "password_email_otp_totp")

    def test_normal_web_password_does_not_claim_a_switch(self):
        scenario = PasswordScenario(normal=True)
        self.assertTrue(scenario.web_run()["success"])
        self.assert_sequence(scenario, normal=True)

    def test_missing_pickup_link_still_allows_password_and_totp(self):
        for entry in ("at", "oai", "team", "personal"):
            scenario = PasswordScenario()
            self.assertTrue(scenario.web_run(entry, pickup_url="")["success"])
            self.assert_sequence(scenario)

    def test_wrong_identity_password_totp_and_repeated_email_never_succeed(self):
        for extra in ({"dump_email": "other@example.com"}, {"dump_status": 403}, {"dump_network": True},
                      {"password_error": True}, {"mfa_error": "invalid_code"}, {"mfa_email": True, "repeat_email": True}):
            with self.subTest(extra=extra):
                scenario = PasswordScenario(**extra)
                result = scenario.web_run()
                self.assertFalse(result["success"], result)
                if not extra.get("repeat_email"):
                    self.assertNotIn("mailbox", scenario.order)
                self.assertLessEqual(scenario.order.count("/api/accounts/password/verify"), 1)
                self.assertNotIn("fixture-password", scenario.logs.getvalue())

    def test_required_email_without_source_fails_after_password(self):
        scenario = PasswordScenario(mfa_email=True)
        result = scenario.web_run("team", pickup_url="")
        self.assertFalse(result["success"])
        self.assertEqual(result["error_code"], "email_code_source_missing")
        self.assertIn("/api/accounts/mfa/verify", scenario.order)

    def test_new_login_retry_uses_password_again_on_same_proxy(self):
        scenario = PasswordScenario(rate_once=True)
        result = scenario.web_run()
        self.assertTrue(result["success"], result)
        self.assertEqual(result["rate_limit_retries"], 1)
        self.assertEqual(len(scenario.clients), 2)
        self.assertEqual(scenario.order.count("/api/accounts/password/verify"), 2)
        self.assertEqual(scenario.order.count("/api/accounts/client_auth_session_dump"), 2)
        self.assertNotIn("mailbox", scenario.order)
        self.assertEqual(scenario.clients[0].options, scenario.clients[1].options)
        self.assertTrue(all(c.closed for c in scenario.clients))

    def test_explicit_email_mode_keeps_original_mailbox_flow(self):
        scenario = PasswordScenario()
        result = scenario.web_run(login_mode="email_otp")
        self.assertTrue(result["success"], result)
        self.assertIn("mailbox", scenario.order)
        self.assertNotIn("/api/accounts/password/verify", scenario.order)
        self.assertNotIn("/api/accounts/client_auth_session_dump", scenario.order)


if __name__ == "__main__":
    unittest.main()
