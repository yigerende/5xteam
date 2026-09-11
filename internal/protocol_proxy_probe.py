import json
import sys
from urllib.parse import urlparse

from curl_cffi import requests


IMPERSONATE = "chrome131"


def proxy_shape(proxy: str) -> dict:
    parsed = urlparse(str(proxy or ""))
    return {
        "configured": bool(proxy),
        "scheme": parsed.scheme,
        "endpoint": f"{parsed.hostname or ''}:{parsed.port}" if parsed.port else (parsed.hostname or ""),
    }


def trace_ip(session: requests.Session, proxy: str) -> dict:
    response = session.get(
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


def main() -> None:
    proxy = str(sys.argv[1] if len(sys.argv) > 1 else "").strip()
    if not proxy:
        print(json.dumps({
            "ok": False,
            "retryable": False,
            "error_code": "proxy_required",
            "stage": "proxy_check",
            "error": "OAuth 代理不能为空",
            "proxy": proxy_shape(proxy),
        }, ensure_ascii=False))
        return

    result = {
        "ok": False,
        "retryable": True,
        "error_code": "proxy_quality_failed",
        "stage": "proxy_check",
        "http_status": 0,
        "proxy": proxy_shape(proxy),
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
            print(json.dumps(result, ensure_ascii=False))
            return
        result["auth_status"] = status
    except Exception as exc:
        result["error"] = str(exc)[:300]
        error_text = str(exc).lower()
        result["error_code"] = (
            "proxy_dns_failed"
            if "resolve proxy" in error_text or "name or service not known" in error_text
            else "proxy_connection_failed"
        )
        print(json.dumps(result, ensure_ascii=False))
        return

    try:
        # A rotating proxy may legitimately return a different exit IP on
        # separate requests. OAuth itself keeps one configured proxy/session
        # for the protocol attempt, but a second Cloudflare trace must not
        # reject that attempt as "IP unstable".
        first = trace_ip(
            requests.Session(impersonate=IMPERSONATE),
            proxy,
        )
        first_ip = first["ip"]
        result["egress_first"] = first
        result.update({
            "ok": True,
            "retryable": False,
            "error_code": "",
            "error": "",
            "exit_ip": first_ip,
            "loc": first.get("loc", ""),
            "colo": first.get("colo", ""),
        })
        print(json.dumps(result, ensure_ascii=False))
    except Exception as exc:
        # Match the manager: the optional IP trace is diagnostic only. An
        # unavailable trace must not reject an otherwise reachable auth route.
        result.update({"ok": True, "retryable": False, "error": "", "error_code": "",
                       "trace_error": str(exc)[:300]})
        print(json.dumps(result, ensure_ascii=False))


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
