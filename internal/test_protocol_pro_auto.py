"""Offline checks for the continuous-session boundary, real PKCE and cookies."""
import contextlib
import hashlib
import base64
import http.cookiejar
import io
import json
import unittest
from urllib.parse import parse_qs, urlsplit
from unittest.mock import patch

from test_protocol_codex_oauth import Response
from manager_oauth import continuous_pro as pro
from manager_oauth import upstream


class CookieStore:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
    def set(self, name, value, domain, path):
        self.jar.set_cookie(http.cookiejar.Cookie(0,name,value,None,False,domain,True,domain.startswith('.'),path,True,True,None,True,None,None,{}))


class ContinuousSessionTest(unittest.TestCase):
    def scenario(self, drift_at=0, relogin=False, payment_error=False, bad_trace_at=0,
                 expected_ip='198.51.100.8', real_login=False, login_mode='email_otp', trace_error_at=0,
                 all_unavailable=False):
        clients, calls, events, diagnostics = [], [], [], []
        outer = self
        proxy = 'http://sticky:pass@proxy.test:80'
        class Session:
            def __init__(self, **kwargs):
                self.cookies = CookieStore(); self.closed = False; self.traces = 0; self.options=kwargs
                clients.append(self)
            def get(self, url, **kwargs):
                outer.assertFalse(self.closed)
                self.traces+=1
                events.append('ip'+str(self.traces))
                if self.traces == trace_error_at:raise RuntimeError('trace timeout '+proxy)
                return Response(url,body="unavailable" if all_unavailable or self.traces == bad_trace_at else "ip="+("198.51.100.9" if drift_at and self.traces>=drift_at else "198.51.100.8"))
            def request(self, method, url, **kwargs):
                outer.assertFalse(self.closed)
                outer.assertEqual(self.options['proxies'], upstream._cffi_proxies(proxy))
                path=urlsplit(url).path;calls.append(path)
                if real_login:
                    if path=='/':return Response(url)
                    if path=='/api/auth/csrf':return Response(url,body={'csrfToken':'csrf'})
                    if path=='/api/auth/signin/openai':
                        events.append('login')
                        self.device=parse_qs(urlsplit(url).query)['ext-oai-did'][0]
                        return Response(url,body={'url':'https://auth.openai.com/authorize?state=web-state'})
                    if path=='/authorize':
                        self.cookies.set('login_session','auth-cookie','auth.openai.com','/')
                        return Response(url)
                outer.assertIn('auth-cookie', [c.value for c in self.cookies.jar])
                if real_login:
                    if path=='/api/accounts/authorize/continue':
                        return Response(url,body={'page':{'type':'login_password' if login_mode=='password_totp' else 'email_otp_verification'}})
                    if path=='/api/accounts/password/verify' or (path=='/api/accounts/email-otp/validate' and login_mode=='email_mfa'):
                        return Response(url,body={'page':{'type':'mfa_challenge','payload':{'factors':[{'id':'factor','factor_type':'totp'}]}}})
                    if path=='/api/accounts/mfa/issue_challenge':return Response(url)
                    if path in ('/api/accounts/email-otp/validate','/api/accounts/mfa/verify'):
                        return Response(url,body={'continue_url':'https://chatgpt.com/api/auth/callback/openai?code=web-code&state=web-state'})
                    if path=='/api/auth/callback/openai':
                        self.cookies.set('__Secure-next-auth.session-token','web-cookie','chatgpt.com','/')
                        return Response(url)
                if path=='/api/auth/session':return Response(url,body={'accessToken':'updated-web-at' if 'pay' in events else 'first-web-at','user':{'email':'pro@example.com'},'account':{'id':'personal'}})
                if path=='/oauth/authorize':
                    if real_login:
                        outer.assertIn('web-cookie',[c.value for c in self.cookies.jar])
                        outer.assertIn(self.device,[c.value for c in self.cookies.jar])
                    query=parse_qs(urlsplit(url).query);self.state=query['state'][0];self.challenge=query['code_challenge'][0]
                    return Response(url,status=302,location='https://auth.openai.com/log-in' if relogin else 'https://auth.openai.com/consent')
                if path=='/log-in':return Response(url)
                if path=='/consent':return Response(url,status=302,location='http://localhost:1455/auth/callback?code=code-after-pay&state='+self.state)
                if path=='/oauth/token':
                    form=parse_qs(kwargs['data'].decode());challenge=base64.urlsafe_b64encode(hashlib.sha256(form['code_verifier'][0].encode()).digest()).decode().rstrip('=')
                    outer.assertEqual(challenge,self.challenge);outer.assertEqual(form['code'][0],'code-after-pay')
                    self.cookies.set('__Secure-next-auth.session-token','latest-web-cookie','chatgpt.com','/')
                    return Response(url,body={'access_token':'new-oauth-at','refresh_token':'new-oauth-rt'})
                raise AssertionError('Unexpected call '+path)
            def close(self):self.closed=True;events.append('close')
        def login(flow):
            events.append('login');flow.device_id='same-device';flow.set_cookie('login_session','auth-cookie','auth.openai.com')
            return {'access_token':'first-web-at','user':{'email':'pro@example.com'},'account':{'id':'personal'}}
        def rpc(method, args):
            outer.assertEqual(len(clients),1);outer.assertFalse(clients[0].closed)
            if method=='pro_recharge':
                events.append('pay');outer.assertEqual(args['session']['access_token'],'first-web-at')
                outer.assertTrue(args['client_id'])
                if payment_error:raise RuntimeError('payment not confirmed')
                return {'status':'success'}
            if method=='pro_tokens':
                outer.assertTrue(args['client_id'])
                outer.assertEqual(args['client_id'],args['initial_client_id'])
                outer.assertTrue(args['client_consistent'])
                outer.assertEqual(args['web_session']['access_token'],'updated-web-at')
                outer.assertEqual(args['web_session']['session_token'],'latest-web-cookie')
                events.append('tokens');outer.assertEqual(args['tokens']['refresh_token'],'new-oauth-rt');return True
            raise AssertionError(method)
        def sentinel(device, stage, proxy_url):
            outer.assertEqual(proxy_url,proxy)
            outer.assertIn(device,[c.value for c in clients[0].cookies.jar])
            return 'fixture-sentinel'
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch('curl_cffi.requests.Session',Session))
            stack.enter_context(patch('curl_cffi.requests.request',side_effect=AssertionError('Unexpected separate client')))
            stack.enter_context(patch('curl_cffi.requests.post',side_effect=AssertionError('Unexpected network')))
            stack.enter_context(patch('urllib.request.OpenerDirector.open',side_effect=AssertionError('Unexpected network')))
            if not real_login:
                stack.enter_context(patch.object(pro.ContinuousProLogin,'login',login))
            stack.enter_context(patch.object(upstream,'generate_openai_sentinel_token',side_effect=sentinel))
            stack.enter_context(patch.object(upstream,'fetch_login_verification_code',return_value='234567'))
            stack.enter_context(patch.object(pro.time,'sleep',lambda *_:None))
            stack.enter_context(patch.object(pro.bridge,'emit',side_effect=lambda *a,**k:diagnostics.append((a,k))))
            stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
            result=pro.run({'email':'pro@example.com','proxy':proxy,'expected_exit_ip':expected_ip,
                            'login_mode':login_mode,'gpt_password':'fixture-password','totp_secret':'JBSWY3DPEHPK3PXP'},rpc)
        self.assertEqual(len(clients),1);self.assertTrue(clients[0].closed)
        self.assertEqual(clients[0].options['impersonate'],upstream.OPENAI_IMPERSONATE)
        checks=[kw['details'] for args,kw in diagnostics if args[1]=='pro_ip_observation']
        self.assertTrue(checks)
        self.assertTrue(all(c['phase'] and c['diagnostic_only'] and c['session_reused'] for c in checks))
        self.assertEqual(len({c['client_id'] for c in checks}),1)
        self.assertNotIn(proxy,json.dumps(diagnostics))
        self.observations=checks
        self.comparisons=[kw['details'] for args,kw in diagnostics if args[1]=='pro_login_oauth_comparison']
        if result['success']:
            self.assertEqual(len(self.comparisons),3)
            self.assertTrue(all(c['client_consistent'] and not c['relogin'] for c in self.comparisons))
            self.assertEqual([c['credentials_saved'] for c in self.comparisons],[False,False,True])
            self.assertLess(calls.index('/oauth/token'),max(i for i,c in enumerate(calls) if c=='/api/auth/session'))
        return result,events,calls
    def test_keeps_one_client_through_payment_and_oauth(self):
        result,events,calls=self.scenario();self.assertTrue(result['success'],result)
        self.assertEqual(events,['ip1','ip2','login','ip3','pay','ip4','ip5','ip6','tokens','close']);self.assertEqual(calls.count('/oauth/token'),1)
        self.assertNotIn('/api/accounts/authorize/continue',calls)
        self.assertTrue(all(c['ip_consistent'] for c in self.comparisons))
    def test_drift_and_unavailable_ip_continue_at_each_boundary(self):
        for phase in range(1,7):
            for failure in ('drift_at','bad_trace_at','trace_error_at'):
                with self.subTest(phase=phase,failure=failure):
                    result,events,calls=self.scenario(**{failure:phase})
                    self.assertTrue(result['success'],result)
                    self.assertEqual(events,['ip1','ip2','login','ip3','pay','ip4','ip5','ip6','tokens','close'])
                    self.assertEqual(calls.count('/oauth/token'),1)
                    self.assertTrue(all(c['expected_exit_ip']=='198.51.100.8' for c in self.observations))
                    check=self.observations[phase-1]
                    self.assertEqual(check['ip_check_status'],'changed' if failure=='drift_at' else 'unavailable')
                    self.assertIs(check['ip_consistent'],False if failure=='drift_at' else None)
                    if failure=='drift_at':
                        self.assertIs(self.comparisons[-1]['ip_consistent'],phase<=3)
                    elif phase in (3,6):
                        self.assertIsNone(self.comparisons[-1]['ip_consistent'])
    def test_missing_baseline_records_first_available_ip_and_continues(self):
        for baseline in ('','invalid'):
            result,events,calls=self.scenario(expected_ip=baseline)
            self.assertTrue(result['success'],result);self.assertIn('tokens',events)
            self.assertEqual(self.observations[0]['ip_check_status'],'baseline')
            self.assertTrue(all(c['expected_exit_ip']=='198.51.100.8' for c in self.observations))
        result,events,calls=self.scenario(expected_ip='',bad_trace_at=1)
        self.assertTrue(result['success'],result)
        self.assertEqual(self.observations[0]['ip_check_status'],'unavailable')
        self.assertEqual(self.observations[1]['ip_check_status'],'baseline')
    def test_all_ip_checks_can_fail_without_blocking_tokens(self):
        result,events,calls=self.scenario(expected_ip='',all_unavailable=True)
        self.assertTrue(result['success'],result);self.assertIn('tokens',events)
        self.assertTrue(all(c['ip_check_status']=='unavailable' and c['ip_consistent'] is None for c in self.observations))
        self.assertTrue(all(c['ip_consistent'] is None for c in self.comparisons))
    def test_real_initial_login_and_post_payment_oauth_share_cookies(self):
        for mode in ('email_otp','password_totp','email_mfa'):
            with self.subTest(mode=mode):
                result,events,calls=self.scenario(real_login=True,login_mode=mode)
                self.assertTrue(result['success'],result)
                self.assertEqual(events,['ip1','ip2','login','ip3','pay','ip4','ip5','ip6','tokens','close'])
                self.assertEqual(calls.count('/api/accounts/authorize/continue'),1)
                self.assertEqual(calls.count('/api/auth/signin/openai'),1)
                self.assertEqual(calls.count('/oauth/token'),1)
                self.assertEqual(calls.count('/api/accounts/password/verify'),int(mode=='password_totp'))
                self.assertEqual(calls.count('/api/accounts/mfa/verify'),int(mode!='email_otp'))
    def test_expired_session_never_relogs(self):
        result,events,calls=self.scenario(relogin=True);self.assertFalse(result['success']);self.assertEqual(events.count('login'),1);self.assertNotIn('/oauth/token',calls)
    def test_uncertain_payment_never_authorizes(self):
        result,events,calls=self.scenario(payment_error=True);self.assertFalse(result['success']);self.assertEqual(events,['ip1','ip2','login','ip3','pay','close']);self.assertEqual(calls,[])

    def test_selects_only_the_paid_workspace(self):
        flow=object.__new__(pro.ContinuousProLogin);flow.paid_account_id='personal'
        self.assertEqual(flow.first_workspace_id({'workspaces':[{'id':'unrelated-team'},{'id':'personal'}]}),'personal')
        with self.assertRaises(RuntimeError):flow.first_workspace_id({'workspaces':[{'id':'unrelated-team'}]})

    def test_client_comparison_reports_actual_object_identity(self):
        flow=object.__new__(pro.ContinuousProLogin)
        flow.client_id='initial-client';flow.login_client=object();flow.web_session=flow.login_client
        self.assertTrue(flow.client_details()['client_consistent'])
        flow.web_session=object()
        details=flow.client_details()
        self.assertFalse(details['client_consistent']);self.assertFalse(details['session_reused'])
        self.assertNotEqual(details['initial_client_id'],details['client_id'])

if __name__=='__main__':unittest.main()
