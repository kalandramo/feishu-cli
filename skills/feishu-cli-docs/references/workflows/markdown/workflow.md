# 飞书云盘原生 Markdown（markdown create/fetch/overwrite/patch/diff）

`markdown` 命令组把 Drive 上的 **`.md` 当作普通文件整体读写**，保留原始 Markdown 源码，**不做** Markdown ↔ 飞书 docx 块的转换。

> **feishu-cli**：如尚未安装，请前往 [riba2534/feishu-cli](https://github.com/riba2534/feishu-cli) 获取安装方式。

## 目录

- [与 doc import/export 的区别](#核心概念与-doc-import--doc-export-的区别)
- [前置条件](#前置条件)
- [命令速查](#命令速查)
- [底层实现与踩坑](#底层实现--踩坑)
- [典型工作流](#典型工作流)
- [权限要求](#权限要求)
- [常见错误](#常见错误)

## 核心概念：与 `doc import` / `doc export` 的区别

| 命令 | 行为 | 创建出的类型 | 适用场景 |
|------|------|------------|---------|
| `doc import` | Markdown → 飞书 docx 块（标题/列表/表格/Callout/Mermaid 画板…） | docx（在线协同文档） | 给人读、要排版、要团队评论 |
| `doc export` | docx → Markdown（块解析回 markdown 源码） | 本地 `.md` | 从飞书 docx 落盘到 Git |
| `markdown create/fetch/overwrite/patch` | 把 `.md` 整体上传/下载，**不转换** | file（Drive 普通文件） | AI agent 直接落盘原汁原味 `.md`、保留 fenced code block 缩进、当图床/密集代码块/版本管理用 |

**判断走哪条**：

- 想要飞书 docx 渲染（人读、排版、表格分块、画板渲染）→ `doc import`
- 想要原始 `.md` 文本 unchanged 保留在云盘（AI agent 读回完全一致；docx 块解析有损）→ `markdown create`
- 想反复覆盖同一份 `.md`、保持 file_token 不变（分享链接持久）→ `markdown overwrite`

## 前置条件

- **认证**：`--as bot|user|auto`（默认 auto）。User 优先；完全未配置 User Token 时回退 Bot；**已配置但解析/刷新失败 fail-closed**，不会静默切 Bot。cron 用 `--as bot`
- **预检**：`feishu-cli auth check --scope "drive:file:upload drive:file:download"`

## 命令速查

### 1. `markdown create` — 上传新 .md 到云盘

```bash
# 从字符串内容创建
feishu-cli markdown create --name plan.md --content "# Plan\n\n- todo 1"

# 从本地文件创建（--name 缺省时取本地 basename）
feishu-cli markdown create --content-file ./local.md
feishu-cli markdown create --file ./local.md

# 指定目标文件夹或 wiki 节点（二者互斥）
feishu-cli markdown create --name draft.md --content-file ./tmp.md --folder-token fldxxx
feishu-cli markdown create --name draft.md --content "# wiki" --wiki-token wikcnxxx

# JSON 输出（拿 file_token 给后续步骤）
feishu-cli markdown create --name plan.md --content-file ./plan.md -o json
```

**关键 flag**：

| flag | 说明 |
|------|------|
| `--name` | 远端文件名，**必须 `.md` 结尾**。`create`：`--content` 时必填、`--content-file` 时可省取本地 basename。`overwrite`：**缺省一律读远端现有名**（含 `--content-file` 场景，不会拿本地文件名顶替）；显式传入 = 同时改名。若读不到远端名则 fail-closed 报错，不会退化成 `<file_token>.md` 静默改名 |
| `--content` | 字符串内容（与 `--content-file` 二选一） |
| `--content-file` | 本地 `.md` 文件路径 |
| `--file` | 兼容别名，等价于 `--content-file` |
| `--folder-token` | 目标 Drive 文件夹（缺省根目录；与 `--wiki-token` 互斥） |
| `--wiki-token` | 目标 wiki 节点（`parent_type=wiki`） |
| `--dry-run` | 只打印将要发出的请求（不解析/刷新 token） |
| `--as` | `bot\|user\|auto`（默认 auto；已配置 User 刷新失败 fail-closed） |
| `-o json` | JSON 输出（含 `file_token` / `file_name` / `size_bytes`） |
| `--user-access-token` | 覆盖登录态 |

**校验**：空内容直接报错（不允许创建空 `.md`）。

### 2. `markdown fetch` — 下载云盘 .md

```bash
# 打印到 stdout（缺省 --output-path 时）
feishu-cli markdown fetch --file-token boxcnxxx

# 落盘到本地路径
feishu-cli markdown fetch --file-token boxcnxxx --output-path ./local.md

# 路径已存在时强制覆盖
feishu-cli markdown fetch --file-token boxcnxxx --output-path ./local.md --overwrite

# JSON 输出（含 content 字符串）
feishu-cli markdown fetch --file-token boxcnxxx -o json

# 历史版本（preview_download?preview_type=16&version=N）
feishu-cli markdown fetch --file-token boxcnxxx --version 7633658129540910621
```

**关键 flag**：

| flag | 说明 |
|------|------|
| `--file-token` | Markdown 文件 token（必填） |
| `--output-path` | 本地保存路径；**目录时用响应头文件名**；缺省打印 stdout |
| `--version` | 历史版本号，走 `preview_download` 的 `version` 查询参数 |
| `--overwrite` | 本地文件已存在时覆盖（缺省直接报错） |
| `--dry-run` | 只打印将要发出的请求 |
| `-o, --output json` | JSON 输出；不传 `--output-path` 时包含 `content` 字符串 |

### 3. `markdown overwrite` — 覆盖已有 .md（保 file_token）

```bash
# 字符串覆盖
feishu-cli markdown overwrite --file-token boxcnxxx --name existing.md --content "新内容"

# 本地文件覆盖
feishu-cli markdown overwrite --file-token boxcnxxx --content-file ./new.md
feishu-cli markdown overwrite --file-token boxcnxxx --file ./new.md

# 覆盖 + 改名（必须 .md 结尾）
feishu-cli markdown overwrite --file-token boxcnxxx --content-file ./new.md --name renamed.md
```

**关键 flag**：

| flag | 说明 |
|------|------|
| `--file-token` | 目标文件 token（必填） |
| `--content` / `--content-file` | 新内容（二选一） |
| `--name` | 覆盖后文件名（`.md` 结尾；缺省通过 `metas/batch_query` 读取远端现有名） |
| `--dry-run` | 只打印将要发出的请求 |
| `-o json` | JSON 输出 |

**核心价值**：`file_token` 保持不变 → 分享链接持久、其他人收藏的链接不失效；多次迭代场景（AI agent 每天更新同一份 `.md`）优于"删了重建"。>20MB 自动走 `upload_prepare/part/finish`，仍保留同一 file_token。

### 3.1 `markdown patch` — 查找替换后覆盖

```bash
feishu-cli markdown patch --file-token boxcnxxx --pattern "TODO" --content "DONE"
feishu-cli markdown patch --file-token boxcnxxx --regex --pattern "v[0-9]+" --content "v2"
```

先 `preview_download?preview_type=16` 拉当前内容，本地 literal/RE2 替换；`match_count=0` 时不写回。

### 4. `markdown diff` — 本地比对远端最新/历史版本（只读，不改远端）

下载远端 Markdown 内容并在本地计算 unified diff，**不修改远端文件**。源/历史走 `GET /open-apis/drive/v1/medias/{token}/preview_download?preview_type=16`。三种比对模式：

```bash
# 模式 1：远端最新 vs 本地文件
feishu-cli markdown diff --file-token boxcnxxx --file ./local.md

# 模式 2：远端某版本 vs 远端最新
feishu-cli markdown diff --file-token boxcnxxx --from-version 3

# 模式 3：远端版本 A vs 版本 B（--to-version 需配合 --from-version）
feishu-cli markdown diff --file-token boxcnxxx --from-version 2 --to-version 5 --context-lines 1

# 结构化输出 + 内置 jq 过滤（--format/--jq 与其它新命令一致；-o json 作兼容别名）
feishu-cli markdown diff --file-token boxcnxxx --file ./local.md --format json
feishu-cli markdown diff --file-token boxcnxxx --file ./local.md --jq '.added_lines'
feishu-cli markdown diff --file-token boxcnxxx --file ./local.md --format table --jq '{identical,added_lines,removed_lines}'
feishu-cli markdown diff --file-token boxcnxxx --file ./local.md -o json   # 兼容写法，等价 --format json
```

> `--from-version` / `--to-version` 必须是数字版本号；可与 `--file` 组合（远端指定版本 vs 本地）。`--to-version` 不能与 `--file` 同时使用。

**关键 flag**：

| flag | 说明 |
|------|------|
| `--file-token` | 目标 Markdown 文件 token（必填） |
| `--file` | 本地 `.md` 路径（模式 1：与远端最新比对） |
| `--from-version` | 起始远端版本号（模式 2/3） |
| `--to-version` | 目标远端版本号（模式 3，需配合 `--from-version`） |
| `--context-lines` | 每个 hunk 上下保留的未变更上下文行数（默认 3） |
| `--dry-run` | 只打印比对计划，不下载/不比对 |
| `--format` | `json\|pretty\|table\|ndjson\|csv` 结构化输出；缺省打印 unified diff 文本 |
| `--jq` | 内置 gojq 过滤结构化输出（如 `--jq '.added_lines'`，无需外部 jq） |
| `-o json` | 兼容别名，等价 `--format json` |

**结构化输出结构**（`--format json` / `-o json`，顶层 9 字段，可直接 `--jq` 取改动量 / 判一致）：

```jsonc
{
  "detection": "local_vs_remote",   // 比对模式：local_vs_remote（模式 1）/ remote_vs_remote（模式 2/3）
  "from": "remote (latest)",        // 左侧来源名（模式 1="remote (latest)"；模式 2/3="remote@version=N"）
  "to": "local: ./local.md",        // 右侧来源名（模式 1="local: <path>"；模式 2="remote (latest)"；模式 3="remote@version=N"）
  "size_bytes_before": 1024,        // 左侧内容字节数
  "size_bytes_after": 1088,         // 右侧内容字节数
  "identical": false,               // 是否无差异（等价于 hunks 为空）
  "added_lines": 5,                 // 新增行数（统计所有 hunk 中 op="+"）
  "removed_lines": 2,               // 删除行数（统计所有 hunk 中 op="-"）
  "hunks": [                        // unified diff 的 hunk 数组（无差异时为空数组）
    {
      "old_start": 10, "old_lines": 4,   // 左侧起始行 / 行数
      "new_start": 10, "new_lines": 7,   // 右侧起始行 / 行数
      "lines": [
        { "op": " ", "text": "上下文行" },   // op: " "=未变 / "-"=删除 / "+"=新增
        { "op": "-", "text": "旧内容" },
        { "op": "+", "text": "新内容" }
      ]
    }
  ]
}
```

常用内置 `--jq`（无需外部 jq）：`--jq '.identical'` 判是否一致、`--jq '.added_lines, .removed_lines'` 取改动量、`--jq '.hunks[].lines[] | select(.op=="+") | .text'` 拉所有新增行。

> 与 `overwrite` 配合：覆盖前先 `diff --file ./local.md` 预览本地相对远端最新的改动，确认后再 overwrite，避免误覆盖。

## 底层实现 & 踩坑

### 1. SDK 不暴露 `file_token` field —— 自拼 multipart

飞书 Go SDK v3.5.3 的 `UploadAllFileReqBody` 只暴露 `file_name` / `parent_type` / `parent_node` / `size` / `checksum` / `file`，**没有 `file_token`** 字段。

但官方 API `POST /open-apis/drive/v1/files/upload_all` 是支持 `file_token` 的：**带 `file_token` 时覆盖原文件保留 token、刷新 version/size；不带时按 `parent_type+parent_node` 在指定目录新建**。

为绕开 SDK 限制，`internal/client/markdown.go:OverwriteFileWithToken` 用 `client.Post` + `*larkcore.Formdata` 自己拼 multipart（translator 检测到 `*Formdata` 会自动切到 FileUpload 多部分序列化路径，见 SDK `core/reqtranslator.go:payload`）。endpoint 仍是官方 `upload_all`。

### 2. 单次上传 20MB 边界

`upload_all` 单次上限 **20MB**。`create` / `overwrite` / `patch` 在 **恰好 20MB** 仍走 `upload_all`，**20MB+1** 自动切 `upload_prepare/upload_part/upload_finish`，覆盖路径同样携带 `file_token` 保留原文件。

**`markdown diff` 另有独立的体积/行数上限**（防 OOM；在下载完成、计算 LCS **之前**拦截）：

- **单侧 ≤ 10MB**：远端走流式 `LimitReader`，本地先 `Stat` 再 `LimitReader`；恰好 10MB 放行，10MB+1 报 `exceeds 10.0 MB markdown +diff content limit`
- `--format` / `--jq` 在下载前解析，非法取值不会先把远端内容读进内存

**行数/矩阵上限**（与 20MB 上传上限无关，防 LCS 矩阵 OOM）：

- **单侧 ≤ 20000 行**：任一侧超过即报错 `内容过大（N 行 / M 行），单侧超过行数上限 20000，建议用外部 diff 工具`
- **两侧行数乘积 ≤ 2000 万单元**：LCS 用完整 `(n+1)×(m+1)` int 矩阵，内存随两侧行数**乘积**增长；乘积超限报错 `内容过大（N × M 行），LCS 矩阵超过 20000000 单元上限（防 OOM），建议用外部 diff 工具`
- 两条都在下载完成、计算 LCS **之前**拦截（`cmd/markdown_diff.go:checkDiffSize`）。长 `.md`（海量日志贴、全量代码 dump）跑 diff 命中该报错时，改用本地 `diff` / `git diff` 等外部工具，或先用 `markdown fetch` 落盘两侧再外部比对。

### 3. `.md` 后缀强制校验

`--name`（create）和 `--name`（overwrite）都强制 **`.md` 结尾**（lowercase 检查后缀），非 `.md` 直接报错。如果想存 `.markdown` / `.mdx` 走 `drive upload` 老路径。

### 4. 空内容直接报错

`--content ""` / 空 `--content-file` 都被拒绝（"Markdown 内容为空，不支持创建/覆盖为空文件"）。需要"清空"语义的话走 `markdown overwrite --content " "` 写一个占位空格。

### 5. `fetch` 的路径输出与 JSON 输出分离

`markdown fetch` 使用 `--output-path` 保存到本地，使用 `-o json` 输出 JSON：

- `--output-path ./x.md` → 落盘到 `./x.md`
- `-o json` → 把 `{file_token, content, size_bytes}` 打到 stdout
- 两者可叠加：落盘 + 同时 stdout JSON 摘要

### 6. `--output-path` 是目录时的文件名

`markdown fetch --output-path ./downloads/` 优先使用 `Content-Disposition` 文件名，缺省才回退 `<fileToken>.md`。

## 何时转走 `doc import/export`

- **要飞书 docx 渲染体验**（人读、表格分行、Mermaid 转画板、Callout 高亮）→ `doc import`（参 `feishu-cli-docs` skill）
- **要从飞书 docx 落盘到 Git 仓库**（块解析回 markdown）→ `doc export`（参 `feishu-cli-docs` skill）
- **要把 Markdown 转换前 sanity check**（Mermaid 花括号、表格 9×9 限制、Callout 6 种类型）→ `../import/references/doc-guide.md`

## 何时转走 `drive upload`

- **非 `.md` 扩展名**（`.mdx` / `.markdown` / `.txt`）→ `drive upload`
- **二进制或非 Markdown 覆盖** → `drive upload --file-token`

## 权限要求

| 命令 | Token 策略 | 所需 scope |
|------|------|------|
| `markdown create` | `--as bot\|user\|auto`（默认 auto） | `drive:file:upload`（或 `drive:drive`） |
| `markdown fetch` / `diff` | `--as bot\|user\|auto`（默认 auto） | `drive:file:download`（或 `drive:drive`） |
| `markdown overwrite` / `patch` | `--as bot\|user\|auto`（默认 auto） | `drive:file:upload` + `drive:drive.metadata:readonly` + `drive:file:download` |

**推荐做法**：执行 `feishu-cli auth login` 登录后，由 `auth check --scope "drive:file:upload drive:file:download"` 预检；缺 scope 时按提示 `auth login` 补申请。

## 典型工作流

### 工作流 A：AI agent 每天迭代同一份 `.md`

```bash
# Day 1：创建一次拿到 file_token
FT=$(feishu-cli markdown create --name daily-summary.md --content-file ./summary.md -o json | jq -r '.file_token')
echo "$FT" > ~/.cache/daily-summary.token

# Day 2+：覆盖（file_token 不变，分享链接持久）
feishu-cli markdown overwrite --file-token "$(cat ~/.cache/daily-summary.token)" --content-file ./summary.md
```

### 工作流 B：从云盘 `.md` 落盘到本地

```bash
# 读回原汁原味 Markdown 源码
feishu-cli markdown fetch --file-token boxcnxxx --output-path ./local.md --overwrite

# 编辑后写回
feishu-cli markdown overwrite --file-token boxcnxxx --content-file ./local.md
```

### 工作流 C：与 `doc import` 协作（先落盘 `.md` 再批量 import）

```bash
# 上游产出 .md 到云盘（保留源码备份）
feishu-cli markdown create --name design.md --content-file ./design.md --folder-token fldxxx

# 下游再走 doc import 生成飞书 docx 给团队阅读
feishu-cli doc import ./design.md --title "设计稿" --upload-images
```

## 注意事项

- **`--as bot|user|auto`**：默认 auto。未配置 User 回退 Bot；已配置但刷新失败 fail-closed
- **不做 Markdown 转换**：本命令组保留 `.md` 字节流不变，**不**做飞书 docx 块转换
- **>20MB 分片覆盖**：`overwrite`/`patch`/`create` 均支持 multipart，保留 file_token
- **`fetch` 输出参数**：路径走 `--output-path`，格式走 `-o/--output`

> `.md` 后缀强制、空内容拒绝两条已经在「核心 flag」与「踩坑」章节出现，这里不再重复，详见 [`overwrite` flag 表](#3-markdown-overwrite--覆盖已有-md保-file_token) 和 [`踩坑 §3/§4`](#3-md-后缀强制校验)。

## 常见错误

实际报错示例（参 `cmd/markdown_overwrite.go` + `internal/client/markdown.go:OverwriteFileWithToken`）：

| 触发条件 | 错误信息 | 排查方向 |
|---|---|---|
| User Token 对目标文件无编辑权限 | `覆盖文件失败: code=<非 0>, msg=<飞书返回>`（常见 `1061004 forbidden`、`1061045 no permission`） | 在飞书云盘右键文件 → 共享 → 给当前用户加「可编辑」；或换文件所有者的 token；或检查 scope `drive:file:upload` 是否已授予 |
| 覆盖未返回 version | `覆盖 Markdown 失败: 未返回 version` | 服务端应在 overwrite 响应里带回 version；保留 log_id 排查 |
| `--name` 不以 `.md` 结尾 | `--name 必须以 .md 结尾，得到 "xxx.txt"` | 改 `.md` 后缀；`.mdx` / `.markdown` 必须走 `drive upload` |
| `--content-file` 路径不存在 | `读取本地文件失败: open <path>: no such file or directory` | 检查路径是否相对当前目录；建议传绝对路径 |
| `--content-file` 是目录 | `--content-file 必须指向文件，不是目录` | 指向具体 `.md` 文件 |
| 既给 `--content` 又给 `--content-file` | `--content 与 --content-file 不能同时使用` | 二选一 |
| 都没给 | `请提供 --content 或 --content-file` | 至少传一个 |
| 未传 `--name` 且读不到远端现有文件名 | 拒绝以 `<fileToken>.md` 静默重命名，非零退出且不上传 | 确认远端文件名后显式传 `--name existing.md`，或先解决元数据读取失败 |
| 内容为空字节 | `Markdown 内容为空，不支持把 .md 覆盖为空文件` | 要清空语义传 `--content " "`（占位空格） |
| HTTP 层异常 | `覆盖文件失败: HTTP <status>, body: <raw>` | 网络/代理问题，附带原始 body 便于排查 |
| 响应解析失败 | `解析覆盖响应失败: <json error>` | 飞书侧返回非 JSON（极罕见，通常网关错误页） |

> 权限不足时飞书 API 直接返回 `code != 0` 的业务错（如 `1061004`、`1061045`），CLI 层透传 `msg` 字段；若同时缺 scope 会在 `auth check` 阶段就拦截，建议执行前先跑 `feishu-cli auth check --scope "drive:file:upload"`。

## 与老命令的对照

| 老命令 | 新 markdown 命令 | 差异 |
|---|---|---|
| `drive upload --file x.md` | `markdown create --content-file x.md` | markdown 强制 `.md` 后缀 + 空内容校验 + AI agent 友好 |
| `drive download --file-token xxx` | `markdown fetch --file-token xxx` | markdown 默认打印 stdout（文本场景）+ 目录路径自动拼 `.md` |
| `drive upload --file x.md --file-token <token>` | `markdown overwrite` | 两者都保留 file_token；drive 覆盖限 ≤20MB，markdown 覆盖支持 >20MB 分片并在省略 --name 时保留远端文件名 |

`drive upload/download` 适合二进制与非 `.md` 文件；原生 Markdown 的读取、比较、查找替换与大文件覆盖使用 `markdown` 命令组。

## 官方协议要点

- 源/历史下载：`GET /open-apis/drive/v1/medias/{token}/preview_download?preview_type=16[&version=N]`
- 创建/覆盖：`POST /files/upload_all`；>20MB 走 `upload_prepare/part/finish`。wiki 目标 `parent_type=wiki`
- overwrite 未传 `--name` 时先 `POST /drive/v1/metas/batch_query` 读现有 title
