"""Offline checks for personal-plan recovery; no real account is used."""
import base64,json,sys,unittest
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).parent/'codex_runtime'))
from manager_oauth import merge_v2_plan as f
class PlanTests(unittest.TestCase):
    def data(self,current='free',subscription='chatgptpro',active=False):
        return {'accounts':{'personal':{'account':{'account_id':'personal','structure':'personal','plan_type':current},'entitlement':{'subscription_plan':subscription,'has_active_subscription':active}}}}
    def test_strict_plan(self):
        self.assertEqual(f.parse_plan(self.data(),'personal'),'free')
        self.assertEqual(f.parse_plan(self.data('pro','chatgptpro',True),'personal'),'paid')
        self.assertEqual(f.parse_plan(self.data('promax','chatgptpromax',True),'personal'),'paid')
        self.assertEqual(f.parse_plan(self.data('free','chatgptpromax',False),'personal'),'free')
        for d in ({},self.data('free','chatgptpro',True),self.data('free','chatgptpro',None),self.data('pro','chatgptpro',False),self.data('free','chatgptpro','false')):
            with self.assertRaises(ValueError):f.parse_plan(d,'personal')
        with self.assertRaises(ValueError):f.parse_plan(self.data(),'another')
    def session(self,team='personal',email='a@example.com',user='child'):
        claims={'email':email,'https://api.openai.com/auth':{'chatgpt_account_id':team,'chatgpt_user_id':user}}
        token='x.'+base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip('=')+'.x'
        return {'accessToken':token,'user':{'email':email},'account':{'id':team}}
    def test_identity_and_live_reads(self):
        job={'account':{'email':'a@example.com'},'mother':{'team_id':'team'},'child_user_id':'child'}
        for session in (self.session('team'),self.session(email='b@example.com'),self.session(user='other')):
            with self.assertRaises(RuntimeError):f.identity(session,job)
        flow=Mock();flow.device_id='fixture'
        responses=[Mock(status_code=200),Mock(status_code=200)]
        responses[0].json.return_value={'email':'a@example.com'};responses[1].json.return_value=self.data()
        flow.web_session.get.side_effect=responses
        self.assertEqual(f.check_plan(flow,self.session(),job),'free')
        self.assertEqual(flow.web_session.get.call_count,2)
        flow.web_session.post.assert_not_called()
        responses[0].json.return_value={'email':'b@example.com'};flow.web_session.get.side_effect=responses
        with self.assertRaises(RuntimeError):f.check_plan(flow,self.session(),job)
    def test_run_selects_personal_and_returns_no_credentials(self):
        flow=Mock();flow.payload={'email':'a@example.com','merge_v2_plan':{'team_id':'team','user_id':'child'}}
        flow.login.return_value=self.session()
        with patch.object(f,'check_plan',return_value='free'):
            self.assertEqual(f.run(flow),{'success':True,'plan':'free','user_id':'child'})
        flow.submit_export.assert_not_called()
    def test_personal_callback(self):
        flow=object.__new__(f.PersonalLogin);flow.workspace_selected=False
        flow.normalize_auth_url=lambda x:x;flow.is_workspace_or_consent_url=lambda x:False
        flow.headers=lambda *a:{}
        r=Mock(status=302);r.location.return_value='https://chatgpt.com/'
        flow.request=lambda *a,**k:r
        flow.follow_callback('https://chatgpt.com/api/auth/callback/openai')
if __name__=='__main__':unittest.main()
