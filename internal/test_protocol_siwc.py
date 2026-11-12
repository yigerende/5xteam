"""Run the unchanged login methods against SIWC HAR-shaped HTTP fixtures."""
import base64
import hashlib
import json
import time
import unittest
from urllib.parse import parse_qs, urlencode, urlsplit

import jwt
from cryptography.hazmat.primitives.asymmetric import rsa
from test_protocol_codex_oauth import Scenario, Response, encoded
from unittest.mock import patch
from http.cookies import SimpleCookie
from test_protocol_pro_auto import CookieStore

KEY = rsa.generate_private_key(public_exponent=65537, key_size=2048)
JWK = json.loads(jwt.algorithms.RSAAlgorithm.to_jwk(KEY.public_key()))
JWK.update(kid="fixture-key", use="sig", alg="RS256")


class SIWCScenario(Scenario):
    def consent(self, url):
        if not getattr(self,"web_login_done",False):
            return Response(url,body={"continue_url":"https://chatgpt.com/api/auth/callback/openai?code=web-code&state=web-state"})
        return Response(url, body={"continue_url": "/choose-an-account" if self.o.get("choose") else "/sign-in-with-chatgpt/consent"})

    def request(self, method, url, **kw):
        path = urlsplit(url).path
        if path == "/": return Response(url)
        if path == "/api/auth/csrf": return Response(url,body={"csrfToken":"fixture-csrf"})
        if path == "/api/auth/signin/openai": return Response(url,body={"url":"https://auth.openai.com/authorize?state=web-state"})
        if path == "/authorize": return Response(url,cookies=["login_session=private-cookie; Path=/; Secure"])
        if path == "/api/auth/callback/openai":
            self.web_login_done=True
            return Response(url,cookies=["__Secure-next-auth.session-token=private-web-cookie; Path=/; Secure"])
        if path == "/api/auth/session":
            assert getattr(self,"web_login_done",False), "web session requested before login callback"
            return Response(url,body={"accessToken":"x."+encoded({"email":"fixture@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"personal"}})+".x","user":{"email":"fixture@example.com"},"account":{"id":"personal"}})

        if path in ("/api/accounts/session/select", "/api/accounts/client_auth_session_dump", "/api/accounts/dynamic-agent-clients/consent/grant"):
            assert kw.get("headers", {}).get("openai-sentinel-token"), "HAR requires the login Sentinel token"
        if path in ("/oauth/token", "/oauth/authorize", "/api/accounts/workspace/select", "/api/accounts/organization/select"):
            raise AssertionError("SIWC used a Codex-only endpoint: " + path)
        if path == "/api/accounts/authorize":
            q = parse_qs(urlsplit(url).query)
            self.auth = q
            self.state, self.challenge = q["state"][0], q["code_challenge"][0]
            assert q["client_id"] == ["dynamic_agent_client"]
            assert q["redirect_uri"] == ["http://127.0.0.1:1456/auth/callback"]
            assert q["ext_agent_host_id"] == ["urn:uuid:fixture-host"]
            self.calls.append({"path": path, **kw})
            return Response(url, 302, location="/choose-an-account" if self.o.get("choose") else "/sign-in-with-chatgpt/consent", cookies=["auth-session-minimized=private-minimized; Path=/; Secure"])
        if path in ("/choose-an-account", "/sign-in-with-chatgpt/consent"):
            self.calls.append({"path": path, **kw})
            return Response(url, body="<html>us_fixture1234567890</html>")
        if path == "/api/accounts/session/select":
            self.calls.append({"path": path, **kw})
            assert json.loads(kw["data"])["session_id"] == "us_fixture1234567890"
            return Response(url, body={"continue_url": "/sign-in-with-chatgpt/consent"})
        if path == "/api/accounts/client_auth_session_dump":
            self.calls.append({"path": path, **kw})
            return Response(url, body={"session_id": "private-auth-session", "client_auth_session": {"email": "fixture@example.com"}})
        if path == "/api/accounts/dynamic-agent-clients/consent/grant":
            self.calls.append({"path": path, **kw})
            assert json.loads(kw["data"]) == {"auth_session_id": "private-auth-session", "agent_name": "sub2api"}
            q = {"code": "private-siwc-code", "state": self.state, "client_id": "oaiapp_fixture"}
            q.update(self.o.get("callback", {}))
            callback = "http://127.0.0.1:1456/auth/callback?" + urlencode(q)
            callback += "&state=duplicate" if self.o.get("duplicate_state") else ""
            return Response(url, body={"continue_url": callback, "page": {"type": "external_url"}})
        if path == "/api/accounts/oauth/token":
            assert kw["headers"]["Content-Type"] == "application/x-www-form-urlencoded"
            self.tokens += 1
            data = {k: v[0] for k, v in parse_qs(kw["data"].decode()).items()}
            self.calls.append({"path": path, "body": data, **kw})
            assert data["client_id"] == "oaiapp_fixture"
            assert data["resource"] == "https://api.openai.com/v1"
            assert data["redirect_uri"] == self.auth["redirect_uri"][0]
            assert base64.urlsafe_b64encode(hashlib.sha256(data["code_verifier"].encode()).digest()).decode().rstrip("=") == self.challenge
            if self.tokens <= self.o.get("token_failures", 0):
                return Response(url, self.o.get("token_status", 502), {"error": "temporary"})
            claims = {"iss": "https://auth.openai.com", "aud": "oaiapp_fixture", "exp": int(time.time()) + 3600,
                      "nonce": self.auth["nonce"][0], "email": "fixture@example.com", "sub": "fixture-subject"}
            claims.update(self.o.get("claims", {}))
            signing_key = rsa.generate_private_key(public_exponent=65537, key_size=2048) if self.o.get("bad_signature") else KEY
            tokens = {"access_token": "private-siwc-at", "refresh_token": "private-siwc-rt", "expires_in": 3600,
                      "id_token": jwt.encode(claims, signing_key, algorithm="RS256", headers={"kid": "fixture-key"}),
                      "scope": self.o.get("scope", self.auth["scope"][0])}
            tokens.pop(self.o.get("missing_token", ""), None)
            return Response(url, body=tokens)
        if path == "/.well-known/openid-configuration":
            self.calls.append({"path": path, **kw})
            return Response(url, body={"issuer": "https://auth.openai.com", "jwks_uri": self.o.get("jwks_uri", "https://auth.openai.com/.well-known/jwks.json")})
        if path == "/.well-known/jwks.json":
            self.calls.append({"path": path, **kw})
            return Response(url, body={"keys": [JWK]})
        return super().request(method, url, **kw)

    def run(self, **payload):
        scenario=self
        self.clients=[]
        class Session:
            def __init__(self,**kwargs):
                self.options=kwargs;self.cookies=CookieStore();self.closed=False;scenario.clients.append(self)
            def request(self,method,url,**kwargs):
                assert not self.closed
                kwargs.update(impersonate=self.options.get("impersonate"),proxies=self.options.get("proxies"))
                r=scenario.request(method,url,**kwargs)
                for raw in r.headers.get_list("set-cookie"):
                    jar=SimpleCookie();jar.load(raw)
                    for name,value in jar.items(): self.cookies.set(name,value.value,urlsplit(url).hostname,"/")
                return r
            def close(self): self.closed=True
        with patch("curl_cffi.requests.Session",Session):
            result=super().run(credential_mode="siwc", siwc_host_id="urn:uuid:fixture-host", **payload)
        assert len(self.clients)==1 and self.clients[0].closed
        return result


class SIWCFlowTests(unittest.TestCase):
    def test_full_login_paths_use_same_engine_and_proxy(self):
        for options, payload in [({}, {}), ({"choose": True}, {}),
                                 ({"password": True, "totp": True}, {"login_mode": "password_totp", "gpt_password": "private-password", "totp_secret": "JBSWY3DPEHPK3PXP"}),
                                 ({"email_mfa": True}, {"totp_secret": "JBSWY3DPEHPK3PXP"})]:
            with self.subTest(options=options):
                s = SIWCScenario(**options)
                result = s.run(**payload)
                self.assertTrue(result["success"], result)
                self.assertEqual(result["access_token"], "private-siwc-at")
                self.assertEqual(result["refresh_token"], "private-siwc-rt")
                self.assertEqual(result["client_id"], "oaiapp_fixture")
                self.assertTrue(result["plan_use_enabled"])
                self.assertEqual(s.tokens, 1)
                for c in s.calls:
                    self.assertIn("proxy.example", str(c.get("proxies")))
                for secret in ["private-siwc-at", "private-siwc-rt", "private-auth-session", "private-password", "private-siwc-code"]:
                    self.assertNotIn(secret, s.logs.getvalue())

    def test_remote_interface_reuses_login_session_and_never_exchanges_locally(self):
        from manager_oauth import bridge
        from urllib.parse import urlencode
        calls = []
        def rpc(method, **params):
            calls.append(method)
            if method == "siwc_generate":
                return {"auth_url": "https://auth.openai.com/api/accounts/authorize?" + urlencode({
                    "client_id": "dynamic_agent_client", "response_type": "code",
                    "redirect_uri": "http://127.0.0.1:1456/auth/callback", "resource": "https://api.openai.com/v1",
                    "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct",
                    "state": "remote-state", "nonce": "remote-nonce", "code_challenge": "remote-pkce",
                    "code_challenge_method": "S256", "ext_agent_host_id": "urn:uuid:fixture-host"})}
            if method == "siwc_callback":
                self.assertIn("private-siwc-code", params["callback_url"])
                self.assertTrue(params["web_session"].get("access_token"))
                return {"access_token": "remote-at", "refresh_token": "remote-rt", "id_token": "remote-id",
                        "auth_mode": "siwc", "email": "fixture@example.com", "client_id": "oaiapp_fixture"}
            raise AssertionError(method)
        with patch.object(bridge, "call", rpc):
            scenario = SIWCScenario(password=True, totp=True)
            result = scenario.run(siwc_authorization_source="sub2api", login_mode="password_totp",
                                  gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP")
        self.assertTrue(result["success"], result)
        self.assertEqual(calls, ["siwc_generate", "siwc_callback"])
        self.assertEqual(scenario.tokens, 0)
        self.assertEqual(len(scenario.clients), 1)
        for secret in ("remote-at", "remote-rt", "remote-state", "private-siwc-code"):
            self.assertNotIn(secret, scenario.logs.getvalue())

    def test_callback_validation_precedes_exchange(self):
        for opts in [{"callback": {"state": "wrong"}}, {"callback": {"code": ""}},
                     {"callback": {"client_id": "dynamic_agent_client"}}, {"callback": {"error": "access_denied"}}, {"duplicate_state": True}]:
            with self.subTest(opts=opts):
                s = SIWCScenario(**opts)
                self.assertFalse(s.run()["success"])
                self.assertEqual(s.tokens, 0)

    def test_signed_claims_are_verified_before_accepting_tokens(self):
        for opts in [{"claims": {"nonce": "wrong"}}, {"claims": {"aud": "other-client"}},
                     {"claims": {"iss": "https://wrong.example"}}, {"claims": {"email": "other@example.com"}},
                     {"claims": {"exp": int(time.time()) - 400}}, {"bad_signature": True},
                     {"missing_token": "refresh_token"}, {"missing_token": "id_token"}, {"jwks_uri": "http://127.0.0.1/keys"}]:
            with self.subTest(opts=opts):
                self.assertFalse(SIWCScenario(**opts).run()["success"])

    def test_exchange_retries_only_transient_failures(self):
        for status, failures, expected in [(502, 2, True), (502, 3, False), (400, 2, False)]:
            s = SIWCScenario(token_status=status, token_failures=failures)
            self.assertEqual(s.run()["success"], expected)
            self.assertEqual(s.tokens, 1 if status == 400 else 3)

    def test_missing_plan_scope_is_not_reported_as_authorized(self):
        result = SIWCScenario(scope="openid email offline_access").run()
        self.assertTrue(result["success"])
        self.assertFalse(result["plan_use_enabled"])


if __name__ == "__main__":
    unittest.main()
