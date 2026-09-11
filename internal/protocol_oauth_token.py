import base64
import json
import sys
from pathlib import Path

from curl_cffi import requests
sys.path.insert(0, str(Path(__file__).resolve().parent / "codex_runtime"))
from manager_oauth.upstream import OPENAI_IMPERSONATE, CPA_PROBE_USER_AGENT, _cffi_proxies


def main() -> None:
    payload = json.load(sys.stdin)
    endpoint = str(payload.get("endpoint") or "").strip()
    proxy = str(payload.get("proxy") or "").strip()
    form = payload.get("form") if isinstance(payload.get("form"), dict) else {}
    if not endpoint:
        raise RuntimeError("OAuth token endpoint 不能为空")
    if not proxy:
        raise RuntimeError("OAuth proxy required")
    response = requests.post(
        endpoint,
        headers={
            "Accept": "application/json",
            "Content-Type": "application/x-www-form-urlencoded",
            "User-Agent": CPA_PROBE_USER_AGENT,
        },
        data={str(key): str(value) for key, value in form.items()},
        proxies=_cffi_proxies(proxy),
        impersonate=OPENAI_IMPERSONATE,
        timeout=int(payload.get("timeout") or 60),
    )
    print(json.dumps({
        "status": int(response.status_code or 0),
        "body": base64.b64encode(response.content).decode("ascii"),
    }, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({
            "status": 0,
            "error": f"{type(exc).__name__}: {exc}",
        }, ensure_ascii=False))
