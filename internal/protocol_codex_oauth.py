"""Local stdio entry point for the vendored Codex OAuth engine."""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "codex_runtime"))
from manager_oauth.adapter import run


def rpc(method, params):
    print(json.dumps({"rpc": method, "params": params}, ensure_ascii=False), flush=True)
    line = sys.stdin.readline()
    if not line:
        raise RuntimeError("Go OAuth job closed the local integration pipe")
    reply = json.loads(line)
    if reply.get("error"):
        raise RuntimeError(reply["error"])
    return reply.get("result")


def main():
    line = sys.stdin.readline()
    payload = json.loads(line or "{}")
    result = run(payload, rpc if payload.get("stdio_rpc") else None)
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({"success": False, "retryable": False, "error_code": "runtime_error",
                          "error": type(exc).__name__ + ": " + str(exc)}, ensure_ascii=False), flush=True)
