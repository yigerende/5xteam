import json
import sys
import time
from urllib.parse import unquote, urlparse

from curl_cffi import requests


IMPERSONATE = "chrome131"


def proxy_error(exc: Exception, proxy: str) -> str:
    message = str(exc).replace(proxy, "[proxy]") if proxy else str(exc)
    password = urlparse(proxy).password or ""
    for value in (password, unquote(password)):
        if value:
            message = message.replace(value, "[redacted]")
    return message[:300]


def proxy_shape(proxy: str) -> dict:
    parsed = urlparse(str(proxy or ""))
    return {
        "configured": bool(proxy),
        "scheme": parsed.scheme,
        "endpoint": f"{parsed.hostname or ''}:{parsed.port}" if parsed.port else (parsed.hostname or ""),
    }


def trace_ip(proxy: str) -> dict:
    # Independent requests match the manager and test stability across connections.
    response = requests.get(
        "https://www.cloudflare.com/cdn-cgi/trace",
        proxies={"http": proxy, "https": proxy},
        impersonate=IMPERSONATE,
        timeout=12,
    )
    values = {}
    for line in response.text.splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip()
    return {
        "status": int(response.status_code or 0),
        "ip": str(values.get("ip") or "").strip(),
        "loc": str(values.get("loc") or "").strip(),
        "colo": str(values.get("colo") or "").strip(),
    }


def probe_proxy(proxy: str) -> dict:
    proxy = str(proxy or "").strip()
    if proxy.lower().startswith("socks5://"):
        proxy = "socks5h://" + proxy[len("socks5://"):]
    if not proxy:
        return {
            "ok": False,
            "retryable": False,
            "error_code": "proxy_required",
            "stage": "proxy_check",
            "error": "OAuth 代理不能为空",
            "proxy": proxy_shape(proxy),
        }

    result = {
        "ok": False,
        "retryable": True,
        "error_code": "proxy_quality_failed",
        "stage": "proxy_check",
        "http_status": 0,
        "proxy": proxy_shape(proxy),
        "ip_check_status": "not_checked",
    }
    try:
        probe = requests.get(
            "https://auth.openai.com/",
            proxies={"http": proxy, "https": proxy},
            impersonate=IMPERSONATE,
            timeout=20,
            allow_redirects=False,
        )
        status = int(probe.status_code or 0)
        result["http_status"] = status
        # This intentionally matches gpt-account-manager: auth.openai.com
        # root 403 is allowed; 429, 5xx, and connection failures are not.
        if status == 0 or status == 429 or status >= 500:
            result["error"] = (
                f"HTTP {status}（疑似 CF/风控拒绝）"
                if status
                else "auth.openai.com 连接失败"
            )
            return result
        result["auth_status"] = status
    except Exception as exc:
        result["error"] = proxy_error(exc, proxy)
        error_text = str(exc).lower()
        result["error_code"] = (
            "proxy_dns_failed"
            if "resolve proxy" in error_text or "name or service not known" in error_text
            else "proxy_connection_failed"
        )
        return result

    try:
        first = trace_ip(proxy)
        first_ip = first["ip"]
        result["egress_first"] = first
        result.update({"exit_ip": first_ip, "loc": first.get("loc", ""), "colo": first.get("colo", "")})
        time.sleep(0.8)
        confirm = trace_ip(proxy)
        result["egress_confirm"] = confirm
        confirm_ip = confirm["ip"]
        if first_ip and confirm_ip and first_ip != confirm_ip:
            result.update({
                "error_code": "proxy_ip_unstable",
                "ip_check_status": "unstable",
                "error": f"代理出口不稳定：同一账号会话检测到 {first_ip} -> {confirm_ip}",
            })
            return result
        result.update({
            "ok": True,
            "retryable": False,
            "error_code": "",
            "error": "",
            "ip_check_status": "consistent" if first_ip and confirm_ip else "unavailable",
        })
    except Exception as exc:
        # Match the manager: the optional IP trace is diagnostic only. An
        # unavailable trace must not reject an otherwise reachable auth route.
        result.update({"ok": True, "retryable": False, "error": "", "error_code": "",
                       "ip_check_status": "unavailable",
                       "trace_error": proxy_error(exc, proxy)})
    return result


def main() -> None:
    proxy = sys.argv[1] if len(sys.argv) > 1 else ""
    print(json.dumps(probe_proxy(proxy), ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({
            "ok": False,
            "retryable": True,
            "error_code": "proxy_probe_failed",
            "stage": "proxy_check",
            "error": f"{type(exc).__name__}: {exc}",
        }, ensure_ascii=False))
