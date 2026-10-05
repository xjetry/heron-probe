#!/bin/sh
# 发布二进制运行完整 E2E，与当前源码 agent 共用断言，不能用当前构建替换缺失的发布产物。
set -eu
[ "$#" -gt 0 ] || { echo "usage: $0 IMAGE=EXPECTED_OS [...]" >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM HUP
"$root/scripts/compat-download.sh" "$work/agents"
E2E_AGENT_BIN_DIR="$work/agents"
# 清单与 compat-download.sh 同一个来源：只发 hub 的端到端（make bound-agent-e2e）经 COMPAT_PIN 给出绑定
# 版本的清单，缺省仍是仓库内的兼容基线。
E2E_AGENT_VERSION=$(jq -r .tag "${COMPAT_PIN:-$root/scripts/compat-agent.json}")
export E2E_AGENT_BIN_DIR E2E_AGENT_VERSION
for pair in "$@"; do
  case "$pair" in
    *=*) image=${pair%%=*}; os=${pair#*=} ;;
    *) echo "FAIL: expected IMAGE=EXPECTED_OS, got $pair" >&2; exit 1 ;;
  esac
  AGENT_IMAGE="$image" EXPECT_OS="$os" "$root/scripts/e2e.sh"
done
echo "published agent compatibility OK: $E2E_AGENT_VERSION"
