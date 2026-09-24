# feishu-cli MCP Server 部署指南（Kubernetes）

把 feishu-cli 作为 MCP server 部署到 Kubernetes，供集群内外的 AI 客户端
（VSCode Copilot / Cursor / 自研 Agent）通过 SSE 接入。

本文档对应**无状态部署形态**：凭证与认证 token 全部通过 Secret 环境变量注入，
不挂载任何持久卷，可水平扩展、可滚动更新。

---

## 1. 部署形态与约束

### 1.1 为什么必须用 SSE 而不是 stdio

cobramcp 提供两种传输形态：

| 形态 | 启动命令 | 适用场景 |
| --- | --- | --- |
| stdio | `feishu-cli mcp start` | 客户端与进程在同一台机器（本地编辑器直接拉起） |
| SSE/HTTP | `feishu-cli mcp stream` | 跨进程 / 跨主机，**容器部署必须用这个** |

stdio 依赖客户端与进程共享标准输入输出，跨 Pod 边界无法提供，因此容器里固定用
`mcp stream`。

### 1.2 配置与凭证从哪来（关键）

feishu-cli 的配置目录**硬编码在 `$HOME/.feishu-cli`**（见
`internal/profile/store.go:120` 的 `RootDir()`），无法用环境变量覆盖。容器里
`nonroot` 用户没有可写的 HOME，所以本方案**不依赖任何配置文件**，凭证全部走
环境变量：

| 环境变量 | 作用 | 必需 |
| --- | --- | --- |
| `FEISHU_APP_ID` | 飞书应用 app_id | 是 |
| `FEISHU_APP_SECRET` | 飞书应用 app_secret | 是 |
| `MCP_AUTH_TOKEN` | MCP 客户端静态 Bearer token | 生产必需 |

优先级链（见 `internal/config/config.go`）：命令行 flag > 环境变量 > 配置文件。

> **局限**：本形态不支持 `auth login` 的持久化 User Token（那需要写
> `~/.feishu-cli/token.json`）。当前 MCP 暴露的是**知识库（wiki）能力**，走
> tenant token，`FEISHU_APP_ID/SECRET` 即可满足。若将来需要 User Token 类能力
> （mail / vc / minutes 等），改用带 PVC 的部署形态，或通过
> `FEISHU_USER_ACCESS_TOKEN` 注入预生成的 token（会过期，不能自动刷新）。

### 1.3 暴露的命令面

MCP 工具列表由 `cmd/mcp.go` 的 `mcpAllowedGroups` 白名单控制，当前仅开放
**wiki（知识库）**命令组，共 19 个工具。其余 400+ 命令默认不可见。要扩展能力时
在 `mcpAllowedGroups` 追加命令组名并重新构建镜像。

---

## 2. 构建镜像

### 2.1 本地构建

在仓库根目录执行：

```bash
docker build -t feishu-cli-mcp:latest .
```

多阶段构建：builder 阶段用 `golang:1.27.1` 编译静态二进制，runtime 阶段用
`gcr.io/distroless/static-debian12:nonroot`——不含 shell 与包管理器，攻击面最小。

注入版本号：

```bash
docker build \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S') \
  -t feishu-cli-mcp:$(git describe --tags --always) .
```

### 2.2 推送到镜像仓库

```bash
REGISTRY=registry.example.com/your-namespace
TAG=$(git describe --tags --always)
docker build -t $REGISTRY/feishu-cli-mcp:$TAG .
docker push $REGISTRY/feishu-cli-mcp:$TAG
```

然后修改 `deploy/k8s/deployment.yaml` 里的 `image:` 字段为实际地址。

> **说明**：本次交付**未在本机验证 `docker build`**（开发环境无 docker）。
> Dockerfile 的正确性依据是：二进制 `CGO_ENABLED=0` 静态编译、无 CGO 依赖、
> 资源全部 `go:embed` 内嵌（`internal/registry/*.go`）、运行时无需外部数据文件
> ——这些都已通过源码核实。首次构建若报错，优先检查 `GO_VERSION` 是否与
> `go.mod` 的 `go` directive 一致。

---

## 3. 创建 Secret

**推荐方式**（不把明文写进文件）：

```bash
kubectl create secret generic feishu-cli-mcp-secret \
  --from-literal=FEISHU_APP_ID=cli_xxxxxxxxxxxx \
  --from-literal=FEISHU_APP_SECRET=xxxxxxxxxxxxxxxx \
  --from-literal=MCP_AUTH_TOKEN=$(openssl rand -hex 32)
```

**模板方式**（适合 GitOps，注意别提交真实值）：

```bash
cp deploy/k8s/secret.example.yaml /tmp/feishu-secret.yaml
# 编辑 /tmp/feishu-secret.yaml 填入真实值
kubectl apply -f /tmp/feishu-secret.yaml
```

生产环境建议用 External Secrets Operator / Vault 从密钥管理系统同步，避免凭证
落在 Git 历史里。

`MCP_AUTH_TOKEN` 生成建议：`openssl rand -hex 32`（64 位十六进制，足够随机）。

---

## 4. 部署

```bash
kubectl apply -f deploy/k8s/deployment.yaml
kubectl apply -f deploy/k8s/service.yaml

# 查看状态
kubectl rollout status deployment/feishu-cli-mcp
kubectl get pods -l app.kubernetes.io/name=feishu-cli-mcp
```

Deployment 的关键设计：

- **`args` 注入 `--auth-token $(MCP_AUTH_TOKEN)`**：cobramcp 的 `--auth-token`
  只从命令行 flag 读取（不读环境变量），而 distroless 无 shell、CMD 里不能做
  `$(VAR)` 展开。因此改用 k8s `args`——`$(MCP_AUTH_TOKEN)` 由 **kubelet 展开**
  （不是 shell 展开），无 shell 镜像同样可用。
- **`readOnlyRootFilesystem: true`**：容器根文件系统只读。feishu-cli 在无配置
  文件时不写盘，因此该约束成立。
- **TCP 探测**：SSE 服务没有独立的 `/healthz` 端点，用 TCP 探测判断进程是否在
  监听（详见第 6 节）。

---

## 5. 验证部署

### 5.1 端口转发到本地

```bash
kubectl port-forward svc/feishu-cli-mcp 8080:8080
```

### 5.2 验证认证生效

```bash
# 不带 token → 期望 401
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/sse

# 带正确 token → 期望 200（SSE 流会挂住，用 --max-time 限制）
curl -s -o /dev/null -w "%{http_code}\n" --max-time 3 \
  -H "Authorization: Bearer <你的 MCP_AUTH_TOKEN>" \
  http://127.0.0.1:8080/sse
```

### 5.3 验证工具列表

用任意 MCP 客户端，或手工走一次 JSON-RPC（stdio 本地调试更方便）：

```bash
# 本地二进制直接验证工具面（不经网络）
feishu-cli mcp tools && cat mcp-tools.json | jq 'length'
# 期望输出 19（当前 wiki 白名单的工具数）
```

---

## 6. 健康检查说明

SSE 服务由 mcp-go 提供 `/sse`（事件流）与 `/message`（消息投递）两个端点，**没有
独立的健康检查端点**。且这两个端点都被认证中间件包裹——不带 token 访问 `/sse`
会返回 401，而非 200。

因此 liveness/readiness probe 用 **TCP 探测**（`tcpSocket.port: 8080`），它只判断
端口是否在监听，不经过认证层，语义正确。

> 不要改用 `httpGet: /sse` 做 probe——SSE 是长连接、永不结束，且需要认证，会导致
> probe 判定异常。

---

## 7. 客户端接入

### 7.1 集群内应用

MCP 客户端配置指向：

```
http://feishu-cli-mcp.<namespace>.svc.cluster.local:8080/sse
Authorization: Bearer <MCP_AUTH_TOKEN>
```

### 7.2 集群外暴露

- **简单**：把 Service 的 `type` 改为 `LoadBalancer` 或 `NodePort`。
- **Ingress**：SSE 需要长连接，务必在 Ingress 上关闭响应缓冲、放宽超时，例如
  Nginx Ingress：

  ```yaml
  metadata:
    annotations:
      nginx.ingress.kubernetes.io/proxy-buffering: "off"
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
  ```

  并确保把 `/sse` 与 `/message` 路径都路由到本 Service。

> 集群外暴露时，`--auth-token` 是唯一防线（传输层建议再套 TLS）。不要省略。

---

## 8. 常见问题

**Pod 起不来，日志报 "缺少 app_id"**
Secret 未创建或 key 名不匹配。检查 `kubectl get secret feishu-cli-mcp-secret -o
jsonpath='{.data}'`，确认 `FEISHU_APP_ID` / `FEISHU_APP_SECRET` 存在。

**调用工具返回 401**
客户端没带 `Authorization: Bearer <token>`，或 token 与 Secret 里的
`MCP_AUTH_TOKEN` 不一致。

**工具列表为空**
`mcpAllowedGroups` 白名单未包含目标命令组，或镜像构建的是旧版本代码。

**想扩展能力到其他命令组**
编辑 `cmd/mcp.go` 的 `mcpAllowedGroups` 追加组名（如 `"doc"`、`"sheet"`），重新
构建镜像。注意追加后要重新评估该组是否含破坏性命令（如 `doc` 组无删除，但
`bitable` 组有记录删除）。

**需要 User Token 类能力（mail / vc / minutes）**
本无状态形态不支持。改用带 PVC 的部署形态（把 `~/.feishu-cli` 挂到持久卷），
或注入 `FEISHU_USER_ACCESS_TOKEN`（会过期，不能自动刷新）。

---

## 9. 相关文件

| 文件 | 说明 |
| --- | --- |
| `Dockerfile` | 多阶段构建，产出 distroless 运行时镜像 |
| `.dockerignore` | 构建上下文排除项 |
| `deploy/k8s/secret.example.yaml` | Secret 模板（占位值） |
| `deploy/k8s/deployment.yaml` | Deployment（无状态，env 注入凭证与 token） |
| `deploy/k8s/service.yaml` | ClusterIP Service |
| `cmd/mcp.go` | MCP 暴露策略（白名单 `mcpAllowedGroups`） |
