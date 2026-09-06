"""Local project SMS adapter used by the in-process Codex OAuth runner.

The adapter talks directly to the configured provider API; it never contacts
the gpt-account-manager service. Configuration is supplied by the Go caller.
"""
import json, re, time
from urllib.parse import urlencode
from curl_cffi import requests

class SMSProviderError(RuntimeError):
    pass

def _cfg_value(cfg, key, default=""):
    v = cfg.get(key, default) if isinstance(cfg, dict) else default
    return str(v if v is not None else default).strip()

class ProjectSMS:
    def __init__(self, provider, config=None, proxy=""):
        self.provider = (provider or "").strip().lower()
        self.config = config or {}
        self.proxy = proxy or ""
        self.activation_id = ""
        self.phone = ""
        self._cdk = ""
        self.session = requests.Session(impersonate="chrome136")
        if self.proxy:
            self.session.proxies = {"http": self.proxy, "https": self.proxy}

    def _json(self, method, url, **kwargs):
        try:
            r = self.session.request(method, url, timeout=25, **kwargs)
            try: data = r.json()
            except Exception: data = {}
            if r.status_code >= 400:
                raise SMSProviderError(f"SMS HTTP {r.status_code}: {(r.text or '')[:180]}")
            return data
        except SMSProviderError:
            raise
        except Exception as e:
            raise SMSProviderError(str(e))

    def _cards(self):
        raw = self.config.get("cdks", [])
        if isinstance(raw, str):
            return [x.strip() for x in re.split(r"[\r\n,]+", raw) if x.strip()]
        return [str(x).strip() for x in (raw or []) if str(x).strip()]

    def acquire(self):
        p = self.provider
        if p == "hero_sms":
            base = _cfg_value(self.config, "base_url", "https://hero-sms.com/stubs/handler_api.php")
            q = {"api_key": _cfg_value(self.config, "api_key"), "action":"getNumber",
                 "service":_cfg_value(self.config,"service","dr"), "country":_cfg_value(self.config,"country","52"),
                 "maxPrice":_cfg_value(self.config,"max_price","1")}
            text = self.session.get(base, params=q, timeout=25).text.strip()
            if not text.startswith("ACCESS_NUMBER:"):
                raise SMSProviderError(text[:180] or "申请号码失败")
            _, aid, phone = (text.split(":", 2)+["",""])[:3]
            self.activation_id, self.phone = aid, "+"+re.sub(r"\D", "", phone)
        elif p in ("nextpro", "congou", "chatai"):
            cards = self._cards()
            if not cards: raise SMSProviderError(f"{p} 卡密池为空")
            self._cdk = cards[0]
            base = _cfg_value(self.config, "base_url", {"nextpro":"http://nextpro.top:8080","congou":"https://sapi.congou.eu.cc","chatai":"https://chataiapi.online/sms"}[p]).rstrip("/")
            if p == "nextpro":
                d = self._json("POST", base+"/api/redeem", json={"cdk":self._cdk})
                self.activation_id = str(d.get("token") or ""); self.phone = str((d.get("activation") or {}).get("phone") or "")
            elif p == "congou":
                d = self._json("POST", base+"/api/activate", json={"cardCode":self._cdk,"country":_cfg_value(self.config,"country","187")})
                self.activation_id = str(d.get("activationId") or "")+"|"+self._cdk; self.phone = str(d.get("phoneNumber") or "")
            else:
                d = self._json("POST", base+"/api/request", headers={"X-SMS-Access":self._cdk}, json={"country":"187"})
                self.activation_id = str(d.get("activationId") or "")+"|"+self._cdk; self.phone = str(d.get("phone") or "")
            if not self.activation_id or not self.phone: raise SMSProviderError("申请号码响应缺少号码或订单")
            if not self.phone.startswith("+"): self.phone = "+"+re.sub(r"\D", "", self.phone)
        else:
            raise SMSProviderError(f"不支持实时接码平台: {p}")
        return self.phone

    def wait_code(self, timeout=120, interval=5):
        deadline = time.time() + timeout
        while time.time() < deadline:
            p = self.provider
            if p == "hero_sms":
                base = _cfg_value(self.config, "base_url", "https://hero-sms.com/stubs/handler_api.php")
                t = self.session.get(base, params={"api_key":_cfg_value(self.config,"api_key"),"action":"getStatus","id":self.activation_id}, timeout=25).text.strip()
                if t.startswith("STATUS_OK:"): return re.sub(r"\D", "", t.split(":",1)[1])
            else:
                base = _cfg_value(self.config, "base_url", {"nextpro":"http://nextpro.top:8080","congou":"https://sapi.congou.eu.cc","chatai":"https://chataiapi.online/sms"}[p]).rstrip("/")
                aid = self.activation_id.split("|",1)[0]
                if p == "nextpro": d = self._json("POST",base+"/api/session/refresh",headers={"Authorization":"Bearer "+self.activation_id},json={})
                elif p == "congou": d = self._json("POST",base+"/api/status",json={"activationId":aid,"cardCode":self._cdk,"country":_cfg_value(self.config,"country","187")})
                else: d = self._json("POST",base+"/api/check",headers={"X-SMS-Access":self._cdk},json={"activationId":aid})
                code = str(d.get("code") or d.get("smsCode") or "").strip()
                if code: return re.sub(r"\D", "", code)
            time.sleep(interval)
        raise SMSProviderError("等待短信验证码超时")

    def release(self, ok=False):
        try:
            p = self.provider
            if p == "hero_sms" and self.activation_id:
                base = _cfg_value(self.config, "base_url", "https://hero-sms.com/stubs/handler_api.php")
                self.session.get(base, params={"api_key":_cfg_value(self.config,"api_key"),"action":"setStatus","id":self.activation_id,"status":"6" if ok else "8"}, timeout=25)
            elif p == "congou" and self.activation_id:
                base = _cfg_value(self.config,"base_url","https://sapi.congou.eu.cc").rstrip("/"); aid=self.activation_id.split("|",1)[0]
                self._json("POST",base+"/api/cancel",json={"activationId":aid,"cardCode":self._cdk,"country":_cfg_value(self.config,"country","187")})
            elif p == "chatai" and self.activation_id:
                base = _cfg_value(self.config,"base_url","https://chataiapi.online/sms").rstrip("/"); aid=self.activation_id.split("|",1)[0]
                self._json("POST",base+("/api/complete" if ok else "/api/cancel"),headers={"X-SMS-Access":self._cdk},json={"activationId":aid})
        except Exception:
            pass
