"""The third-step retry boundary must keep payment outside its loop."""
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from test_protocol_siwc import SIWCScenario  # installs the runtime path
from manager_oauth import bridge, upstream
from manager_oauth.continuous_pro import complete_paid_oauth
from manager_oauth.siwc import SIWCProtocolLogin


class PaidOAuthRetryTests(unittest.TestCase):
    def flow(self):
        return SimpleNamespace(_chatgpt_web_session=Mock(return_value={"access_token":"web"}),
                               paid_account_id="personal", record_exit=Mock(), record_oauth_context=Mock(),
                               exit_details=lambda: {}, log=Mock(), continue_codex=Mock())

    def test_retry_keeps_client_and_does_not_call_payment(self):
        for source in ("local", "sub2api", "codex"):
            with self.subTest(source=source):
                flow=self.flow();calls=[];clients=[]
                def authorize(current,web=None):
                    clients.append(current)
                    if len(clients)<3:raise RuntimeError("HTTP 200 without next page" if len(clients)==1 else "upstream 503")
                    return {"access_token":"oauth-at","refresh_token":"oauth-rt"}
                flow.continue_codex.side_effect=lambda:authorize(flow)
                def rpc(method,**params):
                    calls.append(method)
                    if method=="pro_oauth_retry":return {"retry":True}
                    if method=="siwc_retry_prepare":return {"completed":False}
                    if method=="pro_tokens":return True
                    raise AssertionError("unexpected payment or unrelated RPC: "+method)
                with patch.object(bridge,"call",rpc),patch.object(SIWCProtocolLogin,"continue_authenticated",authorize):
                    complete_paid_oauth(flow,{"credential_mode":"codex" if source=="codex" else "siwc","siwc_authorization_source":source})
                self.assertEqual(clients,[flow,flow,flow]);self.assertEqual(calls.count("pro_oauth_retry"),2)
                self.assertEqual(calls.count("pro_tokens"),1)

    def test_uncertain_import_is_reconciled_before_new_authorization(self):
        flow=self.flow();calls=[];authorize=Mock(side_effect=RuntimeError("callback response lost"))
        def rpc(method,**params):
            calls.append(method)
            if method=="pro_oauth_retry":return {"retry":True}
            if method=="siwc_retry_prepare":return {"completed":True}
            raise AssertionError(method)
        with patch.object(bridge,"call",rpc),patch.object(SIWCProtocolLogin,"continue_authenticated",authorize):
            complete_paid_oauth(flow,{"credential_mode":"siwc","siwc_authorization_source":"sub2api"})
        self.assertEqual(authorize.call_count,1);self.assertEqual(calls,["pro_oauth_retry","siwc_retry_prepare"])

    def test_saved_tokens_are_reused_when_persistence_retries(self):
        flow=self.flow();authorize=Mock(return_value={"access_token":"oauth-at","refresh_token":"oauth-rt"});saves=[]
        def rpc(method,**params):
            if method=="pro_oauth_retry":return {"retry":True}
            if method=="pro_tokens":
                saves.append(params["tokens"])
                if len(saves)==1:raise RuntimeError("save failed")
                return True
            raise AssertionError(method)
        with patch.object(bridge,"call",rpc),patch.object(SIWCProtocolLogin,"continue_authenticated",authorize):
            complete_paid_oauth(flow,{"credential_mode":"siwc"})
        self.assertEqual(authorize.call_count,1);self.assertIs(saves[0],saves[1])

    def test_expired_session_exhaustion_and_stop_leave_the_loop(self):
        for reason,decision,expected in [
                ("siwc_session_expired",{"retry":True,"relogin":True},"oauth_paid_relogin"),
                ("",{"retry":False,"message":"2/2"},"oauth_retry_exhausted"),
                ("siwc_consent_not_authorized",{"retry":False},"siwc_consent_not_authorized")]:
            with self.subTest(reason=reason):
                flow=self.flow();authorize=Mock(side_effect=upstream.LoginFlowError("authorization failure",code=reason,status=401))
                with patch.object(bridge,"call",return_value=decision),patch.object(SIWCProtocolLogin,"continue_authenticated",authorize),self.assertRaises(upstream.LoginFlowError) as e:
                    complete_paid_oauth(flow,{"credential_mode":"siwc"})
                self.assertEqual(e.exception.code,expected);self.assertEqual(authorize.call_count,1)
        flow=self.flow();authorize=Mock(side_effect=RuntimeError("failure"))
        with patch.object(bridge,"call",side_effect=RuntimeError("stopped")),patch.object(SIWCProtocolLogin,"continue_authenticated",authorize),self.assertRaisesRegex(RuntimeError,"stopped"):
            complete_paid_oauth(flow,{"credential_mode":"siwc"})
        self.assertEqual(authorize.call_count,1)


if __name__=="__main__":unittest.main()
