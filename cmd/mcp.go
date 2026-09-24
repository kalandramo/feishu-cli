package cmd

import (
	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
)

// 本文件把 feishu-cli 的 Cobra 命令树以 MCP（Model Context Protocol）server 形式
// 暴露给外部 AI 客户端（VSCode Copilot / Cursor 等）。
//
// 采用 cobramcp 的**子进程模型**：每次工具调用都以还原出的参数重新执行 feishu-cli
// 二进制。之所以不用进程内模型，是因为 feishu-cli 的命令是包级全局单例
// （var xxxCmd = &cobra.Command{}），不满足进程内模型要求的「每次调用返回全新命令树」
// 的 factory 契约；子进程模型正好复用现有无状态设计。
//
// 默认挂载会暴露全部命令（含交互式 auth login、长驻 event consume 等），且 root 的
// persistent flag（含 bot-app-secret）会被继承到每个工具——因此这里必须显式裁剪。

// mcpExcludedCommands 是不向 MCP 客户端暴露的命令路径片段。
//
// 排除标准：交互式（阻塞等待用户输入）、长驻进程、破坏本地凭证/配置、或纯本地
// 环境管理——这些命令交给 LLM 调用要么挂死、要么造成不可预期的本地状态变更。
// 采用「两段式路径片段」而非单词，是为了避免误伤（例如用 "config init" 而非 "init"）。
var mcpExcludedCommands = []string{
	// 交互式 OAuth 登录/登出/刷新：会阻塞等待浏览器回调或用户输入
	"auth login",
	"auth logout",
	"auth refresh",
	// 长驻事件订阅进程
	"event consume",
	"event stop",
	// 本地配置初始化 / 应用注册（会写盘并可能触发交互）
	"config init",
	"config create-app",
	// 本地 profile 写操作（改变后续所有命令的身份解析）
	"profile add",
	"profile use",
	"profile remove",
	"profile rename",
	"profile migrate",
	// 环境体检：依赖网络与本地环境，结果对 LLM 价值低
	"doctor",
}

// mcpExcludedFlags 是不向 MCP 客户端暴露的 flag 名。
//
// bot-app-secret 是敏感凭证：它作为 root persistent flag 会被继承到每一个工具，
// 若不排除，MCP 客户端可以在任意工具调用里传明文 app secret，且会进入 LLM 上下文
// 与调用日志。这是硬性安全边界，不是可选优化。
var mcpExcludedFlags = []string{"bot-app-secret"}

// newMCPCommand 构造挂载到 root 的 `mcp` 命令（含 start/stream/tools/rest 及
// vscode/cursor 配置子命令）。
func newMCPCommand() *cobra.Command {
	return cobramcp.Command(&cobramcp.Config{
		CommandName: "mcp",
		Selectors: []cobramcp.Selector{
			{
				CmdSelector:           mcpCmdSelector,
				LocalFlagSelector:     cobramcp.ExcludeFlags(mcpExcludedFlags...),
				InheritedFlagSelector: cobramcp.ExcludeFlags(mcpExcludedFlags...),
			},
		},
	})
}

// mcpCmdSelector 决定一个命令是否作为 MCP 工具暴露。
func mcpCmdSelector(cmd *cobra.Command) bool {
	// 纯分组命令（自身无 Run/RunE）不暴露——它们只是容器，没有可执行语义。
	//
	// 注意：feishu-cli 的 installUnknownSubcommandGuard 会给「有子命令但不可运行」
	// 的命令组注入 RunE 并打上 groupGuardAnnotation 标记，因此这里不能只判断
	// RunE == nil（挂载时机在 Execute 之后，RunE 已被注入）；必须识别该标记。
	if cmd.Annotations[groupGuardAnnotation] == "1" {
		return false
	}
	if cmd.Run == nil && cmd.RunE == nil && cmd.PreRun == nil && cmd.PreRunE == nil {
		return false
	}

	// 排除交互/长驻/破坏性命令。
	return cobramcp.ExcludeCmdsContaining(mcpExcludedCommands...)(cmd)
}

func init() {
	rootCmd.AddCommand(newMCPCommand())
}
