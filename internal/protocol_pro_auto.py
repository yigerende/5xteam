"""Dedicated stdio worker for one uninterrupted Pro login/purchase/OAuth."""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "codex_runtime"))
from manager_oauth.continuous_pro import run
from protocol_codex_oauth import rpc

if __name__ == "__main__":
    result = run(json.loads(sys.stdin.readline() or "{}"), rpc)
    print(json.dumps(result, ensure_ascii=False), flush=True)
