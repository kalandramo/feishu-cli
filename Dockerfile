# syntax=docker/dockerfile:1

# ─────────────────────────────────────────────────────────────────────────────
# feishu-cli MCP Server — 多阶段构建
#
# 运行形态：`mcp stream`（SSE over HTTP）。容器里不能用 stdio 形态——stdio 需要
# 客户端与进程共享标准输入输出，跨 Pod 边界无法提供。
#
# 为什么用 distroless/static 作为运行时基础镜像：
#   - feishu-cli 用 CGO_ENABLED=0 编译，产出纯静态二进制，不依赖 libc；
#   - 所有 API 元数据/scope 目录均通过 go:embed 内嵌进二进制，运行期不需要任何
#     外部数据文件；
#   - distroless 不含 shell / 包管理器，把攻击面压到最小。
# ─────────────────────────────────────────────────────────────────────────────

ARG GO_VERSION=1.27.1

# ── Stage 1: builder ─────────────────────────────────────────────────────────
FROM golang:${GO_VERSION} AS builder

WORKDIR /src

# 先只复制依赖清单，让 go mod download 层可被缓存——源码改动不会让它失效。
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 复制源码。
COPY . .

# 构建静态二进制。VERSION 由 CI 注入（如 git describe），缺省为 dev。
ARG VERSION=dev
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" \
        -o /out/feishu-cli .

# ── Stage 2: runtime ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# 以非 root 运行。distroless 的 :nonroot tag 自带 uid 65532。
# 该 uid 没有可写的 HOME，因此容器必须以环境变量注入凭证（FEISHU_APP_ID /
# FEISHU_APP_SECRET）——见 docs/DEPLOY.md。若需要 auth login 的持久化 token，
# 改用带 PVC 的部署形态。
USER nonroot:nonroot

COPY --from=builder /out/feishu-cli /usr/local/bin/feishu-cli

# SSE 服务监听端口（与 Deployment 的 containerPort 保持一致）。
EXPOSE 8080

# 健康检查交给 k8s 的 liveness/readiness probe（TCP 探测），这里不设 HEALTHCHECK——
# distroless 没有 shell，无法执行基于 curl/wget 的检查。

# 默认以 SSE 形态启动，监听所有网卡。
#
# 注意：--auth-token 只能通过命令行 flag 传入（cobramcp 不读取环境变量），
# 而 distroless 没有 shell、CMD 里无法做 $(VAR) 展开。因此认证 token 必须在
# k8s Deployment 的 args 里用 $(MCP_AUTH_TOKEN) 注入——该展开由 kubelet 完成，
# 不依赖 shell。未提供 token 时服务不启用任何认证（完全开放），切勿这样上生产。
# 详见 docs/DEPLOY.md。
ENTRYPOINT ["/usr/local/bin/feishu-cli"]
CMD ["mcp", "stream", "--host", "0.0.0.0", "--port", "8080"]
