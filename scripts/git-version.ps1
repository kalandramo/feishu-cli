# Docker 镜像 tag 与 git tag 一一对应：先打 tag，再构建推送镜像。
#
# 用法：
#   git-version.ps1             # 当前最近的 git tag（无 tag 时回退 v0.0.0）
#   git-version.ps1 next        # 建议的下一个 patch 版本（当前 PATCH + 1；无 tag 时 v0.0.1）
#   git-version.ps1 tag [ver]   # 自动打 annotated tag：版本默认取 next（可指定 ver），
#                               # 注释 = 自上次 tag 以来的提交汇总（旧 -> 新）
#
# 示例：tag=v1.0.12 -> current 输出 v1.0.12，next 输出 v1.0.13，tag 打 v1.0.13
#
# tag 模式的前置校验（任一不满足即拒绝，不产生 tag）：
#   1. 工作区干净（否则 tag 指向的 commit 不含改动，与 docker 构建的镜像不一致）
#   2. 自上次 tag 以来有提交（否则注释为空，tag 无意义）
#   3. 本地分支不领先远端（否则 tag 指向远端不存在的 commit）
#   4. 指定版本号符合 v<major>.<minor>.<patch>[-预发布] 格式

# 输出统一按 UTF-8（配合脚本文件带 BOM，保证 Windows PowerShell 5.1 正确解析中文）
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

$mode = if ($args.Count -gt 0) { $args[0] } else { "current" }
$specified = if ($args.Count -gt 1) { $args[1] } else { "" }

# 当前 HEAD 可达的最近 tag；无 tag 时回退 v0.0.0
$recentTag = git describe --tags --abbrev=0 2>$null
if ($LASTEXITCODE -ne 0 -or -not $recentTag) {
    $recentTag = "v0.0.0"
}
$hasTag = $recentTag -ne "v0.0.0"

function Get-NextVersion {
    # 预发布 tag（如 v1.3.0-rc1）→ 去掉后缀的正式版（v1.3.0），而非跳到下一 patch
    if ($recentTag -match '^[vV]?\d+\.\d+\.\d+-') {
        $base = $recentTag -replace '^([vV]?\d+\.\d+\.\d+).*$', '$1'
        if ($base -notmatch '^v') { $base = "v$base" }
        return $base
    }
    # 拆解主/次/修订号（容忍 "v" 前缀；数字缺省为 0，与 git-version.sh 语义一致），PATCH + 1
    # 示例：v1.0 -> v1.0.1；v1 -> v1.0.1；v1.2.3 -> v1.2.4
    if ($recentTag -match '^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?') {
        $major = if ($Matches[1]) { [int]$Matches[1] } else { 0 }
        $minor = if ($Matches[2]) { [int]$Matches[2] } else { 0 }
        $patch = if ($Matches[3]) { [int]$Matches[3] + 1 } else { 1 }
    } else {
        $major = 0
        $minor = 0
        $patch = 1
    }
    "v$major.$minor.$patch"
}

if ($mode -eq "next") {
    Get-NextVersion
    exit 0
}

if ($mode -eq "tag") {
    $version = if ($specified) { $specified } else { Get-NextVersion }

    # 校验 1：版本号格式（用户可指定任意字符串，必须挡住非版本号）
    if ($version -notmatch '^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') {
        [Console]::Error.WriteLine("错误：版本号 '$version' 不符合格式。要求 v<major>.<minor>.<patch>（可带预发布后缀），如 v1.2.3 或 v1.2.3-rc1。")
        exit 1
    }

    # 校验 2：工作区必须干净——否则 tag 指向的 commit 不含未提交改动，
    # 而 task docker 会构建含这些改动的镜像，导致镜像内容与 tag 版本脱钩。
    # --untracked-files=no：只关心已跟踪文件的改动（untracked 不影响 tag 内容）。
    $dirty = git status --porcelain --untracked-files=no
    if ($dirty) {
        [Console]::Error.WriteLine("错误：工作区有未提交的改动，tag 将指向不含这些改动的 commit（与后续构建的镜像不一致）。请先提交或撤销改动后再打 tag。")
        $dirty | ForEach-Object { [Console]::Error.WriteLine("  $_") }
        exit 1
    }

    # 校验 3：自上次 tag 以来必须有新提交。
    # 注释：自上次 tag 以来的提交 subject 汇总；无上次 tag 时用全部提交历史。
    # 注意 1：git log 输出经管道是 string[]，须 @() 强制数组再 join 成单个字符串传 -m，
    #         否则多行时参数被拆开、单行字符串时 -join 会逐字符插入分隔符。
    # 注意 2：range 操作符坑——"$recentTag..HEAD" 必须整体放进双引号，不能写成 "$recentTag"..HEAD，
    #         否则 PowerShell 会把 .. 当作 range 运算符求值。
    if ($hasTag) {
        $noteLines = git log "$recentTag..HEAD" --reverse --format='- %s'
    } else {
        $noteLines = git log --reverse --format='- %s'
    }
    $note = @($noteLines) -join "`n"
    if ([string]::IsNullOrEmpty($note)) {
        [Console]::Error.WriteLine("错误：自上次 tag ($recentTag) 以来没有新提交，无可汇总的注释。请先提交改动（或确认 HEAD 已领先最近 tag）后再打 tag。")
        exit 1
    }

    # 校验 4：本地分支领先远端时，tag 将指向远端不存在的 commit（悬挂 tag）。
    # 无 upstream（未跟踪远端）时 git 报错、$LASTEXITCODE 非 0，视为已同步放行。
    $aheadCount = (git rev-list --count "@{upstream}..HEAD" 2>$null)
    if ($LASTEXITCODE -ne 0) { $aheadCount = "0" }
    if ($aheadCount -ne "0") {
        [Console]::Error.WriteLine("错误：本地分支领先远端 $aheadCount 个提交，tag 将指向远端不存在的 commit。请先 git push 提交后再打 tag。")
        exit 1
    }

    Write-Host ">>> 自动打 tag: git tag -a $version -m `"<提交汇总>`""
    git tag -a $version -m $note
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host ">>> 已打 tag: $version"
    # 自动推送 tag 到远端（仅推 tag 引用，不推提交；本地分支已被上方校验为与远端同步）
    Write-Host ">>> 自动推送 tag: git push origin $version"
    git push origin $version
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host ">>> 已推送 tag: $version"
    exit 0
}

Write-Output $recentTag
