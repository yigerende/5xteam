"""第三方接码平台适配层。

每个平台一个 Provider 子类，统一两个能力：
  - redeem(raw)        导入时把一条原始输入兑换/解析成手机号记录
  - fetch_code(phone)  拉取一次验证码，返回归一化结果

新接入平台只需新增一个子类并在 _PROVIDERS 注册即可。
所有平台直连（不走代理）。
"""
from __future__ import annotations

import json
import re
import urllib.error
import urllib.request
from abc import ABC, abstractmethod
from typing import Any

_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"


def _http_json(url: str, payload: dict[str, Any] | None = None, timeout: int = 20,
               headers: dict[str, str] | None = None) -> dict[str, Any]:
    """直连请求 JSON。payload 为 None 走 GET，否则 POST。业务错误也常以 JSON 返回，故 HTTPError 也读 body。"""
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(
        url, data=data, method="POST" if payload is not None else "GET",
        headers={"Content-Type": "application/json", "Accept": "application/json", "User-Agent": _UA,
                 **(headers or {})},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        try:
            body = exc.read().decode("utf-8", "replace")
        except Exception:
            return {"_error": f"HTTP {exc.code}"}
    except Exception as exc:
        return {"_error": f"请求失败: {exc}"}
    try:
        parsed = json.loads(body)
        return parsed if isinstance(parsed, dict) else {"_raw": parsed}
    except Exception:
        return {"_error": f"返回非 JSON: {body[:200]}"}


def _http_text(url: str, timeout: int = 20) -> dict[str, Any]:
    """sms-activate 风格 GET，返回纯文本。返回 {ok, text, error}。"""
    req = urllib.request.Request(url, method="GET",
                                 headers={"Accept": "text/plain", "User-Agent": _UA})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", "replace").strip()
    except urllib.error.HTTPError as exc:
        try:
            body = exc.read().decode("utf-8", "replace").strip()
        except Exception:
            return {"ok": False, "text": "", "error": f"HTTP {exc.code}"}
    except Exception as exc:
        return {"ok": False, "text": "", "error": f"请求失败: {exc}"}
    return {"ok": True, "text": body, "error": ""}


_CODE_VALUE_RE = re.compile(r"^\d{4,8}$")
_CODE_KEYS = ("verification_code", "verificationcode", "sms_code", "smscode", "otp", "verify_code", "code")


def extract_code_from_json(obj: Any) -> str:
    """深度遍历 JSON，按关键词找 4-8 位纯数字验证码（顶层 code:0 这类状态码因位数不符会被跳过）。"""
    if isinstance(obj, dict):
        for key in _CODE_KEYS:
            for k, v in obj.items():
                if k.lower() == key and isinstance(v, (str, int)) and _CODE_VALUE_RE.match(str(v).strip()):
                    return str(v).strip()
        for v in obj.values():
            found = extract_code_from_json(v)
            if found:
                return found
    elif isinstance(obj, list):
        for v in obj:
            found = extract_code_from_json(v)
            if found:
                return found
    return ""


class SmsProvider(ABC):
    key: str = ""
    name: str = ""

    @abstractmethod
    def redeem(self, raw: str) -> dict[str, Any]:
        """把一条导入原始输入兑换/解析成手机号记录。
        返回 {ok, phone_number, card_code, api_url, lease_expires_at, message}。"""

    @abstractmethod
    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        """拉取一次验证码。phone 为 sms_phones 表行。
        返回 {ok, found, code, message, expired, lease_expires_at, raw}。"""

    def config_ready(self, cfg: dict[str, Any]) -> bool:
        """实时平台的配置是否可用来取号（登录取号前检查）。默认要 api_key，卡密制平台覆写。"""
        return bool(cfg.get("api_key"))


class ChongptProvider(SmsProvider):
    key = "chongpt"
    name = "chongpt"
    base_url = "https://chongpt.xyz"

    def redeem(self, raw: str) -> dict[str, Any]:
        code = (raw or "").strip().upper()
        if not code:
            return {"ok": False, "message": "卡密为空"}
        r = _http_json(f"{self.base_url}/api/public/cdk/verify", {"code": code})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        if not r.get("valid"):
            return {"ok": False, "message": r.get("message") or "卡密无效"}
        sms = r.get("sms") or {}
        if not sms.get("phoneNumber"):
            return {"ok": False, "message": "卡密有效但未返回手机号（非接码类 CDK？）"}
        return {
            "ok": True,
            "phone_number": sms["phoneNumber"],
            "card_code": code,
            "api_url": "",
            "lease_expires_at": sms.get("leaseExpiresAt") or "",
            "message": r.get("message") or "",
        }

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        r = _http_json(f"{self.base_url}/api/public/sms/session", {"code": phone.get("card_code") or ""})
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"], "expired": False,
                    "lease_expires_at": "", "raw": r}
        found = r.get("status") == "received" and bool(r.get("verificationCode"))
        return {
            "ok": True,
            "found": found,
            "code": str(r.get("verificationCode") or ""),
            "message": r.get("message") or "",
            "expired": bool(r.get("leaseExpired")),
            "lease_expires_at": r.get("leaseExpiresAt") or "",
            "raw": r,
        }


class Chong10666Provider(SmsProvider):
    """chong.10666.xyz 接码：卡密制，与 chongpt 同形态但接口/字段不同。
    verify: POST /api/sms/card/verify {cardCode} -> {ok, card:{code, phone}}
    fetch : POST /api/sms/fetch       {cardCode} -> {ok, phone, hasSms, code, content}
            无短信时 content 含「过期时间：YYYY-MM-DD HH:MM:SS」。"""
    key = "chong10666"
    name = "10666接码"
    base_url = "https://chong.10666.xyz/api"

    def redeem(self, raw: str) -> dict[str, Any]:
        code = (raw or "").strip()
        if not code:
            return {"ok": False, "message": "卡密为空"}
        r = _http_json(f"{self.base_url}/sms/card/verify", {"cardCode": code})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        if not r.get("ok"):
            return {"ok": False, "message": r.get("message") or "卡密无效"}
        card = r.get("card") if isinstance(r.get("card"), dict) else {}
        # 平台返回的号已含国家码（如美国号 12679010937 = +1 267 901 0937），仅补 '+' 即为 E.164。
        digits = re.sub(r"\D+", "", str(card.get("phone") or ""))
        phone = "+" + digits if digits else ""
        if not phone:
            return {"ok": False, "message": "卡密有效但未返回手机号"}
        return {"ok": True, "phone_number": phone, "card_code": code, "api_url": "",
                "lease_expires_at": "", "message": r.get("message") or ""}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        r = _http_json(f"{self.base_url}/sms/fetch", {"cardCode": phone.get("card_code") or ""})
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"], "expired": False,
                    "lease_expires_at": "", "raw": r}
        if not r.get("ok"):
            return {"ok": False, "found": False, "code": "", "message": r.get("message") or "取码失败",
                    "expired": False, "lease_expires_at": "", "raw": r}
        content = str(r.get("content") or "")
        # 无短信时把租期到期时间从 content 里解析出来回填
        m = re.search(r"过期时间[:：]\s*([\d\-: ]+)", content)
        lease = m.group(1).strip() if m else ""
        return {
            "ok": True,
            "found": bool(r.get("hasSms")) and bool(r.get("code")),
            "code": str(r.get("code") or ""),
            "message": content,
            "expired": False,
            "lease_expires_at": lease,
            "raw": r,
        }


class GenericProvider(SmsProvider):
    """通用「手机号----接码URL」格式，覆盖 sms8.net 等大部分平台。"""
    key = "generic"
    name = "通用接码"

    def redeem(self, raw: str) -> dict[str, Any]:
        line = (raw or "").strip()
        m = re.match(r"^(\+?\d[\d\s-]*?)-{2,}(https?://\S+)$", line)
        if not m:
            return {"ok": False, "message": f"格式不符（应为 手机号----URL）: {line[:60]}"}
        phone = re.sub(r"[\s-]", "", m.group(1))
        return {"ok": True, "phone_number": phone, "card_code": "", "api_url": m.group(2),
                "lease_expires_at": "", "message": ""}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        url = phone.get("api_url") or ""
        if not url:
            return {"ok": False, "found": False, "code": "", "message": "缺少接码 URL", "expired": False,
                    "lease_expires_at": "", "raw": {}}
        r = _http_json(url)
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"], "expired": False,
                    "lease_expires_at": "", "raw": r}
        code = extract_code_from_json(r)
        data = r.get("data") if isinstance(r.get("data"), dict) else {}
        expires = str(data.get("expired_date") or "")
        return {
            "ok": True,
            "found": bool(code),
            "code": code,
            "message": str(r.get("msg") or r.get("message") or ""),
            "expired": False,
            "lease_expires_at": expires,
            "raw": r,
        }


class HeroSmsProvider(SmsProvider):
    """hero-sms：sms-activate 风格「实时申请」接码（B 模式，不进号池）。
    登录时 getNumber 取号 → getStatus 收码 → setStatus 完成(6)/取消(8) 释放。
    config: {api_key, base_url, service, country}。activation_id 借道 phone['card_code'] 透传，
    fetch_code/release 从 phone dict 里读回 config（登录时的号源 dict 带着这些字段）。"""
    key = "hero_sms"
    name = "hero-sms(实时)"
    realtime = True
    default_base = "https://hero-sms.com/stubs/handler_api.php"

    def redeem(self, raw: str) -> dict[str, Any]:
        return {"ok": False, "message": "hero-sms 为实时申请平台，无需导入号池"}

    def _base(self, config: dict[str, Any]) -> str:
        return str(config.get("base_url") or "").strip() or self.default_base

    def _call(self, config: dict[str, Any], action: str,
              extra: dict[str, Any] | None = None, timeout: int = 20) -> dict[str, Any]:
        params = {"api_key": str(config.get("api_key") or "").strip(), "action": action}
        if extra:
            params.update({k: v for k, v in extra.items() if v not in (None, "")})
        url = self._base(config) + "?" + urllib.parse.urlencode(params)
        return _http_text(url, timeout=timeout)

    @staticmethod
    def _norm_phone(raw: str) -> str:
        digits = re.sub(r"\D+", "", str(raw or ""))
        return "+" + digits if digits else ""

    def acquire(self, config: dict[str, Any]) -> dict[str, Any]:
        """getNumber → ACCESS_NUMBER:<id>:<phone>。返回 {ok, phone_number(+E164), activation_id}。"""
        r = self._call(config, "getNumber", {
            "service": config.get("service"), "country": config.get("country"),
            # hero-sms 必须带 maxPrice，否则即使有库存也返回 NO_NUMBERS
            "maxPrice": str(config.get("max_price") or "1").strip(),
        })
        if not r.get("ok"):
            return {"ok": False, "message": r.get("error") or "申请号码失败"}
        text = r.get("text") or ""
        if text.startswith("ACCESS_NUMBER"):
            parts = text.split(":")
            act_id = parts[1] if len(parts) > 1 else ""
            phone = self._norm_phone(parts[2] if len(parts) > 2 else "")
            if act_id and phone:
                return {"ok": True, "phone_number": phone, "activation_id": act_id,
                        "lease_expires_at": "", "message": ""}
        try:  # 兼容 JSON 返回
            j = json.loads(text)
            act_id = str(j.get("activationId") or j.get("id") or "")
            phone = self._norm_phone(j.get("phoneNumber") or j.get("phone") or "")
            if act_id and phone:
                return {"ok": True, "phone_number": phone, "activation_id": act_id,
                        "lease_expires_at": "", "message": ""}
        except Exception:
            pass
        return {"ok": False, "message": f"申请号码失败: {text[:120] or '空返回'}"}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        """getStatus → STATUS_OK:<code> / STATUS_WAIT_CODE。config 从 phone dict 透传。"""
        act_id = str(phone.get("card_code") or phone.get("activation_id") or "")
        if not act_id:
            return {"ok": False, "found": False, "code": "", "message": "缺少 activation_id",
                    "expired": False, "lease_expires_at": "", "raw": {}}
        r = self._call(phone, "getStatus", {"id": act_id})
        if not r.get("ok"):
            return {"ok": False, "found": False, "code": "", "message": r.get("error") or "取码失败",
                    "expired": False, "lease_expires_at": "", "raw": r}
        text = r.get("text") or ""
        code = ""
        if text.startswith("STATUS_OK"):
            parts = text.split(":", 1)
            code = re.sub(r"\D+", "", parts[1]) if len(parts) > 1 else ""
        elif text.startswith("{"):
            try:
                code = extract_code_from_json(json.loads(text))
            except Exception:
                code = ""
        expired = text.startswith("STATUS_CANCEL") or "NO_ACTIVATION" in text
        return {"ok": True, "found": bool(code), "code": code, "message": text[:120],
                "expired": expired, "lease_expires_at": "", "raw": r}

    def release(self, phone: dict[str, Any], ok: bool) -> dict[str, Any]:
        """setStatus：成功=6(完成)，失败/换号=8(取消)。
        注意 hero-sms 有最小激活期(约 120s)，期内取消会返回 JSON EARLY_CANCEL_DENIED，
        此时号会在超时后自动释放退款，不视为致命错误（尽力而为）。"""
        act_id = str(phone.get("card_code") or phone.get("activation_id") or "")
        if not act_id:
            return {"ok": False, "message": "缺少 activation_id"}
        r = self._call(phone, "setStatus", {"id": act_id, "status": "6" if ok else "8"})
        text = str(r.get("text") or r.get("error") or "")
        return {"ok": text.startswith("ACCESS_"), "message": text[:160]}

    def get_balance(self, config: dict[str, Any]) -> dict[str, Any]:
        """getBalance → ACCESS_BALANCE:<amount>。"""
        r = self._call(config, "getBalance")
        if not r.get("ok"):
            return {"ok": False, "balance": "", "message": r.get("error") or "查询失败"}
        text = r.get("text") or ""
        if text.startswith("ACCESS_BALANCE"):
            parts = text.split(":", 1)
            return {"ok": True, "balance": parts[1] if len(parts) > 1 else "", "message": ""}
        return {"ok": False, "balance": "", "message": text[:120] or "查询失败"}


class NextproProvider(SmsProvider):
    """nextpro(速验)：CDK 卡密制实时接码,一卡一单(15 分钟),号码为 +1 美国号。
    POST /api/redeem {cdk} → {token, activation{phone,country,expiresAt,status,code}};
    之后 Bearer token:POST /api/session/refresh 轮询收码,GET /api/session 查当前单。
    config: {base_url, cdks(list,剩余卡密)}。acquire 消耗一张卡,返回 consumed_cdk 由调用方持久化扣减。
    平台无取消端点,release 为空操作(到期自动过期)。注意:API 按 UA 过滤,必须带浏览器 UA。"""
    key = "nextpro"
    name = "nextpro(实时)"
    realtime = True
    card_pool = True  # 卡密池制:取号走「锁内逐卡兑换、成功才扣卡」逻辑
    default_base = "http://nextpro.top:8080"

    def redeem(self, raw: str) -> dict[str, Any]:
        return {"ok": False, "message": "nextpro 为实时申请平台,无需导入号池"}

    def _base(self, config: dict[str, Any]) -> str:
        return str(config.get("base_url") or "").strip() or self.default_base

    def _call(self, config: dict[str, Any], path: str, payload: dict[str, Any] | None = None,
              token: str = "", timeout: int = 20) -> dict[str, Any]:
        headers = {"Authorization": f"Bearer {token}"} if token else None
        return _http_json(self._base(config) + path, payload, timeout=timeout, headers=headers)

    def config_ready(self, cfg: dict[str, Any]) -> bool:
        return bool(cfg.get("cdks"))

    def acquire(self, config: dict[str, Any]) -> dict[str, Any]:
        """取剩余卡密第一张兑换。返回 {ok, phone_number(+E164), activation_id(=token), consumed_cdk}。"""
        cdks = [str(c).strip() for c in (config.get("cdks") or []) if str(c).strip()]
        if not cdks:
            return {"ok": False, "message": "nextpro 卡密池已空,请在接码管理页补充 CDK"}
        cdk = cdks[0]
        r = self._call(config, "/api/redeem", {"cdk": cdk})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        act = r.get("activation") if isinstance(r.get("activation"), dict) else {}
        token = str(r.get("token") or "")
        phone = str(act.get("phone") or "").strip()
        if phone and not phone.startswith("+"):
            phone = "+" + re.sub(r"\D+", "", phone)
        if not (token and phone):
            # CF 5xx 等错误页也是 JSON({type,title,status}),取 title+状态码而不是 dump 整个 dict
            err = r.get("error") or r.get("message") or r.get("_error")
            if not err:
                status = r.get("status") or r.get("status_code")
                title = str(r.get("title") or "")
                err = f"HTTP {status} {title}".strip() if status else str(r)[:120]
            return {"ok": False, "message": f"兑换失败: {str(err)[:120]}"}
        return {"ok": True, "phone_number": phone, "activation_id": token,
                "consumed_cdk": cdk,
                "lease_expires_at": str(act.get("expiresAt") or ""), "message": ""}

    def replace(self, phone: dict[str, Any]) -> dict[str, Any]:
        """session/replace:同一张卡换一个新号(不耗卡,有 ~20s 换号冷却)。token 复用。"""
        token = str(phone.get("card_code") or phone.get("activation_id") or "")
        if not token:
            return {"ok": False, "message": "缺少 token"}
        r = self._call(phone, "/api/session/replace", {}, token=token)
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        new_phone = str(r.get("phone") or "").strip()
        if new_phone and not new_phone.startswith("+"):
            new_phone = "+" + re.sub(r"\D+", "", new_phone)
        if not new_phone:
            return {"ok": False, "message": f"换号失败: {str(r.get('error') or r)[:120]}"}
        return {"ok": True, "phone_number": new_phone, "activation_id": token,
                "lease_expires_at": str(r.get("expiresAt") or ""), "message": ""}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        """session/refresh 轮询:status=received 且 code 非空即取到。"""
        token = str(phone.get("card_code") or phone.get("activation_id") or "")
        if not token:
            return {"ok": False, "found": False, "code": "", "message": "缺少 token",
                    "expired": False, "lease_expires_at": "", "raw": {}}
        r = self._call(phone, "/api/session/refresh", {}, token=token)
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"],
                    "expired": False, "lease_expires_at": "", "raw": r}
        status = str(r.get("status") or "")
        code = str(r.get("code") or "").strip()
        return {
            "ok": True,
            "found": status == "received" and bool(code),
            "code": code,
            "message": str(r.get("message") or status or "等待短信"),
            "expired": status in ("expired", "cancelled"),
            "lease_expires_at": str(r.get("expiresAt") or ""),
            "raw": r,
        }

    def release(self, phone: dict[str, Any], ok: bool) -> dict[str, Any]:
        """平台无取消端点,订单 15 分钟自动过期,无需操作。"""
        return {"ok": True, "message": "nextpro 无需释放(到期自动过期)"}

    def get_balance(self, config: dict[str, Any]) -> dict[str, Any]:
        """卡密制无账户余额,返回剩余卡数。"""
        cdks = [c for c in (config.get("cdks") or []) if str(c).strip()]
        return {"ok": True, "balance": f"{len(cdks)} 张卡", "message": ""}


class CongouProvider(SmsProvider):
    """congou(sapi.congou.eu.cc,卡密接码平台)：CDK 卡密制实时接码,一卡一单,美国 +1 号。
    POST /api/activate {cardCode,country} → {activationId,phoneNumber};
    /api/status 轮询收码(同号最多 maxSmsCount=3 条,keepWaiting=true 表示还可能有下一条);
    /api/change-number 同卡换号(不耗卡);/api/cancel 取消(激活后 ~120s 冷却,期内取消报错,尽力而为)。
    同卡重复 activate 返回当前进行中的订单(天然幂等)。
    config: {base_url, country(默认 187=美国), cdks(list)}。
    activation_id 复合编码为 "activationId|cardCode",fetch/replace/release 时拆回。"""
    key = "congou"
    name = "congou(实时)"
    realtime = True
    card_pool = True
    default_base = "https://sapi.congou.eu.cc"

    def redeem(self, raw: str) -> dict[str, Any]:
        return {"ok": False, "message": "congou 为实时申请平台，无需导入号池"}

    def _base(self, config: dict[str, Any]) -> str:
        return str(config.get("base_url") or "").strip() or self.default_base

    def _call(self, config: dict[str, Any], path: str, payload: dict[str, Any]) -> dict[str, Any]:
        return _http_json(self._base(config) + path, payload, timeout=25)

    def config_ready(self, cfg: dict[str, Any]) -> bool:
        return bool(cfg.get("cdks"))

    @staticmethod
    def _pack(activation_id: Any, cdk: str) -> str:
        return f"{activation_id}|{cdk}"

    @staticmethod
    def _unpack(packed: str) -> tuple[str, str]:
        parts = str(packed or "").split("|", 1)
        return parts[0], (parts[1] if len(parts) > 1 else "")

    def acquire(self, config: dict[str, Any]) -> dict[str, Any]:
        cdks = [str(c).strip() for c in (config.get("cdks") or []) if str(c).strip()]
        if not cdks:
            return {"ok": False, "message": "congou 卡密池已空，请在接码管理页补充 CDK"}
        cdk = cdks[0]
        country = str(config.get("country") or "").strip() or "187"
        r = self._call(config, "/api/activate", {"cardCode": cdk, "country": country})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        if not r.get("ok"):
            return {"ok": False, "message": f"兑换失败: {str(r.get('error') or r.get('message') or r)[:120]}"}
        act_id = str(r.get("activationId") or "")
        phone = str(r.get("phoneNumber") or "").strip()
        if phone and not phone.startswith("+"):
            phone = "+" + re.sub(r"\D+", "", phone)
        if not (act_id and phone):
            return {"ok": False, "message": "兑换成功但缺少 activationId/phoneNumber"}
        return {"ok": True, "phone_number": phone, "activation_id": self._pack(act_id, cdk),
                "consumed_cdk": cdk, "lease_expires_at": "", "message": ""}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        act_id, cdk = self._unpack(str(phone.get("card_code") or phone.get("activation_id") or ""))
        if not (act_id and cdk):
            return {"ok": False, "found": False, "code": "", "message": "缺少 activationId/cardCode",
                    "expired": False, "lease_expires_at": "", "raw": {}}
        r = self._call(phone, "/api/status", {
            "activationId": act_id, "cardCode": cdk,
            "country": str(phone.get("country") or "").strip() or "187"})
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"],
                    "expired": False, "lease_expires_at": "", "raw": r}
        status = str(r.get("status") or "")
        code = str(r.get("code") or "").strip()
        return {
            "ok": True,
            "found": bool(code),
            "code": code,
            "message": str(r.get("message") or status or "等待短信"),
            "expired": status in ("cancelled", "expired", "superseded"),
            "lease_expires_at": "",
            "raw": r,
        }

    def replace(self, phone: dict[str, Any]) -> dict[str, Any]:
        """change-number:同卡换新号(不耗卡)。返回新的复合 activation_id。"""
        act_id, cdk = self._unpack(str(phone.get("card_code") or phone.get("activation_id") or ""))
        if not (act_id and cdk):
            return {"ok": False, "message": "缺少 activationId/cardCode"}
        r = self._call(phone, "/api/change-number", {
            "activationId": act_id, "cardCode": cdk,
            "country": str(phone.get("country") or "").strip() or "187"})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        if not r.get("ok"):
            return {"ok": False, "message": f"换号失败: {str(r.get('error') or r.get('message') or r)[:120]}"}
        new_id = str(r.get("activationId") or "")
        new_phone = str(r.get("phoneNumber") or "").strip()
        if new_phone and not new_phone.startswith("+"):
            new_phone = "+" + re.sub(r"\D+", "", new_phone)
        if not (new_id and new_phone):
            return {"ok": False, "message": "换号成功但缺少 activationId/phoneNumber"}
        return {"ok": True, "phone_number": new_phone, "activation_id": self._pack(new_id, cdk),
                "lease_expires_at": "", "message": ""}

    def release(self, phone: dict[str, Any], ok: bool) -> dict[str, Any]:
        """cancel:激活后 ~120s 冷却期内会报错(waitSeconds),尽力而为不视为致命。"""
        act_id, cdk = self._unpack(str(phone.get("card_code") or phone.get("activation_id") or ""))
        if not (act_id and cdk):
            return {"ok": False, "message": "缺少 activationId/cardCode"}
        r = self._call(phone, "/api/cancel", {
            "activationId": act_id, "cardCode": cdk,
            "country": str(phone.get("country") or "").strip() or "187"})
        return {"ok": bool(r.get("ok")), "message": str(r.get("message") or r.get("error") or "")[:160]}

    def get_balance(self, config: dict[str, Any]) -> dict[str, Any]:
        cdks = [c for c in (config.get("cdks") or []) if str(c).strip()]
        return {"ok": True, "balance": f"{len(cdks)} 张卡", "message": ""}


class ChataiProvider(SmsProvider):
    """chatai(chataiapi.online/sms)：CDK 卡密制实时接码,一卡一单 3 分钟,美国 +1 号。
    全部 POST + 头 X-SMS-Access:<cdk>:
      /api/request {country} → {activationId,phone,timeoutAt(3min)}
      /api/check {activationId} → {status: waiting|received|canceled|completed, code}
      /api/cancel → 取消并【立即退卡】(可重新取号);/api/complete → 核销。
    国家写死 187=美国。cancel 即退卡:不提供 replace——换号走「release 取消退卡回池 → 重新 acquire」。
    activation_id 复合编码 "activationId|cdk"(check/cancel 都要 cdk 做鉴权头)。"""
    key = "chatai"
    name = "chatai(实时)"
    realtime = True
    card_pool = True
    default_base = "https://chataiapi.online/sms"

    def redeem(self, raw: str) -> dict[str, Any]:
        return {"ok": False, "message": "chatai 为实时申请平台，无需导入号池"}

    def _base(self, config: dict[str, Any]) -> str:
        return str(config.get("base_url") or "").strip() or self.default_base

    def _call(self, config: dict[str, Any], path: str, cdk: str,
              payload: dict[str, Any]) -> dict[str, Any]:
        return _http_json(self._base(config) + path, payload, timeout=25,
                          headers={"X-SMS-Access": cdk})

    def config_ready(self, cfg: dict[str, Any]) -> bool:
        return bool(cfg.get("cdks"))

    @staticmethod
    def _pack(activation_id: Any, cdk: str) -> str:
        return f"{activation_id}|{cdk}"

    @staticmethod
    def _unpack(packed: str) -> tuple[str, str]:
        parts = str(packed or "").split("|", 1)
        return parts[0], (parts[1] if len(parts) > 1 else "")

    def acquire(self, config: dict[str, Any]) -> dict[str, Any]:
        cdks = [str(c).strip() for c in (config.get("cdks") or []) if str(c).strip()]
        if not cdks:
            return {"ok": False, "message": "chatai 卡密池已空，请在接码管理页补充 CDK"}
        cdk = cdks[0]
        r = self._call(config, "/api/request", cdk, {"country": "187"})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        if not r.get("ok"):
            st = str(r.get("status") or "")
            msg = str(r.get("message") or r.get("error") or "")
            # 对齐服务端失败分类:used=废卡删池,locked=有活动订单跳卡保留
            if st == "used":
                return {"ok": False, "message": f"兑换失败: 兑换码已使用（{msg[:60]}）"}
            if st == "locked":
                return {"ok": False, "message": f"兑换失败: 兑换正在处理中（{msg[:60]}）"}
            return {"ok": False, "message": f"兑换失败: {msg[:120] or st or '未知错误'}"}
        act_id = str(r.get("activationId") or "")
        phone = str(r.get("phone") or "").strip()
        if phone and not phone.startswith("+"):
            phone = "+" + re.sub(r"\D+", "", phone)
        if not (act_id and phone):
            return {"ok": False, "message": "取号成功但缺少 activationId/phone"}
        return {"ok": True, "phone_number": phone, "activation_id": self._pack(act_id, cdk),
                "consumed_cdk": cdk, "lease_expires_at": str(r.get("timeoutAt") or ""), "message": ""}

    def fetch_code(self, phone: dict[str, Any]) -> dict[str, Any]:
        act_id, cdk = self._unpack(str(phone.get("card_code") or phone.get("activation_id") or ""))
        if not (act_id and cdk):
            return {"ok": False, "found": False, "code": "", "message": "缺少 activationId/cdk",
                    "expired": False, "lease_expires_at": "", "raw": {}}
        r = self._call(phone, "/api/check", cdk, {"activationId": act_id})
        if r.get("_error"):
            return {"ok": False, "found": False, "code": "", "message": r["_error"],
                    "expired": False, "lease_expires_at": "", "raw": r}
        status = str(r.get("status") or "")
        code = str(r.get("code") or r.get("smsCode") or "").strip()
        if not code and status == "received":
            code = extract_code_from_json(r)
        remaining = r.get("remainingMs")
        expired = status in ("canceled", "cancelled", "expired", "timeout", "completed") or (
            isinstance(remaining, (int, float)) and remaining <= 0)
        return {
            "ok": True,
            "found": bool(code),
            "code": code,
            "message": str(r.get("message") or status or "等待短信"),
            "expired": expired,
            "lease_expires_at": str(r.get("timeoutAt") or ""),
            "raw": r,
        }

    def release(self, phone: dict[str, Any], ok: bool) -> dict[str, Any]:
        """ok=complete 核销;不 ok=cancel 立即退卡,返回 refunded+cdk 由调用方加回卡池。"""
        act_id, cdk = self._unpack(str(phone.get("card_code") or phone.get("activation_id") or ""))
        if not (act_id and cdk):
            return {"ok": False, "message": "缺少 activationId/cdk"}
        path = "/api/complete" if ok else "/api/cancel"
        r = self._call(phone, path, cdk, {"activationId": act_id})
        if r.get("_error"):
            return {"ok": False, "message": r["_error"]}
        success = bool(r.get("ok"))
        return {"ok": success, "refunded": success and not ok, "cdk": cdk if success and not ok else "",
                "message": str(r.get("message") or r.get("status") or "")[:160]}

    def get_balance(self, config: dict[str, Any]) -> dict[str, Any]:
        cdks = [c for c in (config.get("cdks") or []) if str(c).strip()]
        return {"ok": True, "balance": f"{len(cdks)} 张卡", "message": ""}


# 注册表：新增平台在此追加实例即可
_PROVIDERS: dict[str, SmsProvider] = {p.key: p for p in [
    ChongptProvider(), Chong10666Provider(), GenericProvider(), HeroSmsProvider(),
    NextproProvider(), CongouProvider(), ChataiProvider()]}


def get_sms_provider(key: str) -> SmsProvider | None:
    return _PROVIDERS.get((key or "").strip().lower())


def list_sms_providers() -> list[dict[str, Any]]:
    return [{"key": p.key, "name": p.name, "realtime": bool(getattr(p, "realtime", False))}
            for p in _PROVIDERS.values()]
