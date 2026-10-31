"""Offline protocol contracts; never sends an account mutation to OpenAI."""
import base64
import contextlib
import io
import json
import sys
import time
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).parent / "codex_runtime"))
from manager_oauth import adapter, merge_v2, upstream
from manager_oauth.adapter import ProjectProtocolLogin
from manager_oauth.merge_v2 import TeamProtocolLogin


def session(team="target-team", email="child@example.com"):
    payload={"email": email, "https://api.openai.com/auth": {"chatgpt_account_id": team, "chatgpt_user_id": "child-user"}}
    token="x."+base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip("=")+".x"
    return {"accessToken": token, "access_token": token, "account": {"id": team}, "user": {"email": email}}


class MergeV2ProtocolTests(unittest.TestCase):
    def flow(self):
        f=merge_v2.TeamProtocolLogin("fixture",{"email":"child@example.com","credential_mode":"chatgpt_at","proxy":"http://user:secret@proxy.invalid:8080","merge_v2":{"team_id":"target-team"}})
        self.addCleanup(f.close)
        f.team_session_at=time.monotonic()
        return f

    def response(self,status=200,body=None):
        r=Mock(status_code=status)
        r.json.return_value={"success":True} if body is None else body
        return r

    def test_exact_single_request_and_same_session(self):
        f=self.flow();rpc=[]
        with patch.object(f.web_session,"request",return_value=self.response()) as request,patch.object(merge_v2.bridge,"call",side_effect=lambda method,**kw:rpc.append(method) or True):
            result=f.submit_export(session())
        self.assertTrue(result["success"])
        request.assert_called_once()
        args,kw=request.call_args
        self.assertEqual(args,("POST",merge_v2.EXPORT_URL))
        self.assertNotIn("json",kw);self.assertNotIn("data",kw)
        self.assertNotIn("chatgpt-account-id",{k.lower() for k in kw["headers"]})
        self.assertEqual(kw["timeout"],60);self.assertNotIn("impersonate",kw)
        self.assertEqual(f.web_session.impersonate,upstream.OPENAI_IMPERSONATE)
        self.assertFalse(kw["allow_redirects"]);self.assertFalse(kw["default_headers"])
        self.assertEqual(kw["headers"]["User-Agent"],merge_v2.SAFARI_UA)
        self.assertEqual(kw["headers"]["Origin"],"https://chatgpt.com")
        self.assertEqual(kw["headers"]["Referer"],"https://chatgpt.com")
        self.assertEqual(kw["headers"]["oai-language"],"zh-CN")
        self.assertEqual(rpc,["merge_v2_ready","merge_v2_submitting","merge_v2_accepted"])
        self.assertNotIn("access_token",result)

    def test_strict_success_and_unknown_never_retry(self):
        for status,body in [(200,{}),(200,{"success":"true"}),(200,{"success":1}),(204,{}),(500,{"success":True}),(502,{}),(302,{})]:
            with self.subTest(status=status,body=body):
                f=self.flow();rpc=[]
                with patch.object(f.web_session,"request",return_value=self.response(status,body)) as request,patch.object(merge_v2.bridge,"call",side_effect=lambda method,**kw:rpc.append(method) or True):
                    result=f.submit_export(session())
                self.assertFalse(result["success"]);self.assertFalse(result["retryable"])
                self.assertNotIn("merge_v2_accepted",rpc);self.assertNotIn("merge_v2_rejected",rpc)
                request.assert_called_once()

    def test_transport_loss_preserves_checkpoint(self):
        f=self.flow()
        with patch.object(f.web_session,"request",side_effect=TimeoutError("timeout")) as request,patch.object(merge_v2.bridge,"call") as rpc:
            result=f.submit_export(session())
        self.assertFalse(result["retryable"]);request.assert_called_once()
        self.assertEqual([c.args[0] for c in rpc.call_args_list],["merge_v2_ready","merge_v2_submitting"])

    def test_submit_diagnostics_preserve_failure_without_secrets_or_retry(self):
        from curl_cffi.requests.exceptions import Timeout
        f=self.flow()
        merge_v2.bridge.configure(f.payload)
        token=session()["accessToken"]
        failure=Timeout("timeout http://user:secret@proxy.invalid:8080 " + token,code=28)
        with patch.object(f.web_session,"request",side_effect=failure) as request,patch.object(merge_v2.bridge,"call") as rpc,patch.object(merge_v2.bridge,"emit") as emit:
            result=f.submit_export(session())
        self.assertEqual(result["error_code"],"export_uncertain")
        request.assert_called_once()
        events=[c for c in emit.call_args_list if c.args[1]=="export_exception"]
        self.assertEqual(len(events),1)
        details=events[0].kwargs["details"]
        self.assertEqual(details["curl_code"],28)
        self.assertEqual(details["error_type"],"Timeout")
        self.assertFalse(details["retryable"])
        raw=str(emit.call_args_list)
        self.assertNotIn(token,raw);self.assertNotIn("user:secret",raw)
        self.assertNotIn("merge_v2_rejected",[c.args[0] for c in rpc.call_args_list])

    def test_wrong_team_or_identity_never_submits(self):
        for value in [session("personal"),session(email="someone@example.com"),{**session(),"account":{"id":"other"}}]:
            f=self.flow()
            with patch.object(f.web_session,"request") as req,patch.object(merge_v2.bridge,"call") as rpc,self.assertRaises(RuntimeError):
                f.submit_export(value)
            req.assert_not_called();rpc.assert_not_called()

    def test_missing_stale_or_future_timestamp_requires_fresh_login(self):
        for stamp in [None,time.monotonic()-120,time.monotonic()+100]:
            f=self.flow();f.team_session_at=stamp
            with patch.object(f.web_session,"request") as req,patch.object(merge_v2.bridge,"call") as rpc:
                result=f.submit_export(session())
            self.assertTrue(result["retryable"]);req.assert_not_called();rpc.assert_not_called()

    def test_stop_or_failed_checkpoint_prevents_post(self):
        for checkpoint in ["merge_v2_ready","merge_v2_submitting"]:
            f=self.flow()
            def call(method,**kwargs):
                if method==checkpoint:raise RuntimeError("stopped")
            with patch.object(f.web_session,"request") as req,patch.object(merge_v2.bridge,"call",side_effect=call),self.assertRaises(RuntimeError):
                f.submit_export(session())
            req.assert_not_called()

    def test_invalid_workspace_rejection_requests_new_login(self):
        f=self.flow()
        with patch.object(f.web_session,"request",return_value=self.response(400,{"error":{"code":"invalid_workspace_selected"}})) as request,patch.object(merge_v2.bridge,"call") as rpc:
            result=f.submit_export(session())
        self.assertTrue(result["retryable"]);self.assertFalse(result["success"]);request.assert_called_once()
        self.assertEqual(rpc.call_args.args[0],"merge_v2_rejected")

    def test_workspace_selection_and_callback_use_target_not_first(self):
        f=self.flow();calls=[]
        def request(url,**kw):
            calls.append((url,kw))
            if url.endswith("/workspace"):
                return upstream.ProtocolResponse(200,url,{},"{}")
            if url.endswith("workspace/select"):
                return upstream.ProtocolResponse(200,url,{},json.dumps({"continue_url":"https://chatgpt.com/api/auth/callback/openai?code=fixture"}))
            return upstream.ProtocolResponse(302,url,{"Location":"https://chatgpt.com/"},"{}")
        with patch.object(f,"request",side_effect=request),patch.object(f,"decode_oauth_session_cookie",return_value={"workspaces":[{"id":"wrong-first"},{"id":"target-team"}]}):
            f.follow_callback("https://auth.openai.com/workspace")
        self.assertEqual(calls[1][1]["json_data"],{"workspace_id":"target-team"})
        self.assertIn("/api/auth/callback/",calls[2][0])

    def test_adapter_reuses_login_and_keeps_credentials_private(self):
        original=merge_v2.TeamProtocolLogin;flows=[]
        class Flow(original):
            def login(self):
                flows.append(self)
                self.team_session_at=time.monotonic()
                return session()
        with patch.object(merge_v2,"TeamProtocolLogin",Flow),patch("curl_cffi.requests.Session.request",return_value=self.response()),patch.object(merge_v2.bridge,"call",return_value=True):
            result=adapter.run({"email":"child@example.com","credential_mode":"chatgpt_at","proxy":"http://proxy.invalid:8080","merge_v2":{"team_id":"target-team"},"login_mode":"password_totp","gpt_password":"fixture","totp_secret":"JBSWY3DPEHPK3PXP"})
        self.assertTrue(result["success"]);self.assertNotIn("access_token",result)
        self.assertEqual(flows[0].payload["password"],"fixture")
        self.assertEqual(flows[0].payload["_totp_secret"],"JBSWY3DPEHPK3PXP")
        self.assertIs(original.login,adapter.ProjectProtocolLogin.login)

    def test_mismatch_has_distinct_non_network_recovery_code_and_safe_evidence(self):
        f=self.flow()
        for value in [session("personal"), {**session(), "account":{"id":"other-team"}}, session("")]:
            with self.subTest(value=value), patch.object(merge_v2.bridge,"emit") as emit, self.assertRaises(upstream.LoginFlowError) as caught:
                f.validate_team(value)
            self.assertEqual(caught.exception.code,"team_session_mismatch")
            self.assertFalse(caught.exception.retryable)
            details=emit.call_args.kwargs["details"]
            self.assertTrue(details["email_matches"])
            self.assertTrue(all(isinstance(v,bool) for v in details.values()))
            self.assertNotIn(value["accessToken"],json.dumps(details))
            self.assertNotIn("other-team",json.dumps(details))

    def test_wrong_identity_does_not_enter_team_recovery(self):
        f=self.flow()
        with self.assertRaises(RuntimeError) as caught:
            f.validate_team(session("wrong-team","wrong@example.com"))
        self.assertNotIsInstance(caught.exception,upstream.LoginFlowError)

    def test_personal_session_accepts_existing_invite_then_requires_new_login(self):
        f=self.flow()
        with patch.object(f,"get_session",return_value=session("personal")),patch.object(f,"get_session_cookie",return_value=""),patch.object(f,"request",return_value=upstream.ProtocolResponse(200,"https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27",{},'{"accounts":{}}')) as reads,patch.object(f.web_session,"request",return_value=self.response()) as accept,patch.object(merge_v2.bridge,"call") as rpc,self.assertRaises(upstream.LoginFlowError) as caught:
            f._chatgpt_web_session()
        self.assertEqual(caught.exception.code,"team_invitation_accepted")
        self.assertFalse(caught.exception.retryable)
        self.assertEqual(accept.call_count,1)
        self.assertTrue(accept.call_args.args[1].endswith('/accounts/target-team/invites/accept'))
        self.assertEqual(accept.call_args.kwargs['headers']['chatgpt-account-id'],'target-team')
        self.assertEqual(accept.call_args.kwargs['json'],{})
        self.assertEqual([c.args[0] for c in rpc.call_args_list],['merge_v2_accepting','merge_v2_invite_accepted'])
        self.assertEqual(reads.call_count,1)

    def test_joined_member_never_accepts_again_and_identity_failure_never_recovers(self):
        for value in [session('personal'),session('personal','someone@example.com')]:
            f=self.flow()
            with patch.object(f,'get_session',return_value=value),patch.object(f,'get_session_cookie',return_value=''),patch.object(f,'request',return_value=upstream.ProtocolResponse(200,'https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27',{},'{"accounts":{"target-team":{"account":{"id":"target-team"}}}}')) as reads,patch.object(f.web_session,'request') as mutation,patch.object(merge_v2.bridge,'call') as rpc,self.assertRaises(RuntimeError):
                f._chatgpt_web_session()
            mutation.assert_not_called();rpc.assert_not_called()
            self.assertEqual(reads.call_count,1 if value['user']['email']=='child@example.com' else 0)

    def test_invite_timeout_only_reads_membership_and_never_exports_or_resends(self):
        for joined in [False,True]:
            f=self.flow()
            bodies=['{"accounts":{}}','{"accounts":{"target-team":{}}}' if joined else '{"accounts":{}}']
            responses=[upstream.ProtocolResponse(200,'https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27',{},b) for b in bodies]
            with patch.object(f,'get_session',return_value=session('personal')),patch.object(f,'get_session_cookie',return_value=''),patch.object(f,'request',side_effect=responses),patch.object(f.web_session,'request',side_effect=TimeoutError('timeout')) as mutation,patch.object(merge_v2.bridge,'call') as rpc,self.assertRaises(upstream.LoginFlowError) as caught:
                f._chatgpt_web_session()
            self.assertEqual(caught.exception.code,'team_invitation_accepted' if joined else 'team_invitation_unknown')
            mutation.assert_called_once()
            self.assertNotIn('merge_v2_submitting',[c.args[0] for c in rpc.call_args_list])

    def test_invitation_rejection_records_code_without_body_or_repeated_accept(self):
        f=self.flow();merge_v2.bridge.configure(f.payload)
        body={"error":{"code":"seat_limit_exceeded","type":"invalid_request_error",
                       "message":"private card or account details","token":session()["accessToken"]}}
        read=upstream.ProtocolResponse(200,'https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27',{},'{"accounts":{}}')
        with patch.object(f,'request',return_value=read) as reads,patch.object(f.web_session,'request',return_value=self.response(400,body)) as mutation,patch.object(merge_v2.bridge,'call') as rpc,patch.object(merge_v2.bridge,'emit') as emit,self.assertRaises(upstream.LoginFlowError) as caught:
            f.recover_invited_membership(session('personal'))
        self.assertEqual(caught.exception.code,'team_invitation_unknown')
        mutation.assert_called_once();self.assertEqual(reads.call_count,2)
        self.assertEqual([c.args[0] for c in rpc.call_args_list],['merge_v2_accepting'])
        event=next(c for c in emit.call_args_list if c.args[1]=='invite_accept_response')
        self.assertEqual(event.kwargs['response'],{'http_status':400,'body_kind':'json','error_code':'seat_limit_exceeded','error_type':'invalid_request_error'})
        self.assertFalse(event.kwargs['details']['invitation_accepted'])
        self.assertNotIn('private card',str(emit.call_args_list))
        self.assertNotIn(session()['accessToken'],str(emit.call_args_list))
        for bad in ({'error':{'code':session()['accessToken'],'type':'https://private.invalid'}}, {'error':{'code':['seat_limit_exceeded']}}, None):
            self.assertNotIn('error_code',merge_v2.invitation_response_evidence(bad))

    def test_export_diagnostic_uses_actual_login_tls_profile(self):
        f=self.flow()
        with patch.object(f.web_session,'request',return_value=self.response()),patch.object(merge_v2.bridge,'call'),patch.object(merge_v2.bridge,'emit') as emit:
            self.assertTrue(f.submit_export(session())['success'])
        event=next(c for c in emit.call_args_list if c.args[1]=='export_start')
        self.assertEqual(event.kwargs['request']['impersonate'],f.web_session.impersonate)

    def test_stop_or_previous_invite_checkpoint_prevents_acceptance(self):
        f=self.flow()
        with patch.object(f,'request',return_value=upstream.ProtocolResponse(200,'https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27',{},'{"accounts":{}}')),patch.object(f.web_session,'request') as mutation,patch.object(merge_v2.bridge,'call',side_effect=RuntimeError('stopped or already attempted')),self.assertRaises(RuntimeError):
            f.recover_invited_membership(session('personal'))
        mutation.assert_not_called()

    def test_adapter_returns_mismatch_code_without_export_or_internal_retry(self):
        def login(flow):
            flow.validate_team(session("personal"))
        with patch.object(merge_v2.TeamProtocolLogin,"login",login),patch.object(merge_v2.TeamProtocolLogin,"submit_export") as submit,patch.object(merge_v2.bridge,"call") as rpc:
            result=adapter.run({"email":"child@example.com","credential_mode":"chatgpt_at","proxy":"http://proxy.invalid:8080","merge_v2":{"team_id":"target-team"},"login_mode":"password_totp","gpt_password":"fixture","totp_secret":"JBSWY3DPEHPK3PXP"})
        self.assertEqual(result["error_code"],"team_session_mismatch")
        self.assertFalse(result["retryable"])
        submit.assert_not_called();rpc.assert_not_called()

    def test_direct_callback_mismatch_then_new_workspace_session(self):
        first=self.flow()
        with patch.object(first,"request",return_value=upstream.ProtocolResponse(302,"https://chatgpt.com/api/auth/callback/openai",{"Location":"https://chatgpt.com/"},"{}")), patch.object(first,"get_session",return_value=session("personal")), patch.object(first,"get_session_cookie",return_value=""), patch.object(first.web_session,"request") as export:
            first.follow_callback("https://chatgpt.com/api/auth/callback/openai?code=private-code")
            with self.assertRaises(upstream.LoginFlowError): first._chatgpt_web_session()
            export.assert_not_called()
        second=self.flow()
        self.assertIsNot(first.web_session,second.web_session)
        responses=[upstream.ProtocolResponse(200,"https://auth.openai.com/workspace",{},"{}"),upstream.ProtocolResponse(200,"https://auth.openai.com/api/accounts/workspace/select",{},json.dumps({"continue_url":"https://chatgpt.com/api/auth/callback/openai?code=other-code"})),upstream.ProtocolResponse(302,"https://chatgpt.com/api/auth/callback/openai",{"Location":"https://chatgpt.com/"},"{}")]
        with patch.object(second,"request",side_effect=responses) as request, patch.object(second,"get_session",return_value=session()),patch.object(second,"get_session_cookie",return_value=""):
            second.follow_callback("https://auth.openai.com/workspace")
            value=second._chatgpt_web_session()
        self.assertEqual(request.call_args_list[1].kwargs["json_data"],{"workspace_id":"target-team"})
        self.assertTrue(second.workspace_selected)
        self.assertLess(time.monotonic()-second.team_session_at,120)
        with patch.object(second.web_session,"request",return_value=self.response()) as export,patch.object(merge_v2.bridge,"call",return_value=True):
            self.assertTrue(second.submit_export(value)["success"])
            export.assert_called_once()


class MergeLoginStepTests(unittest.TestCase):
    def flow(self, pickup=False):
        flow = TeamProtocolLogin.__new__(TeamProtocolLogin)
        flow.payload = {"generic_accounts": [{"mode": "directurl", "pickup_url": "https://mail.invalid"}] if pickup else [], "_totp_secret": "fixture-secret"}
        flow.login_details = {"login_method": "not_started"}
        flow.device_id, flow.proxy_url = "fixture-device", "http://proxy.invalid"
        return flow

    def test_missing_step_retries_but_unknown_challenges_do_not(self):
        for data, retry in [({}, True), (None, True), ({"page": {}}, True),
                            ({"page": {"type": "auth_challenge"}}, False),
                            ({"page": {"type": "phone_verification"}}, False),
                            ({"error": {"code": "denied"}}, False)]:
            with self.subTest(data=data), self.assertRaises(upstream.LoginFlowError) as caught:
                self.flow().complete_modern_login(data, "password", 0)
            self.assertEqual(caught.exception.retryable, retry)
            if retry:
                self.assertEqual(caught.exception.code, "login_response_incomplete")

    def test_explicit_otp_without_source_does_not_poll_empty_mailbox(self):
        with patch.object(upstream, "fetch_login_verification_code") as fetch:
            for data in [{"page": {"type": "email_otp_verification"}}, {"continue_url": "/email-verification"}]:
                with self.assertRaises(upstream.LoginFlowError) as caught:
                    self.flow().complete_modern_login(data, "password", 0)
                self.assertEqual(caught.exception.code, "email_code_source_missing")
                self.assertFalse(caught.exception.retryable)
            fetch.assert_not_called()

    def test_only_pre_password_passwordless_mode_can_restart_normal_login(self):
        step = {"page": {"type": "email_otp_verification", "payload": {"email_verification_mode": "passwordless_login"}}}
        for scenario in ["identifier", "password", "no_totp", "email_mode", "no_mode", "pickup"]:
            flow = self.flow(scenario == "pickup")
            flow.payload.update(password="fixture-password", selected_login_mode="password_totp")
            data = step
            if scenario == "no_totp": flow.payload["_totp_secret"] = ""
            if scenario == "email_mode": flow.payload["selected_login_mode"] = "email_otp"
            if scenario == "no_mode": data = {"page": {"type": "email_otp_verification"}}
            if scenario == "pickup":
                flow.inspect_login_step(data, "identifier")
                continue
            with self.subTest(scenario=scenario), self.assertRaises(upstream.LoginFlowError) as caught:
                flow.inspect_login_step(data, "password" if scenario == "password" else "identifier")
            self.assertEqual(caught.exception.code, "password_login_restart_required" if scenario == "identifier" else "email_code_source_missing")
            # The outer merge coordinator alone owns this restart; no immediate
            # generic network retry/rotation may consume it.
            self.assertFalse(caught.exception.retryable)

    def test_configured_mailbox_still_polls_and_submits_real_code(self):
        flow = self.flow(True)
        flow.submit_modern_code = Mock(return_value={"continue_url": "/consent"})
        with contextlib.redirect_stderr(io.StringIO()), patch.object(upstream, "fetch_login_verification_code", return_value="123456") as fetch, patch.object(ProjectProtocolLogin, "log"):
            result = flow.complete_modern_login({"page": {"type": "email_otp_verification"}}, "password", 100)
        self.assertEqual(result, "https://auth.openai.com/consent")
        fetch.assert_called_once()
        flow.submit_modern_code.assert_called_once_with("123456")

    def test_password_totp_unchanged_and_password_response_checked(self):
        flow = self.flow()
        flow.complete_totp_challenge = Mock(return_value="fixture-callback")
        with contextlib.redirect_stderr(io.StringIO()), patch.object(ProjectProtocolLogin, "log"), patch.object(upstream, "generate_openai_sentinel_token"), patch.object(ProjectProtocolLogin, "submit_modern_password", return_value={"page": {"type": "mfa_challenge"}}):
            self.assertEqual(flow.complete_modern_login({"page": {"type": "login_password"}}, "password", 0), "fixture-callback")
        flow.complete_totp_challenge.assert_called_once()
        with patch.object(ProjectProtocolLogin, "submit_modern_password", return_value={}):
            with self.assertRaises(upstream.LoginFlowError) as caught:
                flow.submit_modern_password("password")
        self.assertEqual(caught.exception.code, "login_response_incomplete")

    def test_otp_timeout_is_explicit_without_restarting_login(self):
        flow = self.flow(True)
        with contextlib.redirect_stderr(io.StringIO()), patch.object(ProjectProtocolLogin, "log"), patch.object(upstream, "fetch_login_verification_code", return_value="") as fetch:
            with self.assertRaises(upstream.LoginFlowError) as caught:
                flow.complete_modern_login({"page": {"type": "email_otp_verification"}}, "password", 0)
        fetch.assert_called_once()
        self.assertEqual(caught.exception.code, "email_code_unavailable")
        self.assertFalse(caught.exception.retryable)


if __name__=="__main__":unittest.main()
