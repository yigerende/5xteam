import base64
import json
import sys

from curl_cffi import requests


def main() -> None:
    payload = json.load(sys.stdin)
    endpoint = str(payload.get("endpoint") or "").strip()
    proxy = str(payload.get("proxy") or "").strip()
    form = payload.get("form") if isinstance(payload.get("form"), dict) else {}
    if not endpoint:
        raise RuntimeError("OAuth token endpoint 不能为空")
    session = requests.Session(impersonate="chrome136")
    if proxy:
        session.proxies = {"http": proxy, "https": proxy}
    response = session.post(
        endpoint,
        headers={
            "Accept": "application/json",
            "Content-Type": "application/x-www-form-urlencoded",
            "User-Agent": "codex_cli_rs",
        },
        data={str(key): str(value) for key, value in form.items()},
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
