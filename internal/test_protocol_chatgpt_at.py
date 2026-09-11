"""Offline contract tests for the ChatGPT temporary-AT protocol mode."""
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import adapter


class ChatGPTATFlowTests(unittest.TestCase):
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
