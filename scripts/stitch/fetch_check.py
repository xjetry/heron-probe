"""Download a generated screen (screenshot + HTML) from a gen.py response and scan its visible text.

usage: fetch_check.py <response.json> <out_dir> <png_width> [public|admin]
Prints forbidden-term hits found in rendered text only (scripts, styles and tag attributes are
stripped), plus a smoke count of a term every screen must contain, so an empty hit list can be
told apart from a scan that read nothing. The admin mode drops terms that are legitimate in the
panel (hostname, kernel, versions, API, alerts) and checks the sidebar nav instead of the header.
"""
import json
import pathlib
import re
import sys
import urllib.request

# Invented data that no Heron screen may show, public or admin.
COMMON_FORBIDDEN = [
    # "在线率" alone is invented; "不是在线率" is the mandated coverage disclaimer (§6.5), so match
    # the bare word only when it is not preceded by 不是.
    r"99\.\d", "可用率", r"(?<!不是)在线率", "SLA", "宕机", "不可达", "割接", "调度中", "同步延迟", "拓扑", "©",
    "KVM", "NVMe", "Heron Monitor", "Telemetry", "状态平稳", "无宿主", "通畅", "质量", "健康", "良好",
    "上报间隔", "发包", "采样点", "→ 上海", "Anycast", "xtom · 专线 · 家宽",
]
# Fields the public page must not expose; all legitimate in the admin panel.
PUBLIC_ONLY_FORBIDDEN = ["事件日志", "告警", "API", "版本", "刷新", "主机名", "内核"]

MODES = {
    "public": {
        "forbidden": COMMON_FORBIDDEN + PUBLIC_ONLY_FORBIDDEN,
        "smoke": "服务器状态",
        # The public header is a closed set; generators keep inventing nav items under new names,
        # so check by what is allowed rather than by a list of past inventions. Whole tokens are
        # compared: substring removal would turn "网络监控" into "网络" and hide what was invented.
        "chrome": ("header", {"服务器状态", "实时", "每", "2", "秒", "登录"}),
    },
    "admin": {
        "forbidden": COMMON_FORBIDDEN,
        "smoke": "管理工作台",
        # The sidebar nav is the fixed fourteen entries plus brand and account labels.
        "chrome": ("aside", {"Heron", "管理工作台", "管理员", "监控", "告警", "系统", "总览", "节点", "探测任务",
                             "告警规则", "告警事件", "维护静默", "通知渠道", "外观", "主题", "存储", "在线更新",
                             "安全", "API", "token", "注册窗口"}),
    },
}


def fetch(url: str, dest: pathlib.Path) -> int:
    with urllib.request.urlopen(url, timeout=120) as r:
        data = r.read()
    dest.write_bytes(data)
    return len(data)


def visible_text(fragment: str) -> str:
    fragment = re.sub(r"<script.*?</script>|<style.*?</style>", " ", fragment, flags=re.S)
    return re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", fragment))


def check_html(html: pathlib.Path, mode: dict) -> None:
    raw = html.read_text(encoding="utf-8")
    text = visible_text(raw)
    print(f"  smoke {mode['smoke']!r}: {text.count(mode['smoke'])}")
    tag, allowed = mode["chrome"]
    chrome = re.search(rf"<{tag}\b.*?</{tag}>", raw, flags=re.S)
    if chrome is None:
        print(f"  {tag.upper()}: no <{tag}> element found")
    else:
        tokens = re.split(r"[·•/\s]+", visible_text(chrome.group(0)).strip())
        # Lowercase-only tokens are icon-font ligatures such as dark_mode, not labels.
        leftover = [t for t in tokens if t and t not in allowed and not re.fullmatch(r"[a-z_]+", t)]
        print(f"  {tag.upper()} extra text: {leftover!r}" if leftover else f"  {tag.upper()}: ok")
    for pat in mode["forbidden"]:
        for m in list(re.finditer(pat, text))[:2]:
            print(f"  HIT {pat!r}: …{text[max(0, m.start() - 30):m.end() + 30]}…")


def main() -> int:
    resp, out_dir, width = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3]
    mode = MODES[sys.argv[4] if len(sys.argv) > 4 else "public"]
    out_dir.mkdir(parents=True, exist_ok=True)
    result = json.loads(resp.read_text(encoding="utf-8"))["result"]
    inner = json.loads(next(c["text"] for c in result["content"] if c.get("type") == "text"))
    screen = next(s for oc in inner["outputComponents"] for s in oc.get("design", {}).get("screens", []))
    stem = resp.stem
    png = out_dir / f"{stem}.png"
    html = out_dir / f"{stem}.html"
    print(f"{stem}: png {fetch(screen['screenshot']['downloadUrl'] + '=w' + width, png)} B, "
          f"html {fetch(screen['htmlCode']['downloadUrl'], html)} B, id {screen['id']}")
    check_html(html, mode)
    return 0


if __name__ == "__main__":
    sys.exit(main())
