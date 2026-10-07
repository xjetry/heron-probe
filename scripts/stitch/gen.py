"""Generate one Stitch screen synchronously via the Stitch MCP endpoint.

The MCP client in the IDE times out before Stitch answers and generations are
sometimes dropped; a direct JSON-RPC call returns the finished screen (or the
server's error text) in one response, so every run leaves evidence on disk.

usage: gen.py <project_id> <design_system> <DESKTOP|MOBILE> <preamble.md> <body.md> <out.json>
"""
import json
import pathlib
import sys
import urllib.request

ENDPOINT = "https://stitch.googleapis.com/mcp"
# The Stitch API key lives in the untracked .mcp.json at the repo root; resolve it from this file so the
# script works from any cwd and any checkout path.
MCP_CONFIG = pathlib.Path(__file__).resolve().parents[2] / ".mcp.json"


def main() -> int:
    project, design_system, device, preamble, body, out = sys.argv[1:7]
    key = json.loads(MCP_CONFIG.read_text())["mcpServers"]["stitch"]["headers"]["X-Goog-Api-Key"]
    prompt = pathlib.Path(preamble).read_text(encoding="utf-8").rstrip() + "\n\n" + pathlib.Path(body).read_text(encoding="utf-8")
    payload = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "generate_screen_from_text",
            "arguments": {"projectId": project, "designSystem": design_system, "deviceType": device, "prompt": prompt},
        },
    }
    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
        headers={
            "X-Goog-Api-Key": key,
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=900) as resp:
        raw = resp.read().decode("utf-8")
    pathlib.Path(out).write_text(raw, encoding="utf-8")

    result = json.loads(raw).get("result", {})
    text = next((c["text"] for c in result.get("content", []) if c.get("type") == "text"), "")
    if result.get("isError"):
        # Overload shows up here as plain text ("The service is currently unavailable."), not as JSON.
        print(f"ERROR\t{text[:500]}")
        return 2
    inner = json.loads(text)
    screens = [s for oc in inner.get("outputComponents", []) for s in oc.get("design", {}).get("screens", [])]
    for s in screens:
        print(f"{s['name']}\t{s.get('screenMetadata', {}).get('status')}\t{s.get('title')}")
    # A response without a new screen is how Stitch reports an edit-instead-of-generate or a failure.
    return 0 if screens and not result.get("isError") else 1


if __name__ == "__main__":
    sys.exit(main())
