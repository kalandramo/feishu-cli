#!/usr/bin/env bash
# 妙笔BOX 本地验证入口。独立 session、结构化响应和截图校验由 verify_html.py 完成。
set -eu
command -v python3 >/dev/null 2>&1 || { echo "需要 python3 执行 HTML 验证" >&2; exit 2; }
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec python3 "$SCRIPT_DIR/verify_html.py" "$@"
