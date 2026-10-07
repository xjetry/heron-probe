"""Create a project design system from DESIGN.md via the Stitch MCP endpoint.

Stitch keeps only the frontmatter tokens of an uploaded DESIGN.md (colors, type, spacing); the prose
is dropped, which is why every generation prompt also carries the constraint preamble.

usage: ds.py <project_id> <DESIGN.md>   -> prints the design system asset name
"""
import base64
import json
import pathlib
import sys
import urllib.request

ENDPOINT = "https://stitch.googleapis.com/mcp"
# The Stitch API key lives in the untracked .mcp.json at the repo root; resolve it from this file so the
# script works from any cwd and any checkout path.
MCP_CONFIG = pathlib.Path(__file__).resolve().parents[2] / ".mcp.json"


def call(key: str, name: str, arguments: dict) -> dict:
    payload = {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}}
    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(payload).encode("utf-8"),
        headers={"X-Goog-Api-Key": key, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=300) as resp:
        result = json.loads(resp.read().decode("utf-8"))["result"]
    text = next(c["text"] for c in result["content"] if c.get("type") == "text")
    if result.get("isError"):
        raise SystemExit(f"{name} failed: {text[:500]}")
    return json.loads(text)


def main() -> int:
    project, design_md = sys.argv[1], pathlib.Path(sys.argv[2])
    key = json.loads(MCP_CONFIG.read_text())["mcpServers"]["stitch"]["headers"]["X-Goog-Api-Key"]
    encoded = base64.b64encode(design_md.read_bytes()).decode("ascii")
    instance = call(key, "upload_design_md", {"projectId": project, "designMdBase64": encoded})
    created = call(key, "create_design_system_from_design_md", {
        "projectId": project,
        "deviceType": "DESKTOP",
        "selectedScreenInstance": {"id": instance["id"], "sourceScreen": instance["sourceScreen"]},
    })
    print(f"assets/{created['assetId']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
