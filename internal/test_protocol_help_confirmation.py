"""Offline Help contracts from the supplied HAR; no network or real messages."""
import json
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch
from urllib.parse import urlencode, urlsplit, parse_qs

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import adapter, help_confirmation as help

EMAIL = "child@example.com"
PROMPT = "请查询退款；最后一行输入已成功或已失败"
ANSWER = "该订阅退款已发起，预计 5–10 个工作日。\n已成功"


def help_page(email=EMAIL):
    table = [{"_1": 2}, "loaderData", {"_3": 4}, "root",
             {"_5": 6, "_11": 12, "_13": 14}, "user", {"_7": 8, "_9": 10},
             "email", email, "accessToken", "help-only-token",
             "sessionIdToken", "signed-help-session", "helpCenterSessionId", "help-session-id"]
    return '<script>window.__reactRouterContext.streamController.enqueue(' + json.dumps(json.dumps(table) + '\n') + ');</script>'


def response(status=200, body=None, headers=None, text=""):
    r = Mock(status_code=status, headers=headers or {}, text=text, url="")
    r.json.return_value = body or {}
    return r


def item(kind, id, text, thread="thread-1"):
    return {"type": kind, "id": id, "thread_id": thread, "created_at": "2026-10-26T10:00:00Z",
            "content": [{"type": "input_text" if kind == "user_message" else "output_text", "text": text}]}


class HelpTests(unittest.TestCase):
    def setUp(self):
        # Any unmocked HTTP call fails immediately.
        self.network = patch("curl_cffi.requests.Session.request", side_effect=AssertionError("network forbidden"))
        self.network.start(); self.addCleanup(self.network.stop)
        self.flow = adapter.ProjectProtocolLogin("test", {"email": EMAIL, "password": "fixture-password",
            "_totp_secret": "fixture-totp", "credential_mode": "chatgpt_at", "proxy": "http://proxy.invalid:8000"})
        self.addCleanup(self.flow.close)
        self.flow.login = Mock(return_value={"access_token": "private-login-token", "success": True})
        self.calls = []
        self.rpc = patch.object(help.bridge, "call", side_effect=lambda name, **v: self.calls.append((name, v)) or True)
        self.rpc.start(); self.addCleanup(self.rpc.stop)
        self.inquiry = help.HelpInquiry(self.flow, {"request_id": "test-request", "prompt": PROMPT})

    def events(self, *events):
        r = response(headers={"content-type": "text/event-stream"})
        r.iter_lines.return_value = [l for e in events for l in ("data: " + json.dumps(e), "")]
        return r

    def stream(self, *extra):
        return self.events({"type": "thread.created", "thread": {"id": "thread-1"}},
            {"type": "thread.item.done", "item": item("user_message", "user-1", PROMPT)}, *extra)

    def test_full_login_authorize_and_message_same_proxy_session(self):
        auth = help.AUTH + "/oauth/authorize?" + urlencode({"redirect_uri": help.HELP + "/auth/callback", "state": "test-state"})
        callback = help.HELP + "/auth/callback?code=test-code&state=test-state"
        self.flow.sentinel_token = "fixture-sentinel"
        seen = []
        def request(method, url, **kw):
            seen.append((method, url, kw))
            self.assertFalse(kw["allow_redirects"])
            self.assertTrue(kw["default_headers"])
            self.assertNotIn("User-Agent", kw["headers"])
            self.assertNotIn("sec-ch-ua", kw["headers"])
            self.assertEqual(len({k.lower() for k in kw["headers"]}), len(kw["headers"]))
            if "/auth/login?" in url:
                landing = parse_qs(urlsplit(url).query)["redirectTo"][0]
                self.assertEqual(urlsplit(landing).path, "/auth/login-success")
                self.assertEqual(parse_qs(urlsplit(landing).query)["returnTo"], [urlsplit(help.PAGE).path])
                return response(302, headers={"location": auth})
            if url == auth: return response(302, headers={"location": help.AUTH + "/choose-an-account"})
            if url.endswith("/choose-an-account"):
                return response(text='<script>us_wrong</script><button name="session_id" value="us_other">other@example.com</button><button name="session_id" value="us_child"><div>Example Name</div><span>'+EMAIL+'</span></button>')
            if url.endswith("/api/accounts/session/select"):
                self.assertEqual(json.loads(kw["data"]), {"session_id": "us_child"})
                self.assertEqual(kw["headers"]["openai-sentinel-token"], "fixture-sentinel")
                return response(body={"continue_url": callback}, text=json.dumps({"continue_url": callback}))
            if url == callback: return response(302, headers={"location": help.HELP + "/auth/login-success?returnTo=%2Fen%2Fcollections%2F3742473-chatgpt"})
            if "/auth/login-success?" in url: return response(302, headers={"location": help.PAGE})
            if url == help.PAGE: return response(text=help_page())
            if url.endswith("/me"):
                self.assertEqual(kw["headers"]["Authorization"], "Bearer help-only-token")
                self.assertEqual(kw["headers"]["x-session-id"], "signed-help-session")
                return response(body={"email": EMAIL})
            if url == help.CHAT:
                self.assertIn("help_sending", [n for n, _ in self.calls])
                self.assertEqual(kw["json"]["type"], "threads.create")
                self.assertEqual(kw["headers"]["x-chatkit-version"], "rc2")
                self.assertEqual(kw["headers"]["Authorization"], "Bearer help-only-token")
                self.assertEqual(kw["headers"]["x-help-center-session-id"], "help-session-id")
                self.assertTrue(kw["stream"])
                return self.stream({"type": "thread.item.done", "item": item("assistant_message", "answer-1", ANSWER)})
            self.fail("unexpected URL " + url)
        with patch.object(self.flow.web_session, "request", side_effect=request):
            result = self.inquiry.run()
        self.assertTrue(result["success"]); self.flow.login.assert_called_once()
        self.assertEqual(sum(url == help.CHAT for _, url, _ in seen), 1)
        saved = next(v["session"] for n, v in self.calls if n == "help_session")
        self.assertEqual(saved["proxy"], self.flow.proxy_url)
        self.assertEqual(saved["access_token"], "help-only-token")
        self.assertEqual(saved["session_id_token"], "signed-help-session")
        self.assertEqual([v["answer"] for n, v in self.calls if n == "help_reply"], [ANSWER])

    def test_user_prompt_deltas_and_wrong_thread_cannot_confirm(self):
        for extra in [[], [{"type": "thread.item.updated", "update": {"text": ANSWER}}],
                      [{"type": "thread.item.done", "item": item("assistant_message", "answer", ANSWER, "wrong")}]]:
            self.calls.clear(); self.inquiry.verified = True
            with patch.object(self.flow.web_session, "request", return_value=self.stream(*extra)) as req:
                with self.assertRaises(help.HelpError): self.inquiry.send()
            req.assert_called_once(); self.assertNotIn("help_reply", [n for n, _ in self.calls])

    def test_final_complete_assistant_and_stream_error(self):
        self.inquiry.verified = True
        events = [{"type": "thread.item.done", "item": item("assistant_message", "first", "稍等")},
                  {"type": "thread.item.done", "item": item("assistant_message", "last", ANSWER)}]
        with patch.object(self.flow.web_session, "request", return_value=self.stream(*events)):
            self.inquiry.send()
        self.assertEqual([v["message_id"] for n, v in self.calls if n == "help_reply"], ["last"])
        self.calls.clear()
        with patch.object(self.flow.web_session, "request", return_value=self.stream(*events, {"type": "error"})):
            with self.assertRaises(help.HelpError): self.inquiry.send()
        self.assertNotIn("help_reply", [n for n, _ in self.calls])

    def test_send_checkpoint_failure_never_posts(self):
        self.inquiry.verified = True
        with patch.object(help.bridge, "call", side_effect=RuntimeError("stopped")), patch.object(self.flow.web_session, "request") as req:
            with self.assertRaises(RuntimeError): self.inquiry.send()
        req.assert_not_called()

    def test_lost_response_only_recovers_never_reposts(self):
        self.inquiry.verified = True
        with patch.object(self.flow.web_session, "request", side_effect=TimeoutError("lost")) as req:
            with self.assertRaises(TimeoutError): self.inquiry.send()
        req.assert_called_once()
        saved = {"email": EMAIL, "proxy": self.flow.proxy_url, "access_token": "saved-help-token", "session_id_token": "saved-signed-session", "cookies": [{"name": "session", "value": "private", "domain": ".openai.com"}]}
        f = help.HelpInquiry(self.flow, {"session": saved, "sent_at": "2026-10-26T10:00:00Z", "request_id": "original", "prompt": PROMPT})
        queries = []
        def request(method, url, **kw):
            if url.endswith("/me"):
                self.assertEqual(kw["headers"]["Authorization"], "Bearer saved-help-token")
                return response(body={"email": EMAIL})
            typ = kw["json"]["type"]; queries.append(typ)
            if typ == "threads.list": return response(body={"data": [{"id": "thread-1"}]})
            self.assertEqual(typ, "threads.get_by_id")
            return response(body={"id": "thread-1", "items": {"data": [item("user_message", "user-1", PROMPT), item("assistant_message", "answer-1", ANSWER)]}})
        with patch.object(self.flow.web_session, "request", side_effect=request): self.assertTrue(f.run()["success"])
        self.assertEqual(queries, ["threads.list", "threads.get_by_id"]); self.flow.login.assert_not_called()

    def test_unknown_request_does_not_match_old_or_other_user(self):
        self.inquiry.verified = True; self.inquiry.sent_at = help.parse_time("2026-10-26T11:00:00Z")
        with patch.object(self.inquiry, "query", return_value={"data": [{"id": "thread-1"}]}), patch.object(self.inquiry, "thread_items", return_value=[item("user_message", "old", PROMPT), item("assistant_message", "answer", ANSWER)]):
            with self.assertRaisesRegex(help.HelpError, "结果不明"): self.inquiry.recover()
        self.assertNotIn("help_reply", [n for n, _ in self.calls])

    def test_identity_callback_and_host_validation(self):
        for url in ["http://help.openai.com/", "https://help.openai.com.evil.invalid/", "https://user:pass@help.openai.com/", "https://help.openai.com:8000/"]:
            with self.assertRaises(help.HelpError): self.inquiry.request(url)
        with patch.object(self.flow.web_session, "request", return_value=response(body={"email": "other@example.com"})):
            with self.assertRaisesRegex(help.HelpError, "账号"): self.inquiry.identity()
        with patch.object(self.flow.web_session, "request", side_effect=[response(), response(302, headers={"location": help.HELP + "/auth/callback?code=x&state=wrong"})]) as req:
            with self.assertRaisesRegex(help.HelpError, "回调校验"): self.inquiry.authorize()
        self.assertEqual(req.call_count, 2)

    def test_deletion_prepare_failure_preserves_login_but_never_sends(self):
        self.inquiry.config["prepare_only"] = True
        with patch.object(self.inquiry, "authorize", side_effect=help.HelpError("401")), patch.object(self.inquiry, "send") as send:
            result = self.inquiry.run()
        self.assertEqual(result["access_token"], "private-login-token"); send.assert_not_called()
        self.assertIn("help_prepare_failed", [n for n, _ in self.calls])

    def test_session_restore_pin_and_expiration(self):
        self.inquiry.config["session"] = {"email": EMAIL, "proxy": "http://other.invalid", "cookies": [1]}
        self.assertFalse(self.inquiry.restore())
        self.inquiry.config["session"] = {"email": EMAIL, "proxy": self.flow.proxy_url, "cookies": [{"name": "s", "value": "expired", "domain": ".openai.com"}]}
        with patch.object(self.flow.web_session, "request", return_value=response(401)): self.assertFalse(self.inquiry.restore())

    def test_loader_requires_root_identity_and_never_executes_scripts(self):
        self.assertEqual(help.help_page_identity(help_page())["access_token"], "help-only-token")
        for html in ['<script>throw new Error("execute")</script>', help_page().replace('loaderData', 'untrustedData'), help_page().replace('accessToken', 'unusedToken')]:
            with self.assertRaises(help.HelpError): help.help_page_identity(html)
        with self.assertRaisesRegex(help.HelpError, "账号"):
            self.inquiry.load_identity(help_page("other@example.com"))
        self.assertEqual(self.inquiry.access_token, "")

    def test_unauthenticated_help_landing_never_sends(self):
        with patch.object(self.flow.web_session, "request", return_value=response(text='<html>Login</html>')) as req:
            with self.assertRaisesRegex(help.HelpError, "授权凭据"): self.inquiry.run()
        self.assertNotIn("help_sending", [n for n, _ in self.calls])
        self.assertFalse(any("/chatkit" in c.args[1] for c in req.call_args_list))

    def test_help_page_redirects_continue_same_session_then_authorize_once(self):
        for status in sorted(help.REDIRECTS):
            with self.subTest(status=status):
                self.calls.clear()
                self.inquiry = help.HelpInquiry(self.flow, {"request_id": "test-request", "prompt": PROMPT})
                auth = help.AUTH + "/oauth/authorize?" + urlencode({
                    "redirect_uri": help.HELP + "/auth/callback", "state": "redirect-state"})
                callback = help.HELP + "/auth/callback?code=redirect-code&state=redirect-state"
                responses = [response(status, headers={"location": "/en/"}), response(),
                    response(302, headers={"location": auth}), response(302, headers={"location": callback}),
                    response(302, headers={"location": "/auth/login-success?returnTo=%2Fen%2F"}),
                    response(302, headers={"location": help.PAGE + "?loginSuccess=true"}),
                    response(text=help_page()), response(body={"email": EMAIL}),
                    self.stream({"type": "thread.item.done", "item": item("assistant_message", "answer", ANSWER)})]
                with patch.object(self.flow.web_session, "request", side_effect=responses) as req:
                    self.assertTrue(self.inquiry.run()["success"])
                urls = [c.args[1] for c in req.call_args_list]
                self.assertEqual(urls[:2], [help.PAGE, help.HELP + "/en/"])
                self.assertEqual(sum("/auth/login?" in url for url in urls), 1)
                self.assertEqual(urls.count(callback), 1)
                self.assertEqual(urls.count(help.CHAT), 1)
                self.assertTrue(all(not c.kwargs["allow_redirects"] for c in req.call_args_list))
                saved = next(v["session"] for n, v in self.calls if n == "help_session")
                self.assertEqual(saved["proxy"], self.flow.proxy_url)

    def test_help_page_can_enter_oauth_without_restarting_login(self):
        auth = help.AUTH + "/oauth/authorize?" + urlencode({
            "redirect_uri": help.HELP + "/auth/callback", "state": "warm-state"})
        callback = help.HELP + "/auth/callback?code=warm-code&state=warm-state"
        for target in [help.HELP + "/auth/login?redirectTo=fixture", auth]:
            with self.subTest(target=target):
                responses = [response(302, headers={"location": target})]
                if target != auth: responses.append(response(302, headers={"location": auth}))
                responses += [response(302, headers={"location": callback}),
                              response(302, headers={"location": help.PAGE}),
                              response(text=help_page()), response(body={"email": EMAIL})]
                with patch.object(self.flow.web_session, "request", side_effect=responses) as req:
                    self.inquiry.authorize()
                urls = [c.args[1] for c in req.call_args_list]
                self.assertEqual(urls.count(auth), 1)
                self.assertEqual(urls.count(callback), 1)
                self.assertEqual(sum("/auth/login?" in u for u in urls), int(target != auth))

    def test_help_page_redirects_fail_closed_and_are_bounded(self):
        for target in ["https://evil.invalid/", "//help.openai.com.evil.invalid/",
                       "http://help.openai.com/", "https://user:pass@help.openai.com/",
                       "https://@help.openai.com/", "https://help.openai.com:8443/",
                       help.SUPPORT + "/api/v1/me", help.HELP + "/auth/callback?code=x&state=x"]:
            with self.subTest(target=target), patch.object(self.flow.web_session, "request",
                    return_value=response(302, headers={"location": target})) as req:
                with self.assertRaises(help.HelpError): self.inquiry.authorize()
                req.assert_called_once()
        for headers, error, count in [({}, "缺少地址", 1), ({"location": help.PAGE}, "跳转次数超限", 6)]:
            with patch.object(self.flow.web_session, "request", return_value=response(302, headers=headers)) as req:
                with self.assertRaisesRegex(help.HelpError, error): self.inquiry.authorize()
                self.assertEqual(req.call_count, count)
        self.assertNotIn("help_sending", [n for n, _ in self.calls])

    def test_help_redirect_diagnostics_omit_auth_query_and_fragment(self):
        current = help.HELP + "/auth/login?state=private-state"
        target = help.AUTH + "/oauth/authorize?code=private-code&state=private-state#private-token"
        with patch.object(help.bridge, "emit") as emit:
            self.assertEqual(self.inquiry.redirect_target(current, response(302, headers={"location": target})), target)
        diagnostic = json.dumps(emit.call_args.kwargs)
        self.assertNotIn("private-", diagnostic)
        self.assertEqual(emit.call_args.kwargs["response"], {"location": help.AUTH + "/oauth/authorize"})

    def test_warmup_auth_callback_transport_failure_is_not_replayed(self):
        auth = help.AUTH + "/oauth/authorize?" + urlencode({
            "redirect_uri": help.HELP + "/auth/callback", "state": "warm-state"})
        callback = help.HELP + "/auth/callback?code=warm-code&state=warm-state"
        with patch.object(self.flow.web_session, "request", side_effect=[
                response(302, headers={"location": auth}), response(302, headers={"location": callback}),
                ConnectionError("callback lost")]) as req:
            with self.assertRaises(ConnectionError): self.inquiry.authorize()
        self.assertEqual([c.args[1] for c in req.call_args_list], [help.PAGE, auth, callback])
        self.assertNotIn("help_sending", [n for n, _ in self.calls])

    def test_read_transport_retry_keeps_session_and_does_not_retry_writes(self):
        with patch.object(help.time, "sleep"), patch.object(self.flow.web_session, "request", side_effect=[ConnectionError("proxy 504"), response(body={"email": EMAIL})]) as req:
            self.assertTrue(self.inquiry.identity())
            self.assertEqual(req.call_count, 2)
        with patch.object(self.flow.web_session, "request", side_effect=ConnectionError("callback lost")) as req:
            with self.assertRaises(ConnectionError): self.inquiry.request(help.HELP + '/auth/callback?code=fixture')
            req.assert_called_once()
        with patch.object(self.flow.web_session, "request") as req:
            with self.assertRaises(help.HelpError): self.inquiry.query('threads.create', {})
            req.assert_not_called()

    def test_history_can_rewrite_ids_but_requires_unique_original_turn(self):
        self.inquiry.verified = True
        self.inquiry.sent_at = help.parse_time('2026-10-26T10:00:00Z')
        self.inquiry.thread, self.inquiry.user_message = 'thread-1', 'sse-user-id'
        messages = [item('user_message','stored-user-id',PROMPT), item('assistant_message','thinking','稍等'), item('assistant_message','stored-answer',ANSWER)]
        with patch.object(self.inquiry,'thread_items',return_value=messages):
            self.assertTrue(self.inquiry.recover()['success'])
        self.assertEqual(self.inquiry.user_message, 'stored-user-id')
        self.assertEqual(next(v['answer'] for n,v in self.calls if n=='help_reply'), ANSWER)
        self.calls.clear();self.inquiry.user_message = 'sse-user-id'
        with patch.object(self.inquiry,'thread_items',return_value=messages+[item('user_message','duplicate',PROMPT)]):
            with self.assertRaisesRegex(help.HelpError,'未唯一匹配'):self.inquiry.recover()
        self.assertNotIn('help_reply',[n for n,_ in self.calls])


if __name__ == "__main__": unittest.main()
