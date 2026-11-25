"""Continuation fixtures: login succeeded, but SIWC asks for fresh verification."""
import json
import unittest
from urllib.parse import parse_qs, urlencode, urlsplit
from unittest.mock import patch

from test_protocol_siwc import SIWCScenario, Response
from manager_oauth import bridge
from manager_oauth.siwc import AUTHORIZE, REDIRECT, RESOURCE, SCOPES


class IdentityScenario(SIWCScenario):
    def __init__(self, *, challenge="password", after="consent", **options):
        super().__init__(password=True, totp=True, choose=True, **options)
        self.identity_challenge, self.after = challenge, after
        self.identity_calls, self.authorizations = [], []
        self.verified = False

    def next_identity(self, url):
        if self.after == "repeat":
            return Response(url, body={"page": {"type": "login_password"}, "continue_url": "/log-in/password"})
        self.verified = True
        if self.after == "reopen":
            return Response(url, body={})
        if self.after == "web":
            return Response(url, body={"continue_url": "https://chatgpt.com/"})
        if self.after == "consent_page":
            return Response(url, body={"page": {"type": "sign_in_with_chatgpt_consent"}})
        if self.after == "callback":
            return Response(url, body={"continue_url": REDIRECT + "?" + urlencode({
                "state": self.state, "code": "private-siwc-code", "client_id": "oaiapp_fixture"})})
        return Response(url, body={"page": {"type": "sign_in_with_chatgpt_consent"},
                                   "continue_url": "/sign-in-with-chatgpt/consent"})

    def request(self, method, url, **kw):
        path = urlsplit(url).path
        if path == "/api/accounts/authorize":
            self.authorizations.append(url)
        if path == "/api/accounts/session/select":
            response = super().request(method, url, **kw)
            if self.verified:
                return response
            if self.identity_challenge == "expired":
                return Response(url, 401, {"error": {"code": "session_expired"}})
            if self.identity_challenge == "login":
                return Response(url, body={"continue_url": "/log-in"})
            if self.identity_challenge == "totp":
                return self.mfa(url)
            return Response(url, body={"page": {"type": "login_password"}, "continue_url": "/log-in/password"})
        if self.authorizations and path in {"/api/accounts/password/verify", "/api/accounts/mfa/verify"}:
            self.identity_calls.append(path)
            assert len(self.clients) == 1 and not self.clients[0].closed
            assert "proxy.example" in str(kw.get("proxies"))
            assert "private-web-cookie" in [c.value for c in self.clients[0].cookies.jar]
            if path.endswith("password/verify"):
                assert json.loads(kw["data"]) == {"password": "private-password"}
                sentinel = json.loads(kw["headers"]["openai-sentinel-token"])
                assert sentinel["flow"] == "password_verify"
                assert sentinel["id"] == self.sentinel_requests[0]["id"]
                if self.identity_challenge == "wrong_password":
                    return Response(url, 401, {"error": {"code": "invalid_password"}})
                if self.identity_challenge == "password_totp":
                    return self.mfa(url)
            else:
                assert json.loads(kw["data"])["id"] == "factor-1"
            return self.next_identity(url)
        return super().request(method, url, **kw)


class SIWCIdentityTests(unittest.TestCase):
    def remote(self, calls):
        def rpc(method, **params):
            calls.append(method)
            if method == "siwc_generate":
                return {"auth_url": AUTHORIZE + "?" + urlencode({
                    "client_id": "dynamic_agent_client", "response_type": "code", "redirect_uri": REDIRECT,
                    "resource": RESOURCE, "scope": SCOPES, "state": "private-state", "nonce": "private-nonce",
                    "code_challenge": "private-pkce", "code_challenge_method": "S256",
                    "ext_agent_host_id": "urn:uuid:fixture-host"})}
            if method == "siwc_callback":
                assert parse_qs(urlsplit(params["callback_url"]).query)["state"] == ["private-state"]
                return {"access_token": "remote-at", "refresh_token": "remote-rt", "id_token": "remote-id",
                        "auth_mode": "siwc", "email": "fixture@example.com", "client_id": "oaiapp_fixture"}
            raise AssertionError(method)
        return rpc

    def run_scenario(self, s, source, calls):
        with patch.object(bridge, "call", self.remote(calls)):
            return s.run(siwc_authorization_source=source, login_mode="password_totp",
                         gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")

    def test_returned_challenges_and_navigation_local_and_remote(self):
        for source in ("local", "sub2api"):
            for challenge in ("password", "password_totp", "totp"):
                for after in ("consent", "consent_page", "callback", "reopen", "web"):
                    with self.subTest(source=source, challenge=challenge, after=after):
                        calls = []
                        s = IdentityScenario(challenge=challenge, after=after)
                        result = self.run_scenario(s, source, calls)
                        self.assertTrue(result["success"], result)
                        self.assertEqual(s.identifiers, 1, "identity continuation started a fresh login")
                        self.assertEqual(len(s.identity_calls), 2 if challenge == "password_totp" else 1)
                        self.assertEqual(len(set(s.authorizations)), 1, "state/PKCE/nonce changed")
                        self.assertEqual(len(s.authorizations), 2 if after in {"reopen", "web"} else 1)
                        self.assertEqual(calls, ["siwc_generate", "siwc_callback"] if source == "sub2api" else [])
                        self.assertEqual(s.tokens, 0 if source == "sub2api" else 1)
                        self.assertIn("正在补充身份验证", s.logs.getvalue())
                        for secret in ("private-password", "private-state", "private-pkce", "private-siwc-code"):
                            self.assertNotIn(secret, s.logs.getvalue())

    def test_expiry_is_distinct_from_invalid_credentials_and_challenge_loops_are_bounded(self):
        for source in ("local", "sub2api"):
            for challenge, after, expired, writes in (
                    ("expired", "consent", True, 0), ("login", "consent", True, 0),
                    ("password", "repeat", True, 1), ("wrong_password", "consent", False, 1)):
                with self.subTest(source=source, challenge=challenge, after=after):
                    calls = []
                    s = IdentityScenario(challenge=challenge, after=after)
                    result = self.run_scenario(s, source, calls)
                    self.assertFalse(result["success"], result)
                    self.assertEqual(result["error_code"] == "siwc_session_expired", expired, result)
                    self.assertEqual(len(s.identity_calls), writes)
                    self.assertNotIn("siwc_callback", calls)
                    self.assertEqual(s.tokens, 0)

    def test_pro_uses_configured_retry_instead_of_hidden_token_retries(self):
        from manager_oauth.siwc import SIWCProtocolLogin
        s=IdentityScenario(token_failures=1)
        original=SIWCProtocolLogin.continue_authenticated.__func__
        def pro_continue(cls,flow,web=None):
            flow.payload["pro_oauth_retry_managed"]=True
            return original(cls,flow,web)
        with patch.object(SIWCProtocolLogin,"continue_authenticated",classmethod(pro_continue)):
            result=s.run(login_mode="password_totp",gpt_password="private-password",
                         totp_secret="JBSWY3DPEHPK3PXP")
        self.assertFalse(result["success"])
        self.assertEqual(s.tokens,1)


if __name__ == "__main__":
    unittest.main()
