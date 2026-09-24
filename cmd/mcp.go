package cmd

import (
	"strings"

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
// 暴露策略是**白名单（default-deny）**：只有 mcpAllowedGroups 列出的命令组才会
// 成为 MCP 工具，其余一律不可见。
//
// 为什么用白名单而非黑名单：feishu-cli 有 400+ 命令，其中大量是写操作、长驻进程、
// 或交互式命令。黑名单（默认全开、逐个排除）会随命令树增长不断漏出新的危险命令——
// 新增命令默认可见，是 fail-open 的。白名单反过来：新增命令默认不可见，要暴露必须
// 显式加入 mcpAllowedGroups。全量暴露 417 个工具还会显著占用 LLM 上下文、降低工具
// 选择准确率。

// mcpAllowedGroups 是允许暴露给 MCP 客户端的顶层命令组白名单。
//
// 当前只开放知识库（wiki）。要扩展能力时在这里追加命令组名即可（如 "doc"、"sheet"）；
// 每追加一个组都应重新评估该组的写操作/破坏性命令是否需要进一步过滤。
var mcpAllowedGroups = []string{"wiki"}

// mcpExcludedFlags 是不向 MCP 客户端暴露的 flag 名。
//
// bot-app-secret 是敏感凭证：它作为 root persistent flag 会被继承到每一个工具，
// 若不排除，MCP 客户端可以在任意工具调用里传明文 app secret，且会进入 LLM 上下文
// 与调用日志。这是硬性安全边界，与暴露哪些命令组无关。
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

// mcpCmdSelector 决定一个命令是否作为 MCP 工具暴露（白名单语义：默认拒绝）。
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

	// 白名单：只有命令路径的顶层组在 mcpAllowedGroups 中才放行。
	return mcpIsAllowedGroup(cmd)
}

// mcpIsAllowedGroup 判断命令是否属于白名单中的顶层命令组。
//
// 命令路径形如 "feishu-cli wiki create"；取根名之后的第一个段作为顶层组名做精确
// 匹配。刻意不用子串匹配（cobramcp.AllowCmdsContaining）——子串匹配会把
// "move-docs-to-wiki" 这类名字里恰好含 wiki 的非知识库命令误放进来。
func mcpIsAllowedGroup(cmd *cobra.Command) bool {
	segments := strings.Fields(cmd.CommandPath())
	if len(segments) < 2 {
		// 根命令本身（无子命令路径）不属于任何组，不暴露。
		return false
	}
	topGroup := segments[1]
	for _, allowed := range mcpAllowedGroups {
		if topGroup == allowed {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.AddCommand(newMCPCommand())
}
