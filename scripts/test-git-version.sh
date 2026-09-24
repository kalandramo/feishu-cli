#!/bin/sh
# git-version 脚本的行为测试。
#
# 在临时仓（含本地 bare remote）中运行，不触碰真实仓库。
# 用法：sh scripts/test-git-version.sh [sh|ps1]   （缺省 sh）
#
# 覆盖：next 版本递增语义 + tag 模式的前置校验（无新提交/工作区脏/版本号格式）。
#
# 注意：不启用 set -e——断言失败须继续跑完并汇总，而非中途退出。
set -u

MODE="${1:-sh}"
SRC_DIR=$(cd "$(dirname "$0")/.." && pwd)

if [ "$MODE" = "ps1" ]; then
    SCRIPT_SRC="$SRC_DIR/scripts/git-version.ps1"
    RUN() { powershell -NoProfile -ExecutionPolicy Bypass -File "$TMPW/git-version.ps1" "$@"; }
    command -v powershell >/dev/null 2>&1 || { echo "SKIP: powershell 不可用"; exit 0; }
else
    SCRIPT_SRC="$SRC_DIR/scripts/git-version.sh"
    RUN() { sh "$TMPW/git-version.sh" "$@"; }
fi

PASS=0
FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "$2" ] && printf '       %s\n' "$2"; }

# assert_eq <desc> <expected> <actual>
assert_eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected=[$2] actual=[$3]"; fi; }
# assert_ne <desc> <unexpected> <actual>
assert_ne() { if [ "$2" != "$3" ]; then ok "$1"; else bad "$1" "unexpected=[$2]"; fi; }

# new_repo：建临时 bare remote + 工作仓 + 初始提交 + 安装被测脚本
new_repo() {
    TMPROOT=$(mktemp -d)
    TMPW="$TMPROOT/w"
    git init -q --bare "$TMPROOT/r.git"
    git init -q "$TMPW"
    cd "$TMPW"
    git config user.email t@t
    git config user.name t
    cp "$SCRIPT_SRC" "$TMPW/"
    echo a > f
    git add f
    git commit -qm "init"
    git remote add origin "$TMPROOT/r.git"
    git push -q -u origin HEAD
}
cleanup() { cd "$SRC_DIR"; [ -n "$TMPROOT" ] && rm -rf "$TMPROOT" || true; }
trap cleanup EXIT

# commit <msg>：提交一个改动（用于制造"有新提交"）
commit() { echo "$1" >> f; git commit -aqm "$1"; git push -q origin HEAD; }

echo "== git-version ($MODE) 行为测试 =="

# ---------------------------------------------------------------- next 语义
echo "[next 版本递增]"

new_repo
assert_eq "无 tag 时 next → v0.0.1" "v0.0.1" "$(RUN next)"

git tag v1.2.3 && commit x
assert_eq "v1.2.3 后 next → v1.2.4（patch 递增）" "v1.2.4" "$(RUN next)"

# 预发布：rc 之后的正式版应是同版本号（去预发布后缀），而非跳到下一 patch
git tag v1.3.0-rc1
assert_eq "v1.3.0-rc1 后 next → v1.3.0（正式版，非 v1.3.1）" "v1.3.0" "$(RUN next)"
git tag -d v1.3.0-rc1 >/dev/null

# ---------------------------------------------------------------- tag 前置校验
echo "[tag 前置校验]"

# (1) 无新提交：必须拒绝，且不得产生新 tag
new_repo
RUN tag >/dev/null 2>&1            # 首次 tag 成功
BEFORE=$(git tag -l | wc -l)
if RUN tag >/dev/null 2>&1; then
    bad "无新提交时 tag 应拒绝（非零退出）" "实际返回 0"
else
    ok "无新提交时 tag 拒绝并返回非零"
fi
assert_eq "无新提交时未产生新 tag" "$BEFORE" "$(git tag -l | wc -l)"

# (2) 工作区脏（已跟踪文件被改）：必须拒绝
new_repo
echo dirty >> f                    # 不 commit
BEFORE=$(git tag -l | wc -l)
if RUN tag >/dev/null 2>&1; then
    bad "工作区脏时 tag 应拒绝" "实际返回 0"
else
    ok "工作区脏时 tag 拒绝并返回非零"
fi
assert_eq "工作区脏时未产生新 tag" "$BEFORE" "$(git tag -l | wc -l)"
git checkout -q f

# (3) 版本号格式校验：非法值必须拒绝
new_repo
commit x
BEFORE=$(git tag -l | wc -l)
for bad_ver in "abc" "3.0.0" "v1.2" "v1.2.3.4" "v1.2.3-"; do
    if RUN tag "$bad_ver" >/dev/null 2>&1; then
        bad "非法版本号 [$bad_ver] 应拒绝" "实际返回 0"
    else
        ok "非法版本号 [$bad_ver] 拒绝"
    fi
done
assert_eq "非法版本号未产生任何 tag" "$BEFORE" "$(git tag -l | wc -l)"

# 合法版本号（含预发布后缀）应接受
if RUN tag "v9.9.9" >/dev/null 2>&1; then ok "合法版本号 v9.9.9 接受"; else bad "合法版本号 v9.9.9 被拒" ""; fi

# 空参数等价于未指定 → 走默认 next（不应视为非法版本号）
new_repo
commit x
if RUN tag "" >/dev/null 2>&1; then ok "空参数走默认 next"; else bad "空参数应走默认 next" ""; fi
assert_eq "空参数产出的是 next 版本（v0.0.1）" "1" "$(git tag -l | grep -c '^v0[.]0[.]1$')"

# ---------------------------------------------------------------- 回归保护
echo "[既有防线回归]"

# 领先远端：必须拒绝
new_repo
echo ahead >> f && git commit -aqm "ahead"    # 故意不 push
BEFORE=$(git tag -l | wc -l)
if RUN tag >/dev/null 2>&1; then
    bad "领先远端时 tag 应拒绝" "实际返回 0"
else
    ok "领先远端时 tag 拒绝"
fi
assert_eq "领先远端时未产生新 tag" "$BEFORE" "$(git tag -l | wc -l)"

# 正常路径：应成功并推送
new_repo
commit x
if RUN tag >/dev/null 2>&1; then ok "正常路径 tag 成功"; else bad "正常路径 tag 失败" ""; fi
# --refs 过滤掉 annotated tag 的 ^{} 解引用行，否则一个 tag 会被计成两行
assert_eq "tag 已推送到远端" "1" "$(git ls-remote --tags --refs origin | wc -l)"

echo ""
printf '结果: %s passed, %s failed
' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
