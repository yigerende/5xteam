"""SIWC email continuation uses the existing mailbox parser and the live client."""
import json
import unittest
from urllib.parse import urlsplit
from unittest.mock import patch

from test_protocol_siwc_identity import IdentityScenario, SIWCIdentityTests, Response
from manager_oauth import bridge, upstream


class EmailScenario(IdentityScenario):
    def __init__(self, *, delivery="send", mail_format="html", **options):
        super().__init__(**options)
        self.delivery, self.mail_format = delivery, mail_format
        self.sent = self.email_verified = False
        self.email_calls, self.mail_reads = [], []

    def email_step(self, url):
        if self.delivery in {"send", "send_page", "send_json"}:
            return Response(url, body={"page": {"type": "email_otp_send"},
                                       **({} if self.delivery == "send_page" else {"continue_url": "/api/accounts/email-otp/send"})})
        self.sent = True
        return Response(url, body={"page": {"type": "email_otp_verification"},
                                   **({} if self.delivery == "page" else {"continue_url": "/email-verification"})})

    def next_identity(self, url):
        if not self.email_verified:
            return self.email_step(url)
        return super().next_identity(url)

    def request(self, method, url, **kw):
        path = urlsplit(url).path
        if path in {"/api/accounts/email-otp/send", "/email-verification", "/api/accounts/email-otp/validate"}:
            self.email_calls.append(path)
            assert len(self.clients) == 1 and not self.clients[0].closed
            assert "proxy.example" in str(kw.get("proxies"))
            assert "private-web-cookie" in [c.value for c in self.clients[0].cookies.jar]
            if path.endswith("/send"):
                assert not self.sent, "duplicate email send"
                self.sent = True
                if self.o.get("send_loop"):
                    return Response(url, 302, location="/api/accounts/email-otp/send")
                if self.delivery == "send_json":
                    return Response(url, body={"page": {"type": "email_otp_verification"}})
                return Response(url, 302, location="/email-verification")
            if path == "/email-verification":
                return Response(url, body="<html>Email verification</html>")
            assert json.loads(kw["data"]) == {"code": "345678"}
            sentinel = json.loads(kw["headers"]["openai-sentinel-token"])
            assert sentinel["flow"] == "email_verification"
            assert sentinel["id"] == self.sentinel_requests[0]["id"]
            if self.o.get("invalid_code"):
                return Response(url, 401, {"error": {"code": "invalid_code"}})
            if self.o.get("repeat_email"):
                return Response(url, body={"page": {"type": "email_otp_verification"}})
            self.email_verified = True
            return self.next_identity(url)
        result = super().request(method, url, **kw)
        if path == "/api/accounts/session/select" and self.identity_challenge == "email" and not self.email_verified:
            return self.email_step(url)
        return result

    def mail(self, url, **kw):
        self.mail_reads.append(self.sent)
        assert url == "https://mail.example/pickup/private"
        if self.o.get("empty_mail"):
            return "No messages"
        # First poll may still be a cached old message; reuse the real parser.
        code = "345678" if self.sent and (not self.o.get("cached_once") or self.mail_reads.count(True) > 1) else "111111"
        if self.mail_format == "json":
            return json.dumps({"code": code})
        if self.mail_format == "text":
            return "Your OpenAI verification code is " + code
        return "<html><p>Your OpenAI verification code is <b>" + code + "</b></p></html>"


class SIWCEmailTests(unittest.TestCase):
    def run_scenario(self, scenario, source, **payload):
        calls = []
        with patch.object(bridge, "call", SIWCIdentityTests().remote(calls)):
            result = scenario.run(siwc_authorization_source=source, login_mode="password_totp",
                                  gpt_password="private-password", totp_secret="JBSWY3DPEHPK3PXP", **payload)
        return result, calls

    def test_email_after_any_identity_challenge_preserves_transaction(self):
        for source in ("local", "sub2api"):
            for challenge in ("email", "password", "password_totp", "totp"):
                for delivery in ("send", "send_page", "send_json", "page", "url"):
                    with self.subTest(source=source, challenge=challenge, delivery=delivery):
                        s = EmailScenario(challenge=challenge, delivery=delivery)
                        result, calls = self.run_scenario(s, source)
                        self.assertTrue(result["success"], result)
                        self.assertEqual(s.identifiers, 1)
                        self.assertEqual(len(set(s.authorizations)), 1)
                        self.assertEqual(s.email_calls.count("/api/accounts/email-otp/validate"), 1)
                        self.assertEqual(s.email_calls.count("/api/accounts/email-otp/send"), int(delivery.startswith("send")))
                        self.assertEqual(calls, ["siwc_generate", "siwc_callback"] if source == "sub2api" else [])
                        for secret in ("345678", "111111", "private-password", "private-web-cookie", "pickup/private"):
                            self.assertNotIn(secret, s.logs.getvalue())

    def test_existing_parser_formats_and_post_email_navigation(self):
        for source in ("local", "sub2api"):
            for fmt in ("html", "text", "json"):
                for after in ("consent_page", "callback", "reopen", "web"):
                    with self.subTest(source=source, fmt=fmt, after=after):
                        s = EmailScenario(challenge="password_totp", mail_format=fmt, after=after, cached_once=True)
                        result, _ = self.run_scenario(s, source)
                        self.assertTrue(result["success"], result)
                        self.assertEqual(s.mail_reads, [False, True, True], "old pre-send code must be skipped")
                        self.assertEqual(len(set(s.authorizations)), 1)

    def test_missing_link_timeout_invalid_code_and_repeated_challenge_are_bounded(self):
        for source in ("local", "sub2api"):
            for options, payload, expected in (
                ({}, {"pickup_url": ""}, "siwc_email_pickup_missing"),
                ({"empty_mail": True}, {}, "siwc_email_code_timeout"),
                ({"invalid_code": True}, {}, None),
                ({"repeat_email": True}, {}, "siwc_session_expired"),
                ({"send_loop": True}, {}, "siwc_session_expired"),
            ):
                with self.subTest(source=source, options=options, payload=payload):
                    s = EmailScenario(challenge="password_totp", **options)
                    result, calls = self.run_scenario(s, source, **payload)
                    self.assertFalse(result["success"], result)
                    if expected:
                        self.assertEqual(result["error_code"], expected)
                    self.assertNotIn("siwc_callback", calls)
                    self.assertEqual(s.tokens, 0)
                    self.assertLessEqual(len(s.mail_reads), 4)
                    self.assertLessEqual(s.email_calls.count("/api/accounts/email-otp/validate"), 1)
                    self.assertLessEqual(s.email_calls.count("/api/accounts/email-otp/send"), 1)


if __name__ == "__main__":
    unittest.main()
