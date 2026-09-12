"""Offline contract tests for the ChatGPT temporary-AT protocol mode."""
from pathlib import Path
import contextlib
import io
import json
import sys
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import adapter


class ChatGPTATFlowTests(unittest.TestCase):
    def test_swallowed_429_stops_requests_and_cannot_report_success(self):
        original = adapter.ProjectProtocolLogin
        response = adapter.upstream.ProtocolResponse(429, "https://auth.openai.com/test", {}, '{"error":{"code":"rate_limit_exceeded"}}')
        requests = []

        class Flow(original):
            def login(self):
                with patch.object(self, "_web_session_request", side_effect=lambda *a, **k: requests.append(a) or response):
                    for _ in range(2):
                        try:
                            self.request("https://auth.openai.com/test")
                        except adapter.upstream.LoginFlowError:
                            pass
                return {"access_token": "must-not-be-saved"}

        with patch.object(adapter, "ProjectProtocolLogin", Flow), patch.object(adapter.bridge, "emit"):
            result = adapter._run_once({"email": "a@example.com", "credential_mode": "chatgpt_at", "proxy": "http://proxy.example:8080"})
        self.assertFalse(result["success"])
        self.assertEqual(result["error_code"], "chatgpt_rate_limited", result)
        self.assertEqual(len(requests), 1)
        self.assertNotIn("access_token", result)

    def test_429_creates_new_session_then_succeeds_on_same_proxy(self):
        instances, emitted = [], []
        original = adapter.ProjectProtocolLogin
        response = adapter.upstream.ProtocolResponse(429, "https://auth.openai.com/api/accounts/authorize/continue",
            {"Retry-After": "7"}, json.dumps({"error": {"code": "rate_limit_exceeded"}}))

        class Flow(original):
            def __init__(self, job, payload):
                super().__init__(job, payload)
                instances.append(self)

            def login(self):
                if len(instances) == 1:
                    with patch.object(self, "_web_session_request", return_value=response):
                        return self.authorize_continue("a@example.com")
                return {"access_token": "new-at"}

        with patch.object(adapter, "ProjectProtocolLogin", Flow), \
                patch.object(adapter.time, "sleep") as sleep, \
                patch.object(adapter.bridge, "emit", side_effect=lambda *a, **k: emitted.append((a, k))), \
                contextlib.redirect_stderr(io.StringIO()):
            result = adapter.run({"email": "a@example.com", "credential_mode": "chatgpt_at", "proxy": "http://proxy.example:8080"})
        self.assertTrue(result["success"], result)
        self.assertEqual(result["rate_limit_retries"], 1)
        self.assertEqual(result["access_token"], "new-at")
        self.assertEqual(len(instances), 2)
        self.assertIsNot(instances[0].web_session, instances[1].web_session)
        self.assertEqual(instances[0].proxy_url, instances[1].proxy_url)
        sleep.assert_called_once_with(7)
        self.assertTrue(any(a[1] == "retry_wait" for a, _ in emitted))

    def test_429_retries_are_bounded_and_non_rate_errors_are_not_retried(self):
        limited = {"success": False, "error_code": "chatgpt_rate_limited", "retryable": False, "error": "HTTP 429"}
        with patch.object(adapter, "_run_once", side_effect=lambda *a: dict(limited)) as run, \
                patch.object(adapter.time, "sleep") as sleep, patch.object(adapter.random, "randint", return_value=0), \
                patch.object(adapter.bridge, "emit"):
            result = adapter.run({"credential_mode": "chatgpt_at"})
        self.assertFalse(result["success"])
        self.assertFalse(result["retryable"])
        self.assertEqual(run.call_count, 3)
        self.assertEqual([c.args[0] for c in sleep.call_args_list], [30, 60])
        for failure in [{**limited, "retry_after_seconds": 301},
                        {"success": False, "error_code": "bad_password"},
                        {"success": False, "dead": True}]:
            with self.subTest(failure=failure), patch.object(adapter, "_run_once", return_value=failure) as run, \
                    patch.object(adapter.time, "sleep") as sleep:
                result = adapter.run({"credential_mode": "chatgpt_at"})
                self.assertFalse(result["success"])
                run.assert_called_once()
                sleep.assert_not_called()
        with patch.object(adapter, "_run_once", return_value=limited) as run, patch.object(adapter.time, "sleep") as sleep:
            adapter.run({"credential_mode": "codex_rt"})
            run.assert_called_once()
            sleep.assert_not_called()

    def test_retry_after_formats(self):
        self.assertEqual(adapter.parse_retry_after({"Retry-After": "12"}), 12)
        self.assertEqual(adapter.parse_retry_after({"retry-after": "invalid"}), 0)
        self.assertEqual(adapter.parse_retry_after({"Retry-After": "-1"}), 0)
        self.assertEqual(adapter.parse_retry_after({}), 0)
        from datetime import datetime, timezone
        with patch.object(adapter, "datetime") as clock:
            clock.now.return_value = datetime(2026, 9, 12, 0, 0, tzinfo=timezone.utc)
            self.assertEqual(adapter.parse_retry_after({"retry-after": "Sat, 12 Sep 2026 00:01:00 GMT"}), 60)

    def test_web_mode_uses_chatgpt_authorize_and_session(self):
        flow = adapter.ProjectProtocolLogin("test", {
            "email": "a@example.com",
            "credential_mode": "chatgpt_at",
            "proxy": "http://proxy.example:8080",
            "configured_login_mode": "password_totp",
            "selected_login_mode": "password_totp",
        })
        self.addCleanup(flow.close)
        authorize_url = (
            "https://auth.openai.com/authorize?state=expected-state"
            "&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fapi%2Fauth%2Fcallback%2Fopenai"
        )
        calls = []

        class Response:
            status = 200

            def __init__(self, data=None, url=""):
                self.data = data or {}
                self.url = url

            def json(self):
                return self.data

            def location(self):
                return ""

        def request(url, *, method="GET", json_data=None, form_data=None, headers=None, timeout=60,
                    allow_redirects=False):
            kwargs = {
                "method": method,
                "json_data": json_data,
                "form_data": form_data,
                "headers": headers,
                "timeout": timeout,
                "allow_redirects": allow_redirects,
            }
            calls.append((url, kwargs))
            data = {"url": authorize_url} if "/api/auth/signin/openai?" in url else {}
            return Response(data, "https://auth.openai.com/log-in/password" if url == authorize_url else url)

        with patch.object(flow, "get_csrf_token", return_value="csrf"), \
                patch.object(flow, "request", side_effect=request):
            self.assertEqual(flow.prepare_oauth_authorize_url(), authorize_url)
        self.assertEqual(flow.oauth_authorize_source, "chatgpt_web")
        self.assertEqual(flow.oauth_code_verifier, "")
        self.assertEqual([urlsplit(url).path for url, _ in calls], ["/", "/api/auth/signin/openai", "/authorize"])
        signin_url, signin_kwargs = calls[-2]
        query = parse_qs(urlsplit(signin_url).query)
        self.assertEqual(query["prompt"], ["login"])
        self.assertEqual(query["login_hint"], ["a@example.com"])
        self.assertEqual(query["ext-oai-did"], [flow.device_id])
        self.assertEqual(query["screen_hint"], ["login_or_signup"])
        self.assertEqual(query["ext-passkey-client-capabilities"], ["0111"])
        self.assertTrue(query["auth_session_logging_id"][0])
        self.assertEqual(signin_kwargs["form_data"], {
            "callbackUrl": "https://chatgpt.com/", "csrfToken": "csrf", "json": "true",
        })
        self.assertTrue(calls[0][1]["allow_redirects"])
        self.assertTrue(calls[-1][1]["allow_redirects"])

        with patch.object(flow, "get_session", return_value={"accessToken": "web-at", "user": {"email": "a@example.com"}}), \
                patch.object(flow, "get_session_cookie", return_value="web-session"):
            result = flow._chatgpt_web_session()
        self.assertEqual(result["access_token"], "web-at")
        self.assertEqual(result["session_token"], "web-session")
        self.assertNotIn("refresh_token", result)

        continue_url = "https://chatgpt.com/api/auth/callback/openai?code=code"
        with patch.object(flow, "follow_callback") as follow, \
                patch.object(flow, "_chatgpt_web_session", return_value={"access_token": "web-at"}) as session:
            self.assertEqual(flow._finish_chatgpt_login(continue_url), {"access_token": "web-at"})
        follow.assert_called_once_with(continue_url)
        session.assert_called_once_with()

    def test_codex_mode_uses_unchanged_upstream_login(self):
        flow = adapter.ProjectProtocolLogin("test", {
            "credential_mode": "codex_rt",
            "proxy": "http://proxy.example:8080",
        })
        self.addCleanup(flow.close)
        with patch.object(adapter.upstream.ChatGPTProtocolLogin, "login", return_value={"access_token": "codex-at"}) as login:
            self.assertEqual(flow.login(), {"access_token": "codex-at"})
        login.assert_called_once_with()

    def test_run_passes_global_login_selection_and_disables_sms(self):
        captured = {}

        class FakeFlow:
            def __init__(self, _job_id, payload):
                captured.update(payload)
                self.login_details = {
                    "configured_login_mode": payload["configured_login_mode"],
                    "selected_login_mode": payload["selected_login_mode"],
                    "login_method": "password_totp",
                    "login_method_label": "ChatGPT 密码 + OpenAI TOTP 登录",
                    "login_auth_status": "succeeded",
                }

            def login(self):
                return {"access_token": "web-at"}

            def close(self):
                pass

        with patch.object(adapter, "ProjectProtocolLogin", FakeFlow):
            result = adapter.run({
                "email": "a@example.com",
                "credential_mode": "chatgpt_at",
                "login_mode": "password_totp",
                "configured_login_mode": "password_totp",
                "selected_login_mode": "password_totp",
                "gpt_password": "password",
                "totp_secret": "JBSWY3DPEHPK3PXP",
                "allow_sms": True,
            })
        self.assertTrue(result["success"])
        self.assertEqual(captured["password"], "password")
        self.assertEqual(captured["_totp_secret"], "JBSWY3DPEHPK3PXP")
        self.assertEqual(captured["credential_mode"], "chatgpt_at")
        self.assertFalse(captured["allow_sms"])
        self.assertTrue(captured["skip_phone_verification"])


if __name__ == "__main__":
    unittest.main()
