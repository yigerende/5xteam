"""Keep project diagnostics, dead-account handling and SMS persistence local."""
import json
import re
from urllib.parse import urlsplit

from . import bridge, upstream


def cookie_snapshot(flow):
    return sorted([{"name": c.name, "domain": c.domain, "path": c.path, "secure": c.secure}
                   for c in flow.cookie_jar], key=lambda c: (c["name"], c["domain"], c["path"]))


def response_shape(response, flow):
    data = response.json()
    page = data.get("page") if isinstance(data.get("page"), dict) else {}
    error = data.get("error") if isinstance(data.get("error"), dict) else {}
    cookies = cookie_snapshot(flow)
    return {"http_status": response.status, "url": bridge.safe_url(response.url),
            "location": bridge.safe_url(response.location()), "response_keys": sorted(data),
            "page_type": str(page.get("type") or data.get("page_type") or ""),
            "continue_url": bridge.safe_url(flow.extract_continue_url(data)),
            "error_code": str(error.get("code") or ""), "error_type": str(error.get("type") or ""),
            "error_message": bridge.redact(error.get("message") or "")[:500],
            "cookie_jar": cookies,
            "auth_session_cookie_present": any(c["name"] == "oai-client-auth-session" for c in cookies),
            "login_session_cookie_present": any(c["name"] == "login_session" for c in cookies)}


class ProjectProtocolLogin(upstream.ChatGPTProtocolLogin):
    def __init__(self, job_id, payload):
        super().__init__(job_id, payload)
        self.last_stage = "oauth_init"
        self.last_status = 0

    def log(self, step, message, level="info"):
        self.last_stage = step
        super().log(step, message, level)

    def request(self, url, **kwargs):
        stage = urlsplit(url).path.strip("/").replace("/", "_") or "authorize"
        self.last_stage, self.last_status = stage, 0
        headers = kwargs.get("headers") or {}
        payload = kwargs.get("json_data") or kwargs.get("form_data") or {}
        request = {"method": kwargs.get("method", "GET"), "url": bridge.safe_url(url),
                   "referer": bridge.safe_url(headers.get("Referer")),
                   "payload_fields": sorted(payload), "header_names": sorted(headers),
                   "sentinel_attached": bool(headers.get("openai-sentinel-token")),
                   "impersonate": upstream.OPENAI_IMPERSONATE, "allow_redirects": False,
                   "timeout_seconds": kwargs.get("timeout", 60)}
        bridge.emit(stage, "request_start", "OAuth 请求开始", request=request)
        try:
            response = super().request(url, **kwargs)
        except Exception as exc:
            bridge.emit(stage, "request_error", "OAuth 请求异常", request=request,
                        details={"error": bridge.redact(exc)[:500], "error_type": type(exc).__name__}, level="error")
            raise
        self.last_status = response.status
        bridge.emit(stage, "request_complete", "OAuth 请求已返回", http_status=response.status,
                    request=request, response=response_shape(response, self),
                    level="warning" if response.status >= 400 else "info")
        code = bridge.dead_code(response.text) if response.status >= 400 else ""
        if code:
            raise bridge.DeadAccountError(code, stage, response.status, upstream.protocol_compact_error(response.json()))
        return response

    def resolve_db_phone_source(self, account_email):
        row = bridge.call("sms_pick", provider=str(self.payload.get("sms_provider") or ""),
                          exclude_ids=self.payload.get("_sms_tried_ids") or [])
        if not row:
            self.log("phone_pool", "号码池没有可用手机号（绑满、冷却、过期、停用或正在使用）", "warning")
            return {}
        self.payload["_resolved_sms_phone"] = row
        self.log("phone_pool", "从本项目号码池选取手机号 " + str(row.get("phone_number") or ""))
        return self._source_from_sms_row(row, account_email)

    def _handle_rejected_phone(self, exc):
        phone = self.payload.get("_resolved_sms_phone") or {}
        phone_id = str(phone.get("id") or "")
        tried = self.payload.setdefault("_sms_tried_ids", [])
        if phone_id and phone_id not in tried:
            tried.append(phone_id)
        if phone.get("realtime") or phone.get("activation_id"):
            self._release_realtime_phone(phone, ok=False)
        elif phone_id:
            bridge.call("sms_rejected", id=phone_id, disable=exc.disable, reason=exc.reason)
        self.log("phone_pool", "当前号码未通过，换号重试：" + str(exc.reason), "warning")

    def persist_sms_binding(self):
        phone = self.payload.get("_resolved_sms_phone")
        if not isinstance(phone, dict):
            return
        if phone.get("realtime") or phone.get("activation_id"):
            self._release_realtime_phone(phone, ok=True)
            return
        try:
            bridge.call("sms_bind", id=phone.get("id"))
            self.log("phone_pool", "已保存本项目手机号与账号的绑定", "success")
        except Exception as exc:
            self.log("phone_pool", "写绑定关系失败：" + str(exc), "warning")


def run(payload, rpc=None):
    # Allowlisted input prevents an injected manager URL from enabling remote OAuth.
    email = str(payload.get("email") or "").strip()
    pickup = str(payload.get("pickup_url") or "").strip()
    provider = str(payload.get("sms_provider") or "").strip()
    local = {"email": email, "password": str(payload.get("gpt_password") or ""),
             "_totp_secret": str(payload.get("totp_secret") or ""), "proxy": payload.get("proxy"),
             "job_id": "local", "allow_sms": bool(payload.get("allow_sms", True)),
             "sms_config": payload.get("sms_config") or {}, "credential_mode": "codex_rt",
             "sms_realtime_provider": provider if provider in {"hero_sms", "nextpro", "congou", "chatai"} else "",
             "sms_provider": provider if provider in {"generic", "chongpt", "chong10666"} else "",
             "generic_accounts": [{"email": email, "pickup_url": pickup, "imap_host": pickup,
                                   "mode": "mailtoken" if urlsplit(pickup).fragment else "directurl"}] if pickup else []}
    bridge.configure({**payload, **local}, rpc)
    flow = None
    try:
        flow = ProjectProtocolLogin("local", local)
        session = flow.login()
        token = upstream.jwt_payload(session.get("id_token") or session.get("access_token") or "")
        auth = token.get("https://api.openai.com/auth") or {}
        return {**session, "success": True, "account_id": auth.get("chatgpt_account_id", "")}
    except bridge.DeadAccountError as exc:
        return {"success": False, "dead": True, "status": "deactivated", "retryable": False,
                "error_code": exc.code, "stage": exc.stage, "http_status": exc.status, "error": bridge.redact(exc)}
    except Exception as exc:
        typed = isinstance(exc, upstream.LoginFlowError)
        retryable = bool(exc.retryable) if typed else upstream._is_transient_login_error(str(exc))
        stage = flow.last_stage if flow else "oauth_init"
        status = (exc.status or 0) if typed else (flow.last_status if flow else 0)
        bridge.emit(stage, "exception", "Codex OAuth 失败", http_status=status,
                    details={"error": bridge.redact(exc)[:800], "error_type": type(exc).__name__,
                             "retryable": retryable, "cookie_jar": cookie_snapshot(flow) if flow else []}, level="error")
        return {"success": False, "error": bridge.redact(exc), "retryable": retryable,
                "error_code": exc.code if typed else "login_failed", "stage": stage, "http_status": status,
                "hint": bridge.redact(exc.hint) if typed else ""}
