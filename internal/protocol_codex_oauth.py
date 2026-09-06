import json, re, sys, time, html, logging
from datetime import datetime
from curl_cffi import requests

OTP = re.compile(r"\b\d{6}\b")
CTX = re.compile(r"(?i)(?:code|验证码|認証コード|verification code)\D{0,80}(\d{6})")

def log(msg): print("[protocol] "+str(msg), file=sys.stderr, flush=True)
def clean(v):
    s = html.unescape(str(v or "")); s = re.sub(r"(?is)<[^>]+>", " ", s); return re.sub(r"\s+", " ", s)
def code_from(v):
    if isinstance(v, dict):
        for k in ("code","otp","verification_code","verificationCode","email_code","emailCode"):
            if k in v:
                c=code_from(v[k])
                if c:return c
        return code_from(" ".join(str(x) for x in v.values()))
    if isinstance(v,list): return code_from(" ".join(str(x) for x in v))
    s=clean(v)
    m=CTX.search(s)
    return m.group(1) if m else (OTP.search(s).group(0) if OTP.search(s) else "")

def main():
    p=json.load(sys.stdin); email=str(p.get("email") or "").strip(); pickup=str(p.get("pickup_url") or "").strip(); proxy=str(p.get("proxy") or "").strip()
    if not email or not pickup: raise RuntimeError("邮箱或取件链接未配置")
    logging.basicConfig(level=logging.INFO, format="[protocol] %(message)s")
    import config.codex as cfg
    cfg.ENABLE_CODEX_AUTO=True; cfg.CODEX_OAUTH_DRIVER="protocol"; cfg.CODEX_AUTH_URL_SOURCE="local"; cfg.CPA_MANAGEMENT_URL=""; cfg.CPA_MANAGEMENT_KEY=""
    cfg.SMS_PROVIDER=str(p.get("sms_provider") or ""); cfg.SMS_CONFIG=p.get("sms_config") or {}
    from core import codex_oauth
    sess=requests.Session(impersonate="chrome136")
    if proxy: sess.proxies={"http":proxy,"https":proxy}
    def otp_provider(account, after_ts=None):
        deadline=time.time()+180; seen=set()
        while time.time()<deadline:
            try:
                u=pickup+('&' if '?' in pickup else '?')+'json=1'; r=sess.get(u,timeout=25)
                c=code_from(r.json() if 'json' in r.headers.get('content-type','') else r.text)
                if c and c not in seen:return c
                # common mailbox API exposes /api/messages behind /messages/<token>
                if '/messages/' in pickup:
                    base=pickup.split('/messages/',1)[0]; token=pickup.split('/messages/',1)[1].split('/',1)[0]
                    rr=sess.get(base+'/api/messages',params={'email':account,'token':token,'limit':20},timeout=25)
                    c=code_from(rr.text)
                    if c and c not in seen:return c
            except Exception: pass
            time.sleep(3)
        raise RuntimeError("等待邮箱验证码超时")
    log("初始化 gpt-manager 风格 Codex OAuth 协议")
    result = manager_style_oauth(email, proxy, otp_provider, cfg, codex_oauth, str(p.get("gpt_password") or ""))
    if not isinstance(result,dict) or not result.get('success'):
        raise RuntimeError(str(result.get('error') if isinstance(result,dict) else result))
    out={'success':True,'access_token':result.get('access_token',''),'refresh_token':result.get('refresh_token',''),'account_id':result.get('account_id',''),'result':result}
    print(json.dumps(out,ensure_ascii=False))

def manager_style_oauth(email, proxy, otp_provider, cfg, co, password=""):
    """移植 gpt-account-manager refer_oauth.py 的纯协议主链。

    关键点：直接 authorize；passwordless/send-otp；email OTP 不带
    sentinel；按 page/continue_url 分流；add_phone 才申请号码；workspace
    后兼容 organization/select；token 交换使用三次独立请求重试。
    """
    from core.session import BrowserSession
    from urllib.parse import urlparse, parse_qs
    session=BrowserSession(proxy=proxy, fingerprint_seed=f"account:{email.lower()}")
    cv, cc = co._generate_pkce(); state=co._generate_state()
    auth=co._build_authorize_url(state, cc, prompt="login")
    try:
        # gpt-manager L1：authorize 直接建登录会话，不做 chatgpt.com 预检。
        co._bootstrap_authorize(session, state, cc, auth_url=auth)
        co.human_delay("api")
        # L2 authorize/continue，保留 sentinel + RUM/Datadog 头。
        email_step_resp = co._submit_email(session, email)
        email_step = co._resp_json(email_step_resp)
        page = email_step.get("page") if isinstance(email_step,dict) else {}
        payload = page.get("payload") if isinstance(page,dict) and isinstance(page.get("payload"),dict) else {}
        passwordless_allowed = payload.get("passwordless_disabled") is False
        if passwordless_allowed or not password:
            # Existing-account login follows gpt-manager's modern OTP kickoff:
            # try resend/send first, then passwordless. OpenAI may return 409
            # for passwordless when an OTP challenge is already active.
            otp_attempts = [
                ("POST", "https://auth.openai.com/api/accounts/email-otp/resend", "https://auth.openai.com/email-verification", None),
                ("GET", "https://auth.openai.com/api/accounts/email-otp/send", "https://auth.openai.com/email-verification", None),
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp", "https://auth.openai.com/email-verification", {}),
            ]
            send_ok = False
            last_status = 0
            for method, url, referer, body in otp_attempts:
                send_headers = session.get_auth_headers(referer=referer)
                try:
                    if method == "GET":
                        send = session.get(url, headers=send_headers, allow_redirects=False)
                    else:
                        send = session.post(url, headers=send_headers, data=json.dumps(body) if body is not None else None, allow_redirects=False)
                    last_status = send.status_code
                    if send.status_code == 200:
                        log(f"{url.rsplit('/', 1)[-1]} 成功")
                        send_ok = True
                        break
                    log(f"{url.rsplit('/', 1)[-1]} HTTP {send.status_code}，尝试下一个发码端点")
                except Exception as exc:
                    log(f"{url.rsplit('/', 1)[-1]} 请求异常：{str(exc)[:160]}")
            if not send_ok:
                if last_status == 409 and password:
                    passwordless_allowed = False
                else:
                    raise RuntimeError(f"OTP 发码失败，最后 HTTP {last_status}")
        if not passwordless_allowed and password:
            log("passwordless 不可用，按 gpt-manager 回退 password/verify")
            ph=session.get_auth_headers(referer="https://auth.openai.com/log-in/password")
            pr=session.post("https://auth.openai.com/api/accounts/password/verify", headers=ph, data=json.dumps({"password":password}), allow_redirects=False)
            if pr.status_code != 200: raise RuntimeError(f"password/verify HTTP {pr.status_code}: {(pr.text or '')[:180]}")
            pstep=co._resp_json(pr); ptype=((pstep.get("page") or {}).get("type") if isinstance(pstep,dict) else "") or ""
            if "email" not in ptype and "verification" not in str(pstep.get("continue_url", "")) and "consent" not in ptype:
                raise RuntimeError("password/verify 未返回邮箱验证或 consent 页面")
        # gpt-manager keeps a used-code set and resends when a stale OTP is
        # returned by the mailbox provider. Do the same here.
        h=session.get_auth_headers(referer="https://auth.openai.com/email-verification")
        session._attach_oai_context_headers(h)
        val=None; used=set()
        for attempt in range(1,4):
            code=otp_provider(email, after_ts=time.time())
            if code in used:
                code=otp_provider(email, after_ts=time.time())
            used.add(code)
            log(f"提交 email-otp/validate（第 {attempt}/3 次，不携带 sentinel）")
            val=session.post("https://auth.openai.com/api/accounts/email-otp/validate", headers=h, data=json.dumps({"code":code}), allow_redirects=False)
            if val.status_code == 200: break
            body=(val.text or "").lower()
            if "wrong_email_otp_code" not in body and "wrong code" not in body and "expired" not in body:
                raise RuntimeError(f"email-otp/validate HTTP {val.status_code}: {(val.text or '')[:180]}")
            if attempt < 3:
                resend=session.post("https://auth.openai.com/api/accounts/email-otp/resend", headers=h, data="{}", allow_redirects=False)
                log(f"email-otp/resend HTTP {resend.status_code}，等待新验证码")
                time.sleep(2)
        if val is None or val.status_code != 200: raise RuntimeError(f"email-otp/validate HTTP {val.status_code if val else 0}: {(val.text if val else '')[:180]}")
        step=val.json() if val.text else {}; cont=co._extract_continue_url_from_step(step); page=((step.get("page") or {}).get("type") if isinstance(step,dict) else "") or ""
        log(f"OTP validate → page={page or '-'} continue_url={cont[:100] if cont else '-'}")
        if co._needs_phone_verification(step,cont):
            if not co._needs_add_phone(step,cont): raise RuntimeError("OpenAI 要求已绑定手机号验证，未发现 add_phone")
            log("检测到 add_phone，进入本项目接码适配")
            co._do_phone_verification(session)
        # gpt-manager consent path：workspace/select 后处理 organization/select。
        wid=co._get_workspace_id(session)
        ws=co._post_json(session,"https://auth.openai.com/api/accounts/workspace/select",{"workspace_id":wid},referer="https://auth.openai.com/sign-in-with-chatgpt/codex/consent")
        if ws.status_code not in (200,201,204,301,302,303,307,308): raise RuntimeError(f"workspace/select HTTP {ws.status_code}: {(ws.text or '')[:180]}")
        loc=ws.headers.get("location") or ws.headers.get("Location"); data=co._resp_json(ws)
        nxt=loc or co._extract_continue_url_from_step(data)
        if isinstance(data,dict) and ("organization" in str(data).lower() or "organization" in nxt.lower()):
            orgs=((data.get("data") or {}).get("orgs") if isinstance(data.get("data"),dict) else []) or []
            if orgs:
                org=orgs[0]; oid=org.get("id")
                org_resp=co._post_json(session,"https://auth.openai.com/api/accounts/organization/select",{"org_id":oid},referer=nxt or "https://auth.openai.com/sign-in-with-chatgpt/codex/consent")
                nxt=org_resp.headers.get("location") or co._extract_continue_url_from_step(co._resp_json(org_resp))
        if not nxt: raise RuntimeError("workspace/organization 选择后没有 callback URL")
        callback=co._follow_until_callback(session,nxt,state); code=co._extract_code(callback,state)
        # gpt-manager token 交换：独立 session、form-urlencoded、最多 3 次。
        token=None
        for attempt in range(1,4):
            log(f"oauth/token 交换（第 {attempt}/3 次）")
            try:
                tx=requests.Session(impersonate="chrome136")
                if proxy: tx.proxies={"http":proxy,"https":proxy}
                trr=tx.post(cfg.CODEX_TOKEN_URL, headers={"Content-Type":"application/x-www-form-urlencoded","Accept":"application/json"}, data={"grant_type":"authorization_code","code":code,"redirect_uri":cfg.CODEX_REDIRECT_URI,"client_id":cfg.CODEX_CLIENT_ID,"code_verifier":cv}, timeout=60)
                if trr.status_code == 200:
                    tr=trr.json()
                    if tr.get("access_token"): token=tr; tx.close(); break
                log(f"oauth/token HTTP {trr.status_code}")
                tx.close()
            except Exception as exc:
                log(f"oauth/token 网络错误：{str(exc)[:160]}")
        if not token or not token.get("refresh_token"): raise RuntimeError("oauth/token 未返回完整 AT/RT")
        return {'success':True,'access_token':token.get('access_token',''),'refresh_token':token.get('refresh_token',''),'account_id':(co._parse_id_token(token.get('id_token','')) or {}).get('account_id',''),'callback_url':callback}
    finally:
        try: session.close()
        except Exception: pass

if __name__=='__main__':
    try: main()
    except Exception as e:
        print(json.dumps({'success':False,'error':f'{type(e).__name__}: {e}'},ensure_ascii=False))
