"""Owner-configured recovery after an uncertain submission; server-side only."""
import time
from urllib.parse import urljoin

from . import bridge, upstream
from .adapter import ProjectProtocolLogin
from .merge_v2 import TeamProtocolLogin


class PersonalLogin(ProjectProtocolLogin):
    # Retain the existing password/TOTP, cookies, proxy and callback mechanics.
    follow_callback = TeamProtocolLogin.follow_callback

    def __init__(self, job_id, payload):
        super().__init__(job_id, payload)
        self.workspace_selected = False

    def trace(self, event, message, **details):
        bridge.emit("merge_v2_recovery", event, "个人空间登录回调核对", details=details)


    def log(self, step, message, level="info"):
        if step == "waiting_code":
            raise RuntimeError("additional verification required")
        super().log(step, message, level)

    def submit_workspace_and_org(self, referer_url):
        data = self.decode_oauth_session_cookie()
        rows = data.get("workspaces", []) if isinstance(data, dict) else []
        rows = [r for r in rows if isinstance(r, dict) and isinstance(r.get("id"), str) and r["id"] != self.payload["excluded_team"]]
        personal = [r for r in rows if r.get("id") == self.payload.get("personal_id") or r.get("is_personal") is True or r.get("type") == "personal" or r.get("structure") == "personal"]
        choices = personal or rows
        if len(choices) != 1:
            raise RuntimeError("personal workspace not uniquely identified")
        self.workspace_selected = True
        url = "https://auth.openai.com/api/accounts/workspace/select"
        response = self.request(url, method="POST", json_data={"workspace_id": choices[0]["id"]},
                                headers=self.headers(url, {"Accept": "application/json", "Origin": "https://auth.openai.com", "Referer": referer_url}), timeout=45)
        if response.status in {301, 302, 303, 307, 308} and response.location():
            return urljoin(url, response.location())
        if response.status != 200:
            raise RuntimeError("personal workspace selection failed")
        next_url = self.extract_continue_url(response.json())
        if not next_url:
            raise RuntimeError("personal workspace continuation missing")
        return self.normalize_auth_url(next_url)


def identity(session, job):
    token = session.get("access_token") or session.get("accessToken") or ""
    claims = upstream.jwt_payload(token)
    auth = claims.get("https://api.openai.com/auth") or {}
    user = session.get("user") or {}
    emails = [e for e in (user.get("email"), claims.get("email"), (claims.get("https://api.openai.com/profile") or {}).get("email")) if e is not None]
    if not emails or any(not isinstance(e, str) or e.casefold() != job["account"]["email"].casefold() for e in emails):
        raise RuntimeError("personal email mismatch")
    account_id = auth.get("chatgpt_account_id")
    if not account_id or account_id == job["mother"]["team_id"] or (session.get("account", {}).get("id") not in (None, account_id)):
        raise RuntimeError("personal account mismatch")
    if auth.get("chatgpt_user_id") != job["child_user_id"]:
        raise RuntimeError("personal user mismatch")
    return token, account_id


def parse_plan(data, account_id):
    # Same live entitlement source as Space mail management's GPT information.
    # Unlike display-only fallbacks, an unknown/missing value cannot settle money.
    if not isinstance(data, dict) or data.get("error"):
        raise ValueError("invalid plan response")
    accounts = data.get("accounts")
    if not isinstance(accounts, dict):
        raise ValueError("missing accounts")
    item = accounts.get(account_id) or accounts.get("default")
    if not isinstance(item, dict):
        raise ValueError("account not found")
    account = item.get("account")
    if not isinstance(account, dict) or account.get("account_id") != account_id or account.get("structure") != "personal":
        raise ValueError("not the authenticated personal account")
    entitlement = item.get("entitlement") or {}
    if not isinstance(entitlement, dict):
        raise ValueError("invalid entitlement")
    aliases = {"chatgptfreeplan": "free", "chatgptfree": "free",
               "chatgptplusplan": "plus", "chatgptplus": "plus",
               "chatgptproplan": "pro", "chatgptpro": "pro",
               "chatgptproliteplan": "prolite", "chatgptprolite": "prolite",
               "chatgptpromaxplan": "promax", "chatgptpromax": "promax", "pro_max": "promax",
               "chatgptgoplan": "go", "chatgptgo": "go", "pro_lite": "prolite"}

    def normalize(raw):
        if raw is None or raw == "":
            return None
        if not isinstance(raw, str):
            raise ValueError("invalid plan")
        value = aliases.get(raw.strip().lower(), raw.strip().lower())
        if value not in {"free", "plus", "pro", "prolite", "promax", "go"}:
            raise ValueError("unknown personal plan")
        return value

    active = entitlement.get("has_active_subscription")
    if active is not None and not isinstance(active, bool):
        raise ValueError("invalid subscription state")
    current = normalize(account.get("plan_type"))
    # A cancelled/expired subscription can retain its old subscription_plan.
    # Only explicit current Free plus boolean inactive may override that label.
    if current == "free" and active is False:
        return "free"
    subscription = normalize(entitlement.get("subscription_plan") or entitlement.get("plan_type"))
    if current and subscription and current != subscription:
        raise ValueError("conflicting personal plan")
    plan = current or subscription
    if plan is None:
        raise ValueError("missing plan")
    if (active is True and plan == "free") or (active is False and plan != "free"):
        raise ValueError("conflicting subscription state")
    return "free" if plan == "free" else "paid"


def check_plan(flow, session, job):
    token, account_id = identity(session, job)
    headers = {"Authorization": "Bearer " + token, "Accept": "application/json", "Origin": "https://chatgpt.com", "Referer": "https://chatgpt.com/", "oai-language": "zh-CN", "oai-device-id": flow.device_id}
    def get(path):
        started = time.monotonic()
        bridge.emit("merge_v2_recovery", "plan_request", "读取个人身份及套餐")
        try:
            response = flow.web_session.get("https://chatgpt.com/backend-api" + path, headers=headers, timeout=20, allow_redirects=False)
        except Exception as exc:
            bridge.emit("merge_v2_recovery", "plan_error", "个人套餐查询失败", level="error", details={"error_type": type(exc).__name__}, duration_ms=int((time.monotonic()-started)*1000))
            raise
        bridge.emit("merge_v2_recovery", "plan_response", "个人套餐查询返回", http_status=response.status_code, duration_ms=int((time.monotonic()-started)*1000))
        if response.status_code != 200:
            raise RuntimeError("plan query failed")
        return response.json()
    me = get("/me")
    if not isinstance(me, dict) or not isinstance(me.get("email"), str) or me["email"].casefold() != job["account"]["email"].casefold():
        raise RuntimeError("live identity mismatch")
    return parse_plan(get("/accounts/check/v4-2023-04-27?timezone_offset_min=-480"), account_id)



def run(flow):
    config = flow.payload["merge_v2_plan"]
    job = {"account": {"email": flow.payload["email"]}, "mother": {"team_id": config["team_id"]}, "child_user_id": config["user_id"]}
    session = flow.login()
    result = check_plan(flow, session, job)
    bridge.emit("merge_v2_recovery", "plan_verified", "个人套餐已核验", details={"plan": result})
    return {"success": True, "plan": result, "user_id": config["user_id"]}
