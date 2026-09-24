#!/bin/sh
# Docker 镜像 tag 与 git tag 一一对应：先打 tag，再构建推送镜像。
#
# 用法：
#   git-version.sh             # 当前最近的 git tag（无 tag 时回退 v0.0.0）
#   git-version.sh next        # 建议的下一个 patch 版本（当前 PATCH + 1；无 tag 时 v0.0.1）
#   git-version.sh tag [ver] [-m <msg>]
#                              # 自动打 annotated tag：版本默认取 next（可指定 ver），
#                              # 注释默认 = 自上次 tag 以来的提交汇总（旧 -> 新）；
#                              # 传 -m/--message <msg> 则用自定义注释（不取历史提交）
#
# 示例：tag=v1.0.12 -> current 输出 v1.0.12，next 输出 v1.0.13，tag 打 v1.0.13
#
# tag 模式的前置校验（任一不满足即拒绝，不产生 tag）：
#   1. 自上次 tag 以来有提交（否则注释为空，tag 无意义）
#   2. 工作区干净（否则 tag 指向的 commit 不含改动，与 docker 构建的镜像不一致）
#   3. 本地分支不领先远端（否则 tag 指向远端不存在的 commit）
#   4. 指定版本号符合 v<major>.<minor>.<patch>[-预发布] 格式
set -e

# 参数解析：第一个非选项参数是 mode，其后第一个非选项参数是版本号；
# -m/--message <msg> 指定自定义注释，可出现在任意位置。
# 支持 -m msg、-m=msg、--message msg、--message=msg 四种写法。
MODE=""
SPECIFIED=""
MESSAGE=""
while [ $# -gt 0 ]; do
    case "$1" in
        -m|--message)
            shift
            MESSAGE="${1:-}"
            ;;
        -m=*|--message=*)
            MESSAGE="${1#*=}"
            ;;
        *)
            if [ -z "$MODE" ]; then
                MODE="$1"
            elif [ -z "$SPECIFIED" ]; then
                SPECIFIED="$1"
            fi
            ;;
    esac
    shift
done
MODE="${MODE:-current}"

# 当前 HEAD 可达的最近 tag；无 tag 时回退 v0.0.0（git describe 失败输出到 stderr，静默丢弃）
RECENT_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
HAS_TAG=false
[ "$RECENT_TAG" != "v0.0.0" ] && HAS_TAG=true

# strip_prerelease <ver>：去掉预发布后缀（v1.3.0-rc1 → v1.3.0）。
# 用于「预发布之后打正式版」的场景：rc 的 next 应是同版本号的正式版，而非跳到下一 patch。
strip_prerelease() {
    printf '%s\n' "$1" | sed -E 's/^([vV]?[0-9]+\.[0-9]+\.[0-9]+).*$/\1/'
}

# is_prerelease <ver>：是否带预发布后缀（含 '-' 且 '-' 后非空）
is_prerelease() {
    case "$1" in
        *-?*) return 0 ;;
        *) return 1 ;;
    esac
}

# next_version：由最近 tag 推导下一个版本。
#   - 最近 tag 是预发布（如 v1.3.0-rc1）→ 去掉后缀的正式版（v1.3.0）
#   - 否则 → PATCH + 1
next_version() {
    if is_prerelease "$RECENT_TAG"; then
        strip_prerelease "$RECENT_TAG"
        return 0
    fi

    # 拆解主/次/修订号（容忍 "v" 前缀或缺失 "v"，数字缺省为 0），PATCH + 1
    MAJOR=$(printf '%s' "$RECENT_TAG" | sed -E -e 's/^v?([0-9]+)\..*/\1/' -e 't' -e 's/.*/0/')
    MINOR=$(printf '%s' "$RECENT_TAG" | sed -E -e 's/^v?[0-9]+\.([0-9]+)\..*/\1/' -e 't' -e 's/.*/0/')
    PATCH=$(printf '%s' "$RECENT_TAG" | sed -E -e 's/^v?[0-9]+\.[0-9]+\.([0-9]+).*/\1/' -e 't' -e 's/.*/0/')
    MAJOR=${MAJOR:-0}
    MINOR=${MINOR:-0}
    PATCH=${PATCH:-0}
    printf 'v%s.%s.%s\n' "$MAJOR" "$MINOR" "$((PATCH + 1))"
}

# valid_version <ver>：是否符合 v<major>.<minor>.<patch>[-预发布]（v 前缀必需）
valid_version() {
    printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
}

if [ "$MODE" = "next" ]; then
    next_version
    exit 0
fi

if [ "$MODE" = "tag" ]; then
    VERSION="${SPECIFIED:-$(next_version)}"

    # 校验 1：版本号格式（用户可指定任意字符串，必须挡住非版本号）
    if ! valid_version "$VERSION"; then
        echo "错误：版本号 '$VERSION' 不符合格式。要求 v<major>.<minor>.<patch>（可带预发布后缀），如 v1.2.3 或 v1.2.3-rc1。" >&2
        exit 1
    fi

    # 校验 2：工作区必须干净——否则 tag 指向的 commit 不含未提交改动，
    # 而 task docker 会构建含这些改动的镜像，导致镜像内容与 tag 版本脱钩。
    # --untracked-files=no：只关心已跟踪文件的改动（untracked 不影响 tag 内容）。
    if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
        echo "错误：工作区有未提交的改动，tag 将指向不含这些改动的 commit（与后续构建的镜像不一致）。请先提交或撤销改动后再打 tag。" >&2
        git status --short --untracked-files=no >&2
        exit 1
    fi

    # 校验 3：注释来源。
    # 未指定 -m 时注释取「自上次 tag 以来的提交汇总」，此时必须确有新提交（否则注释为空）。
    # 指定 -m 时用自定义注释，不依赖提交历史，故跳过「有新提交」校验。
    if [ -n "$MESSAGE" ]; then
        NOTE="$MESSAGE"
    else
        # 注意：不可用 while read 累积后判空——$(cmd) 展开为空时会留下一行空行，
        # read 对该空行执行一次循环体，使变量变成 "\n" 而非空串（旧实现的 bug）。
        if $HAS_TAG; then
            COMMITS=$(git log "$RECENT_TAG"..HEAD --reverse --format='- %s')
        else
            COMMITS=$(git log --reverse --format='- %s')
        fi
        if [ -z "$COMMITS" ]; then
            echo "错误：自上次 tag ($RECENT_TAG) 以来没有新提交，无可汇总的注释。请先提交改动（或确认 HEAD 已领先最近 tag）后再打 tag，或用 -m <msg> 指定自定义注释。" >&2
            exit 1
        fi
        NOTE="$COMMITS"
    fi

    # 校验 4：本地分支领先远端时，tag 将指向远端不存在的 commit（悬挂 tag）。
    # 无 upstream（未跟踪远端）时 git 报错，视为已同步放行。
    AHEAD=$(git rev-list --count "@{upstream}..HEAD" 2>/dev/null || echo "0")
    if [ "$AHEAD" != "0" ]; then
        echo "错误：本地分支领先远端 ${AHEAD} 个提交，tag 将指向远端不存在的 commit。请先 git push 提交后再打 tag。" >&2
        exit 1
    fi

    echo ">>> 自动打 tag: git tag -a $VERSION -F <注释文件>"
    # 用 -F <文件> 而不是 -m <字符串>：自定义注释可能含引号/换行等 shell 敏感字符，
    # 经文件传递可避免引号转义问题（与 ps1 版保持一致）。
    NOTE_FILE=$(mktemp)
    trap 'rm -f "$NOTE_FILE"' EXIT
    printf '%s\n' "$NOTE" > "$NOTE_FILE"
    git tag -a "$VERSION" -F "$NOTE_FILE"
    rm -f "$NOTE_FILE"
    trap - EXIT
    echo ">>> 已打 tag: $VERSION"
    # 自动推送 tag 到远端（仅推 tag 引用，不推提交；本地分支已被上方校验为与远端同步）
    echo ">>> 自动推送 tag: git push origin $VERSION"
    git push origin "$VERSION"
    echo ">>> 已推送 tag: $VERSION"
    exit 0
fi

printf '%s\n' "$RECENT_TAG"
