"""Offline contract tests for the ChatGPT temporary-AT protocol mode."""
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import adapter


class ChatGPTATFlowTests(unittest.TestCase):
    def test_web_mode_uses_chatgpt_authorize_and_session(self):
        flow = adapter.ProjectProtocolLogin("test", {
            "credential_mode": "chatgpt_at",
            "proxy": "http://proxy.example:8080",
            "configured_login_mode": "password_totp",
            "selected_login_mode": "password_totp",
        })
        authorize_url = (
            "https://auth.openai.com/authorize?state=expected-state"
            "&redirect_uri=https%3A%2F%2Fchatgpt.com%2Fapi%2Fauth%2Fcallback%2Fopenai"
        )
        with patch.object(flow, "get_csrf_token", return_value="csrf"), \
                patch.object(flow, "signin_openai", return_value=authorize_url):
            self.assertEqual(flow.prepare_oauth_authorize_url(), authorize_url)
        self.assertEqual(flow.oauth_authorize_source, "chatgpt_web")
        self.assertEqual(flow.oauth_code_verifier, "")

        callback = "https://chatgpt.com/api/auth/callback/openai?code=code&state=expected-state"
        with patch.object(flow, "follow_callback") as follow, \
                patch.object(flow, "get_session", return_value={"accessToken": "web-at", "user": {"email": "a@example.com"}}), \
                patch.object(flow, "get_session_cookie", return_value="web-session"):
            result = flow.exchange_oauth_callback(callback)
        follow.assert_called_once_with(callback)
        self.assertEqual(result["access_token"], "web-at")
        self.assertEqual(result["session_token"], "web-session")
        self.assertNotIn("refresh_token", result)

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
