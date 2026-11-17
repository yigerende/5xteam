"""Authenticated Help inquiry over the existing ChatGPT login session.

No support messages are sent until Go has durably recorded the send intent.
Cookie/proxy snapshots travel only over the private stdio RPC, never diagnostics.
"""
import json
import re
import time
import uuid
from datetime import datetime, timezone
from html.parser import HTMLParser
from urllib.parse import urljoin, urlsplit, parse_qs, urlencode

from . import bridge, upstream

HELP = "https://help.openai.com"
SUPPORT = "https://support.api.openai.com"
AUTH = "https://auth.openai.com"
CHAT = SUPPORT + "/api/v1/chatkit"
PAGE = HELP + "/en/collections/3742473-chatgpt"
ALLOWED = {"help.openai.com", "support.api.openai.com", "auth.openai.com"}
REDIRECTS = {301, 302, 303, 307, 308}


class HelpError(RuntimeError):
    def __init__(self, message, code="help_failed"):
        super().__init__(message)
        self.code = code


class AccountButtons(HTMLParser):
    def __init__(self):
        super().__init__(); self.buttons = []; self.current = None
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "button" and attrs.get("name") == "session_id":
            self.current = [attrs.get("value", ""), ""]
    def handle_data(self, data):
        # Names and email live in adjacent elements with no HTML whitespace.
        # Preserve a boundary so "Name" + "child@example.com" is not one email.
        if self.current is not None: self.current[1] += " " + data
    def handle_endtag(self, tag):
        if tag == "button" and self.current is not None:
            self.buttons.append(self.current); self.current = None


def parse_time(value):
    try:
        dt = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
        return dt.replace(tzinfo=timezone.utc).timestamp() if dt.tzinfo is None else dt.timestamp()
    except (ValueError, TypeError):
        return 0


def message_text(item):
    return "\n".join(c.get("text", "") for c in item.get("content", [])
                     if c.get("type") in {"output_text", "input_text"} and isinstance(c.get("text"), str))


def help_page_identity(html):
    """Read the Help root loader's JSON reference table; never execute page JS.

    HAR exports omit Authorization headers. Help's client obtains its Bearer
    token and signed X-Session-Id from this loader, not from ChatGPT cookies.
    """
    if len(html) > 4 << 20: raise HelpError("Help 登录页面超出限制")
    marker = "window.__reactRouterContext.streamController.enqueue("
    for match in re.finditer(re.escape(marker), html):
        try:
            encoded, _ = json.JSONDecoder().raw_decode(html[match.end():].lstrip())
            table = json.loads(encoded)
            if not isinstance(table, list): continue
            def value(index):
                if type(index) is not int or not 0 <= index < len(table): return None
                return table[index]
            def field(obj, key):
                if not isinstance(obj, dict): return None
                for ref, index in obj.items():
                    if isinstance(ref, str) and re.fullmatch(r"_\d+", ref) and value(int(ref[1:])) == key:
                        return value(index)
                return None
            root = field(field(table[0], "loaderData"), "root")
            user = field(root, "user")
            result = {"email": field(user, "email"), "access_token": field(user, "accessToken"),
                      "session_id_token": field(root, "sessionIdToken"),
                      "session_id": field(root, "helpCenterSessionId")}
            if all(isinstance(v, str) and v and len(v) <= 32768 and not any(c in v for c in "\r\n") for v in result.values()):
                return result
        except (ValueError, TypeError, IndexError, RecursionError):
            continue
    raise HelpError("Help 登录页面未返回支持系统授权凭据")


class HelpInquiry:
    def __init__(self, flow, config):
        self.flow, self.config = flow, config
        self.email = flow.payload["email"].strip().lower()
        self.session_id = str((config.get("session") or {}).get("session_id") or uuid.uuid4())
        self.frame_id = str(uuid.uuid4())
        self.thread = str(config.get("conversation_id") or "")
        self.user_message = str(config.get("user_message_id") or "")
        self.sent_at = parse_time(config.get("sent_at"))
        self.prompt = str(config.get("prompt") or "")
        self.verified = False
        self.access_token = ""
        self.session_id_token = ""

    def checkpoint(self, name, **values):
        return bridge.call("help_" + name, **values)

    def stage(self, stage, status):
        self.checkpoint("stage", stage=stage, status=status)

    def headers(self, json_request=False):
        h = {"Accept": "application/json", "Origin": HELP, "Referer": HELP + "/",
             "x-openai-client": "support-automation", "x-chatkit-locale": "zh-CN", "x-chatkit-version": "rc2",
             "x-help-center-session-id": self.session_id, "x-session-id": self.session_id,
             "chatkit-frame-instance-id": self.frame_id, "x-current-page": PAGE}
        if json_request: h["Content-Type"] = "application/json"
        if self.access_token: h["Authorization"] = "Bearer " + self.access_token
        if self.session_id_token: h["x-session-id"] = self.session_id_token
        return h

    @staticmethod
    def validate_url(url):
        parsed = urlsplit(url)
        if parsed.scheme != "https" or parsed.hostname not in ALLOWED or parsed.port not in (None, 443) or parsed.username is not None:
            raise HelpError("Help 跳转地址不受信任")

    def request(self, url, **kwargs):
        self.validate_url(url)
        # Retry only explicitly read-only calls on the same session/proxy.
        # OAuth callbacks and support writes are never replayed here.
        read_only = kwargs.pop("read_only", False)
        method, timeout = kwargs.pop("method", "GET"), kwargs.pop("timeout", 60)
        headers = self.flow.headers(url, kwargs.pop("headers", {}))
        for attempt in range(3 if read_only else 1):
            try:
                return self.flow.web_request(method, url, headers=headers,
                    allow_redirects=False, timeout=timeout, **kwargs)
            except Exception:
                if not read_only or attempt == 2: raise
                time.sleep(attempt + 1)

    def identity(self, allow_expired=False):
        r = self.request(SUPPORT + "/api/v1/me", headers=self.headers(), read_only=True)
        if r.status_code in (401, 403) and allow_expired: return False
        if r.status_code != 200: raise HelpError(f"Help 身份校验失败：HTTP {r.status_code}")
        try: email = r.json().get("email", "").strip().lower()
        except (ValueError, AttributeError): email = ""
        if not email or email != self.email: raise HelpError("Help 登录账号与当前账号不一致", "help_identity_mismatch")
        self.verified = True
        return True

    def restore(self):
        saved = self.config.get("session") or {}
        if saved.get("email", "").lower() != self.email or saved.get("proxy") != self.flow.proxy_url:
            return False
        self.access_token = str(saved.get("access_token") or "")
        self.session_id_token = str(saved.get("session_id_token") or "")
        bridge.protect_code(self.access_token); bridge.protect_code(self.session_id_token)
        if not self.access_token or not self.session_id_token: return False
        for cookie in saved.get("cookies", []):
            domain = str(cookie.get("domain", "")).lstrip(".")
            if domain not in ALLOWED and domain not in {"openai.com", "chatgpt.com"}: continue
            name, value = cookie.get("name", ""), cookie.get("value", "")
            bridge.protect_code(value)
            if name and value: self.flow.set_cookie(name, value, cookie.get("domain", ""), cookie.get("path", "/"))
        if saved.get("device_id"): self.flow.device_id = saved["device_id"]
        return bool(saved.get("cookies")) and self.identity(allow_expired=True)

    def save_session(self):
        cookies = []
        for c in self.flow.web_session.cookies.jar:
            if c.domain.lstrip(".") in ALLOWED | {"openai.com", "chatgpt.com"}:
                bridge.protect_code(c.value)
                cookies.append({"name": c.name, "value": c.value, "domain": c.domain, "path": c.path})
        self.checkpoint("session", session={"email": self.email, "proxy": self.flow.proxy_url,
            "cookies": cookies, "session_id": self.session_id, "device_id": self.flow.device_id,
            "access_token": self.access_token, "session_id_token": self.session_id_token})

    def load_identity(self, html):
        identity = help_page_identity(html)
        if identity["email"].strip().lower() != self.email:
            raise HelpError("Help 登录账号与当前账号不一致", "help_identity_mismatch")
        self.access_token = identity["access_token"]
        self.session_id_token = identity["session_id_token"]
        self.session_id = identity["session_id"]
        bridge.protect_code(self.access_token); bridge.protect_code(self.session_id_token)

    def redirect_target(self, current, response):
        location = response.headers.get("location", "")
        if not location:
            raise HelpError("Help 跳转缺少地址")
        target = urljoin(current, location)
        self.validate_url(target)
        bridge.emit("help_authorize", "redirect", "Help 页面跳转", http_status=response.status_code,
                    request={"url": bridge.safe_url(current)}, response={"location": bridge.safe_url(target)})
        return target

    def open_page(self, navigation):
        """Warm Help cookies; hand auth redirects to the state-checked flow.

        Only ordinary Help pages are retried as reads. In particular, never
        consume an OAuth callback here or restart an already-started login.
        """
        current = PAGE
        for _ in range(6):
            parsed = urlsplit(current)
            if parsed.hostname == "auth.openai.com" or parsed.path.startswith("/auth/"):
                return current
            if parsed.hostname != "help.openai.com":
                raise HelpError("Help 页面跳转未进入 Help 或授权页面")
            warm = self.request(current, headers={**navigation, "sec-fetch-site": "same-origin"}, read_only=True)
            if warm.status_code == 200:
                return None
            if warm.status_code not in REDIRECTS:
                raise HelpError(f"Help 页面打开失败：HTTP {warm.status_code}")
            current = self.redirect_target(current, warm)
        raise HelpError("Help 页面跳转次数超限")

    def authorize(self):
        self.stage("authorize", "running")
        navigation = {"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                      "Accept-Language": "en-US,en;q=0.9", "Referer": PAGE,
                      "sec-fetch-dest": "document", "sec-fetch-mode": "navigate",
                      "sec-fetch-user": "?1", "Upgrade-Insecure-Requests": "1"}
        # The browser opens Help before clicking login. Establish Help's cookies
        # using the original curl session and its fixed proxy, just like that UI.
        current = self.open_page(navigation)
        # Follow the same callback/landing path as Help's own login UI.
        landing = "/auth/login-success?" + urlencode({"returnTo": urlsplit(PAGE).path})
        current = current or HELP + "/auth/login?" + urlencode({"redirectTo": landing})
        expected_state = ""
        for _ in range(20):
            p = urlsplit(current)
            if p.hostname == "auth.openai.com" and p.path == "/oauth/authorize":
                q = parse_qs(p.query)
                if q.get("redirect_uri", [""])[0] != HELP + "/auth/callback":
                    raise HelpError("Help OAuth 回调地址不匹配")
                expected_state = q.get("state", [""])[0]
            if p.hostname == "help.openai.com" and p.path == "/auth/callback":
                q = parse_qs(p.query)
                if not expected_state or q.get("state", [""])[0] != expected_state or not q.get("code"):
                    raise HelpError("Help OAuth 回调校验失败")
                bridge.protect_code(q["code"][0])
            r = self.request(current, headers={**navigation,
                "sec-fetch-site": "same-origin" if p.hostname == "help.openai.com" else "same-site"})
            if r.status_code in REDIRECTS:
                current = self.redirect_target(current, r); continue
            if r.status_code == 200 and p.hostname == "auth.openai.com" and p.path == "/choose-an-account":
                parser = AccountButtons(); parser.feed(r.text)
                matching = [sid for sid, text in parser.buttons if self.email in
                            re.findall(r"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}", text.lower())]
                if len(matching) != 1: raise HelpError("Help 账号选择页未找到唯一匹配账号")
                sid = matching[0]; bridge.protect_code(sid)
                headers = self.flow.headers(AUTH + "/api/accounts/session/select", {
                    "Accept": "application/json", "Origin": AUTH, "Referer": current})
                if self.flow.sentinel_token: headers["openai-sentinel-token"] = self.flow.sentinel_token
                self.flow._sync_web_session_cookies()
                selected = self.flow.request(AUTH + "/api/accounts/session/select", method="POST",
                    json_data={"session_id": sid}, headers=headers, timeout=60)
                next_url = selected.location() or self.flow.extract_continue_url(selected.json())
                if selected.status not in (200, 301, 302, 303, 307, 308) or not next_url:
                    raise HelpError("Help 账号选择未返回有效回调")
                current = urljoin(current, next_url); continue
            if r.status_code == 200 and p.hostname == "help.openai.com":
                self.load_identity(r.text)
                # Retain the encrypted candidate if the identity read has a
                # transport failure. Every restore revalidates it before send.
                self.save_session(); self.identity(); self.stage("authorize", "completed"); return
            raise HelpError(f"Help 授权未完成：HTTP {r.status_code}（{p.hostname}{p.path}）")
        raise HelpError("Help 授权跳转次数超限")

    def query(self, kind, params):
        if kind not in {"threads.list", "threads.get_by_id", "items.list"}:
            raise HelpError("Help 对话读取类型不支持")
        r = self.request(CHAT, method="POST", json={"type": kind, "params": params}, headers=self.headers(True), read_only=True)
        if r.status_code != 200: raise HelpError(f"Help 对话读取失败：HTTP {r.status_code}")
        try: return r.json()
        except ValueError: raise HelpError("Help 对话返回格式异常")

    def thread_items(self, thread_id):
        data = self.query("threads.get_by_id", {"thread_id": thread_id})
        if data.get("id") != thread_id: raise HelpError("Help 对话标识不匹配")
        items = data.get("items", {})
        if items.get("has_more"):
            items = self.query("items.list", {"thread_id": thread_id, "limit": 100, "order": "desc"})
        if items.get("has_more"):
            raise HelpError("Help 对话过长，需人工核实原提问", "help_reply_unknown")
        return sorted(items.get("data", []), key=lambda x: parse_time(x.get("created_at")))

    def recover(self):
        self.stage("reply", "running")
        threads = [self.thread] if self.thread else [x.get("id") for x in self.query("threads.list", {"limit": 20, "order": "desc"}).get("data", [])]
        exact, candidates = [], []
        for tid in threads:
            if not tid: continue
            items = self.thread_items(tid)
            for index, item in enumerate(items):
                if item.get("thread_id") != tid or item.get("type") != "user_message": continue
                match = (tid, items, index)
                if self.user_message and item.get("id") == self.user_message:
                    exact.append(match)
                # Help can rewrite SSE IDs when persisting conversation history.
                # Accept only one full-text/time match, never a nearby other turn.
                if message_text(item) == self.prompt and self.sent_at - 5 <= parse_time(item.get("created_at")) <= self.sent_at + 600:
                    candidates.append(match)
        matches = exact or candidates
        if len(matches) != 1:
            raise HelpError("上次发送结果不明，未唯一匹配原提问；已停止重复发送", "help_send_unknown")
        tid, items, index = matches[0]
        self.thread, self.user_message = tid, items[index]["id"]
        self.checkpoint("thread", conversation_id=tid, user_message_id=self.user_message)
        answer = None
        for item in items[index+1:]:
            if item.get("thread_id") != tid: continue
            if item.get("type") == "user_message": break
            if item.get("type") == "assistant_message": answer = item
        if answer: return self.accept_answer(answer)
        raise HelpError("Help 原对话尚未返回完整回答", "help_reply_pending")

    def accept_answer(self, item):
        answer = message_text(item)
        if not self.verified or item.get("thread_id") != self.thread or not self.user_message or not answer.strip():
            raise HelpError("Help 完整回复缺少匹配的账号或对话")
        if len(answer.encode("utf-8")) > 65536: raise HelpError("Help 回答过长，需要人工查看", "help_reply_unknown")
        self.checkpoint("reply", answer=answer, message_id=item.get("id", ""), conversation_id=self.thread)
        return {"success": True, "help_completed": True}

    def send(self):
        if not self.verified: raise HelpError("Help 身份尚未校验")
        # Must commit successfully before any potentially mutating request.
        self.checkpoint("sending")
        self.stage("send", "running")
        params = {"input": {"content": [{"type": "input_text", "text": self.prompt}],
                  "quoted_text": "", "attachments": [], "inference_options": {}}}
        if self.thread: params["thread_id"] = self.thread
        headers = self.headers(True)
        headers["x-customer-agents-chat-create-id"] = self.config["request_id"]
        response = self.request(CHAT, method="POST", json={"type": "threads.add_user_message" if self.thread else "threads.create", "params": params},
                                headers=headers, stream=True, timeout=180)
        try:
            if response.status_code != 200:
                raise HelpError(f"Help 提问返回 HTTP {response.status_code}，先核查原对话", "help_send_unknown")
            if "text/event-stream" not in response.headers.get("content-type", ""):
                raise HelpError("Help 未返回回复数据流", "help_send_unknown")
            self.stage("reply", "running")
            data_lines, size, answer = [], 0, None
            def event(text):
                nonlocal answer
                try: e = json.loads(text)
                except ValueError: raise HelpError("Help 回复数据流格式异常", "help_reply_unknown")
                if e.get("type") == "thread.created":
                    tid = e.get("thread", {}).get("id", "")
                    if not tid or self.thread and tid != self.thread: raise HelpError("Help 新对话标识异常")
                    self.thread = tid; self.checkpoint("thread", conversation_id=tid)
                if e.get("type") == "thread.item.done":
                    item = e.get("item", {})
                    if item.get("thread_id") != self.thread: return
                    if item.get("type") == "user_message" and message_text(item) == self.prompt:
                        self.user_message = item.get("id", "")
                        self.checkpoint("thread", conversation_id=self.thread, user_message_id=self.user_message)
                        self.stage("send", "completed")
                    elif item.get("type") == "assistant_message" and self.user_message:
                        answer = item
                if e.get("type") == "error":
                    raise HelpError("Help 回复流返回错误，等待核查原对话", "help_reply_unknown")
            for line in response.iter_lines():
                if isinstance(line, bytes): line = line.decode("utf-8")
                size += len(line.encode("utf-8"))
                if size > 2 << 20: raise HelpError("Help 数据流超出限制", "help_reply_unknown")
                if not line:
                    if data_lines: event("\n".join(data_lines)); data_lines = []
                elif line.startswith("data:"): data_lines.append(line[5:].lstrip())
            if data_lines: event("\n".join(data_lines))
            if answer: return self.accept_answer(answer)
            raise HelpError("Help 连接中断或尚未返回完整回答", "help_reply_pending")
        finally:
            response.close()

    def run(self):
        login_result = None
        try:
            if not self.config.get("prepare_only") and self.restore():
                self.stage("login", "completed"); self.stage("authorize", "completed")
            else:
                self.stage("login", "running")
                if not self.flow.payload.get("password") or not self.flow.payload.get("_totp_secret"):
                    raise HelpError("OAI 确认重新登录需要账号密码和 2FA")
                login_result = self.flow.login()
                self.stage("login", "completed")
                self.authorize()
            if self.config.get("prepare_only"):
                return {**(login_result or {}), "success": True}
            return self.recover() if self.sent_at else self.send()
        except Exception as exc:
            if self.config.get("prepare_only") and login_result:
                self.checkpoint("prepare_failed", error="删除前 Help 授权未完成：" + bridge.redact(exc)[:400])
                return {**login_result, "success": True}
            raise


def run(flow, config):
    try:
        return HelpInquiry(flow, config).run()
    except Exception as exc:
        code = getattr(exc, "code", "help_failed")
        return {"success": False, "retryable": False, "error_code": code,
                "error": bridge.redact(exc)[:600]}
