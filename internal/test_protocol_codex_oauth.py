import base64
import contextlib
import io
import json
import types
import unittest
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch
from urllib.parse import parse_qs, quote, urlparse

from curl_cffi.requests import Cookies, Headers

import protocol_codex_oauth as oauth


AUTH = "https://auth.openai.com/oauth/authorize?state=test-state&code_challenge=test-challenge"
CONSENT = "https://auth.openai.com/sign-in-with-chatgpt/codex/consent"
REDIRECT = "http://localhost:1455/auth/callback"
CALLBACK = REDIRECT + "?code=test-code&state=test-state"
WORKSPACE = "https://auth.openai.com/api/accounts/workspace/select"
ORGANIZATION = "https://auth.openai.com/api/accounts/organization/select"
TOKEN = "https://auth.openai.com/oauth/token"


def encoded(data):
    return base64.urlsafe_b64encode(json.dumps(data).encode()).decode().rstrip("=")


class Response:
    def __init__(self, status=200, data=None, location="", text="", url="", cookie=None):
        self.status_code = status
        self.data = data
        self.text = json.dumps(data) if data is not None else text
        self.url = url
        self.headers = Headers({"Location": location} if location else {})
        self.cookies = Cookies()
        self.cookie = cookie

    def json(self):
        if self.data is None:
            raise ValueError("not JSON")
        return self.data


class Session:
    def __init__(self, routes=(), cookie=None):
        self.routes = list(routes)
        self.calls = []
        self.cookies = Cookies()
        self.session = self
        self.closed = False
        self.proxies = {}
        if cookie:
            self.set_cookie(cookie)

    def set_cookie(self, cookie):
        value = cookie if isinstance(cookie, str) else encoded(cookie) + ".timestamp.signature"
        self.cookies.set("oai-client-auth-session", value, domain="auth.openai.com", path="/")

    def get_auth_headers(self, referer):
        return {"referer": referer, "origin": "https://auth.openai.com", "content-type": "application/json"}

    def get_auth_navigate_headers(self, referer):
        return {"referer": referer, "accept": "text/html", "upgrade-insecure-requests": "1"}

    def _attach_oai_context_headers(self, headers):
        return headers

    def request(self, method, url, **kwargs):
        self.calls.append((method, url, kwargs))
        if not self.routes:
            raise AssertionError(f"unexpected request: {method} {url}")
        expected_method, expected_url, response = self.routes.pop(0)
        if (method, url) != (expected_method, expected_url):
            raise AssertionError(f"expected {expected_method} {expected_url}; got {method} {url}")
        if isinstance(response, Exception):
            raise response
        response.url = response.url or url
        if response.cookie:
            self.set_cookie(response.cookie)
        return response

    def get(self, url, **kwargs):
        return self.request("GET", url, **kwargs)

    def post(self, url, **kwargs):
        return self.request("POST", url, **kwargs)

    def close(self):
        self.closed = True


class ConsentTests(unittest.TestCase):
    def setUp(self):
        self.events = patch.object(oauth, "emit_event").start()
        self.addCleanup(patch.stopall)

    def flow(self, session):
        return oauth.ManagerConsentFlow(session, AUTH, REDIRECT, "test-state")

    def test_missing_cookie_can_redirect_directly_to_callback(self):
        session = Session([("GET", CONSENT, Response(302, location=CALLBACK))])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        self.assertEqual(len(session.calls), 1)

    def test_consent_get_populates_cookie_before_workspace_select(self):
        session = Session([
            ("GET", CONSENT, Response(cookie={"workspaces": [{"id": "team-1"}]})),
            ("POST", WORKSPACE, Response(data={"page": {"type": "external_url"}, "continue_url": CALLBACK})),
        ])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        request = session.calls[1][2]
        self.assertEqual(json.loads(request["data"]), {"workspace_id": "team-1"})
        self.assertEqual(request["headers"]["referer"], CONSENT)
        self.assertEqual(request["timeout"], 45)
        self.assertFalse(request["allow_redirects"])

    def test_existing_cookie_does_not_skip_consent_get(self):
        session = Session([("GET", CONSENT, Response(302, location=CALLBACK))], cookie={"workspaces": [{"id": "team-1"}]})
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        self.assertEqual([call[0] for call in session.calls], ["GET"])

    def test_missing_cookie_reuses_authorize_and_pkce(self):
        session = Session([
            ("GET", CONSENT, Response()),
            ("GET", AUTH, Response(302, location="/sign-in-with-chatgpt/codex/consent")),
            ("GET", CONSENT, Response(303, location=CALLBACK)),
        ])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        self.assertTrue(any(call.args[1] == "authorize_fallback" for call in self.events.call_args_list))

    def test_no_continue_url_starts_authorize(self):
        session = Session([("GET", AUTH, Response(302, location=CALLBACK))])
        self.assertEqual(self.flow(session).resolve(""), CALLBACK)

    def test_missing_context_reports_final_route_not_missing_cookie(self):
        session = Session([("GET", CONSENT, Response()), ("GET", AUTH, Response())])
        with self.assertRaisesRegex(RuntimeError, "did not return callback code"):
            self.flow(session).resolve(CONSENT)
        self.assertEqual(len(session.calls), 2)

    def test_cookie_encoding_variants_match_manager(self):
        data = {"workspaces": [{"id": "team-1"}]}
        raw = encoded(data)
        for value in (raw, raw + ".timestamp.signature", quote('"' + raw + '.timestamp.signature"'), "." + raw + ".signature"):
            with self.subTest(value=value):
                self.assertEqual(self.flow(Session(cookie=value)).session_data(), data)

    def test_invalid_cookie_is_not_a_fatal_error(self):
        self.assertEqual(self.flow(Session(cookie="invalid%%%value")).session_data(), {})

    def test_direct_workspace_fields(self):
        for field in ("workspace_id", "workspaceId"):
            session = Session([
                ("GET", CONSENT, Response()),
                ("POST", WORKSPACE, Response(302, location=CALLBACK)),
            ], cookie={field: "team-1"})
            self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)

    def test_organization_project_is_included(self):
        org = {"id": "org-1", "projects": [{"id": "project-1"}]}
        session = Session([
            ("GET", CONSENT, Response()),
            ("POST", WORKSPACE, Response(data={"data": {"orgs": [org]}})),
            ("POST", ORGANIZATION, Response(data={"continue_url": CALLBACK})),
        ], cookie={"workspaces": [{"id": "team-1"}]})
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        self.assertEqual(json.loads(session.calls[-1][2]["data"]), {"org_id": "org-1", "project_id": "project-1"})

    def test_organization_can_come_from_cookie(self):
        session = Session([
            ("GET", CONSENT, Response()),
            ("POST", WORKSPACE, Response(data={})),
            ("POST", ORGANIZATION, Response(302, location=CALLBACK)),
        ], cookie={"workspace_id": "team-1", "orgs": [{"id": "org-1"}]})
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)
        self.assertEqual(json.loads(session.calls[-1][2]["data"]), {"org_id": "org-1"})

    def test_choose_account_uses_session_select(self):
        choose = "https://auth.openai.com/choose-an-account"
        session = Session([
            ("GET", choose, Response(text='<input value="us_test123456789012"/>')),
            ("POST", "https://auth.openai.com/api/accounts/session/select", Response(data={"continue_url": CALLBACK})),
        ])
        self.assertEqual(self.flow(session).resolve(choose), CALLBACK)
        self.assertEqual(json.loads(session.calls[-1][2]["data"]), {"session_id": "us_test123456789012"})

    def test_no_valid_organizations_uses_authorize_fallback(self):
        session = Session([
            ("GET", CONSENT, Response()),
            ("POST", WORKSPACE, Response(400, data={"error": {"code": "no_valid_organizations"}})),
            ("GET", AUTH, Response(302, location=CALLBACK)),
        ], cookie={"workspace_id": "team-1"})
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)

    def test_relative_redirects_preserve_query(self):
        session = Session([
            ("GET", CONSENT, Response(307, location="/oauth/next?key=value")),
            ("GET", "https://auth.openai.com/oauth/next?key=value", Response(308, location=CALLBACK)),
        ])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)

    def test_callback_connection_error_is_captured(self):
        session = Session([("GET", CONSENT, ConnectionError("Connection failed: " + CALLBACK))])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)

    def test_network_failure_is_not_swallowed(self):
        session = Session([("GET", CONSENT, TimeoutError("network timeout"))])
        with self.assertRaises(TimeoutError):
            self.flow(session).resolve(CONSENT)

    def test_state_mismatch_stops_before_localhost_request(self):
        session = Session([("GET", CONSENT, Response(302, location=CALLBACK.replace("test-state", "other-state")))])
        with self.assertRaisesRegex(RuntimeError, "state mismatch"):
            self.flow(session).resolve(CONSENT)
        self.assertEqual(len(session.calls), 1)

    def test_callback_error_and_missing_code_stop_locally(self):
        for value, error in ((REDIRECT + "?error=access_denied", "callback error"), (REDIRECT, "missing authorization code")):
            with self.assertRaisesRegex(RuntimeError, error):
                self.flow(Session()).resolve(value)

    def test_unrelated_code_url_is_not_callback(self):
        self.assertFalse(self.flow(Session()).is_callback("https://example.com/?code=wrong"))
        self.assertFalse(self.flow(Session()).is_callback("http://localhost.example.com:1455/auth/callback?code=wrong"))

    def test_final_response_callback_url_is_recognized(self):
        session = Session([("GET", CONSENT, Response(url=CALLBACK))])
        self.assertEqual(self.flow(session).resolve(CONSENT), CALLBACK)

    def test_redirect_cycles_are_bounded(self):
        session = Session([("GET", AUTH, Response(302, location=AUTH)) for _ in range(18)])
        with self.assertRaisesRegex(RuntimeError, "did not return callback code"):
            self.flow(session).resolve("")
        self.assertEqual(len(session.calls), 18)

    def test_explicit_dead_error_retains_stage_and_status(self):
        session = Session([("GET", CONSENT, Response(403, data={"error": {"code": "account_deactivated", "message": "Account deactivated"}}))])
        with self.assertRaises(oauth.OAuthStepError) as raised:
            self.flow(session).resolve(CONSENT)
        self.assertEqual(raised.exception.error_code, "account_deactivated")
        self.assertEqual(raised.exception.stage, "consent")
        self.assertEqual(raised.exception.http_status, 403)

    def test_ordinary_errors_are_not_dead_accounts(self):
        for status in (400, 401, 403, 429, 502):
            with self.subTest(status=status):
                error = oauth.OAuthStepError("consent", Response(status, data={"error": {"code": "temporary_error"}}))
                self.assertEqual(error.error_code, "")

    def test_token_error_description_is_retained(self):
        response = Response(400, data={"error": "invalid_grant", "error_description": "Authorization code expired"})
        error = oauth.OAuthStepError("oauth_token", response)
        self.assertIn("Authorization code expired", str(error))
        self.assertEqual(oauth.response_shape(response)["error_code"], "invalid_grant")

    def test_response_metadata_never_contains_cookie_values(self):
        session = Session(cookie={"workspace_id": "private-workspace"})
        response = Response(data={"oai-client-auth-session": "secret-session", "continue_url": CALLBACK})
        shape = oauth.response_shape(response, session)
        self.assertTrue(shape["auth_session_cookie_present"])
        self.assertEqual(shape["auth_session_body_type"], "str")
        text = json.dumps(shape)
        self.assertNotIn("secret-session", text)
        self.assertNotIn("private-workspace", text)
        self.assertNotIn("test-code", text)

    def test_independent_sessions_do_not_share_workspaces(self):
        def run(index):
            wid = "team-" + str(index)
            session = Session([
                ("GET", CONSENT, Response(cookie={"workspace_id": wid})),
                ("POST", WORKSPACE, Response(302, location=CALLBACK)),
            ])
            self.flow(session).resolve(CONSENT)
            return json.loads(session.calls[-1][2]["data"])["workspace_id"]
        with ThreadPoolExecutor(max_workers=4) as workers:
            self.assertEqual(list(workers.map(run, range(12))), ["team-" + str(i) for i in range(12)])


class FullOAuthTests(unittest.TestCase):
    def run_flow(self, token_responses, otp_statuses=(200,), consent_response=None, phone=False):
        routes = [("POST", "https://auth.openai.com/api/accounts/email-otp/resend", Response(data={}))]
        for status in otp_statuses:
            step = {"continue_url": "https://auth.openai.com/add-phone" if phone else CONSENT,
                    "page": {"type": "add_phone" if phone else "sign_in_with_chatgpt_codex_consent"},
                    "oai-client-auth-session": "not-a-cookie"}
            data = step if status == 200 else {"error": {"code": "wrong_email_otp_code"}}
            routes.append(("POST", "https://auth.openai.com/api/accounts/email-otp/validate", Response(status, data=data)))
            if status != 200:
                routes.append(("POST", "https://auth.openai.com/api/accounts/email-otp/resend", Response(data={})))
        routes.append(("GET", CONSENT, consent_response or Response(302, location=CALLBACK)))
        session = Session(routes)
        exchanges = [Session([("POST", TOKEN, response)]) for response in token_responses]
        co = types.SimpleNamespace(
            _generate_pkce=lambda: ("test-verifier", "test-challenge"), _generate_state=lambda: "test-state",
            _build_authorize_url=lambda *args, **kwargs: AUTH,
            _bootstrap_authorize=lambda *args, **kwargs: Response(url=AUTH), human_delay=lambda *args: None,
            _submit_email=lambda *args: Response(data={"page": {"payload": {"passwordless_disabled": False}}}),
            _resp_json=lambda response: response.json(), _extract_continue_url_from_step=oauth.next_auth_url,
            _needs_phone_verification=lambda *args: phone, _needs_add_phone=lambda *args: phone,
            _do_phone_verification=lambda *args: {"continue_url": CONSENT},
            _extract_code=lambda url, state: parse_qs(urlparse(url).query)["code"][0],
            _parse_id_token=lambda value: {"account_id": "team-1"},
        )
        cfg = types.SimpleNamespace(CODEX_REDIRECT_URI=REDIRECT, CODEX_TOKEN_URL=TOKEN, CODEX_CLIENT_ID="test-client")
        factory = types.SimpleNamespace(BrowserSession=lambda **kwargs: session)
        with patch.dict("sys.modules", {"core.session": factory}), patch.object(oauth.requests, "Session", side_effect=exchanges), \
                patch.object(oauth.time, "sleep"), patch.object(oauth, "emit_event"), contextlib.redirect_stderr(io.StringIO()):
            self.session, self.exchanges = session, exchanges
            return oauth.manager_style_oauth("test@example.com", "http://test-proxy:8080", lambda *args, **kwargs: "123456", cfg, co)

    def test_exported_log_401_401_200_then_no_cookie_succeeds(self):
        result = self.run_flow([Response(data={"access_token": "test-at", "refresh_token": "test-rt"})], (401, 401, 200))
        self.assertTrue(result["success"])
        self.assertEqual(result["refresh_token"], "test-rt")
        self.assertTrue(self.session.closed)
        self.assertTrue(self.exchanges[0].closed)
        request = self.exchanges[0].calls[0][2]
        self.assertEqual(request["data"]["code_verifier"], "test-verifier")
        self.assertEqual(request["data"]["code"], "test-code")
        self.assertEqual(self.exchanges[0].proxies["https"], "http://test-proxy:8080")

    def test_phone_success_uses_returned_continue_url(self):
        result = self.run_flow([Response(data={"access_token": "test-at", "refresh_token": "test-rt"})], phone=True)
        self.assertTrue(result["success"])

    def test_token_5xx_retries_then_succeeds(self):
        result = self.run_flow([Response(502, data={}), Response(data={"access_token": "test-at", "refresh_token": "test-rt"})])
        self.assertTrue(result["success"])
        self.assertTrue(all(exchange.closed for exchange in self.exchanges))

    def test_token_network_error_retries_then_succeeds(self):
        result = self.run_flow([TimeoutError("network timeout"), Response(data={"access_token": "test-at", "refresh_token": "test-rt"})])
        self.assertTrue(result["success"])
        self.assertTrue(all(exchange.closed for exchange in self.exchanges))

    def test_token_4xx_does_not_reuse_authorization_code(self):
        with self.assertRaisesRegex(oauth.OAuthStepError, "oauth_token HTTP 400"):
            self.run_flow([Response(400, data={"error": {"code": "invalid_grant"}})])
        self.assertEqual(len(self.exchanges[0].calls), 1)
        self.assertTrue(self.exchanges[0].closed)
        self.assertTrue(self.session.closed)

    def test_missing_refresh_token_is_not_success(self):
        with self.assertRaises(RuntimeError):
            self.run_flow([Response(data={"access_token": "test-at"})])

    def test_dead_during_consent_returns_structured_result(self):
        result = self.run_flow([], consent_response=Response(403, data={"error": {"code": "account_deleted"}}))
        self.assertTrue(result["dead"])
        self.assertEqual(result["stage"], "consent")
        self.assertEqual(result["http_status"], 403)
        self.assertFalse(result["success"])
        self.assertTrue(self.session.closed)


if __name__ == "__main__":
    unittest.main()
