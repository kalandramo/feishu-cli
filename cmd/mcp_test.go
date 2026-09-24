package cmd

import (
	"strings"
	"testing"

	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本测试钉住 MCP 暴露面的安全不变量：裁剪逻辑一旦被改动（新增命令组、
// 调整排除清单、改挂载时机），这些断言会立即失败。
//
// 背景：cobramcp 默认暴露全部命令，且 root 的 persistent flag 会被继承到
// 每个工具——包括 bot-app-secret。因此「危险命令不暴露」与「敏感 flag 不暴露」
// 是必须被测试守护的契约，而不是一次性人工核对。

// buildMCPSelectorTree 构造一棵贴近真实形态的命令树：
// 一个带守卫标记的分组命令、一个普通可运行命令、一个被排除的交互命令。
func buildMCPSelectorTree() *cobra.Command {
	root := &cobra.Command{Use: "feishu-cli"}

	// 分组命令：模拟 installUnknownSubcommandGuard 注入 RunE + annotation 后的形态。
	group := &cobra.Command{Use: "auth", RunE: func(*cobra.Command, []string) error { return nil }}
	group.Annotations = map[string]string{groupGuardAnnotation: "1"}
	group.AddCommand(&cobra.Command{
		Use:  "login",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	group.AddCommand(&cobra.Command{
		Use:  "status",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(group)

	// 普通业务命令：应被暴露。
	root.AddCommand(&cobra.Command{
		Use:  "schema",
		RunE: func(*cobra.Command, []string) error { return nil },
	})

	// 长驻命令：应被排除。
	event := &cobra.Command{Use: "event"}
	event.AddCommand(&cobra.Command{
		Use:  "consume <event_key>",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(event)

	return root
}

func TestMCPSelector_ExcludesGroupGuardCommands(t *testing.T) {
	root := buildMCPSelectorTree()

	auth, _, err := root.Find([]string{"auth"})
	if err != nil {
		t.Fatal(err)
	}
	// 分组命令自身带守卫 RunE，仍必须被排除——否则会作为一个空工具暴露。
	if mcpCmdSelector(auth) {
		t.Errorf("带 groupGuardAnnotation 的分组命令不得暴露为 MCP 工具")
	}

	// 分组下的具体命令按各自语义判定。
	login, _, _ := root.Find([]string{"auth", "login"})
	if mcpCmdSelector(login) {
		t.Errorf("auth login 属排除清单，不得暴露")
	}
	status, _, _ := root.Find([]string{"auth", "status"})
	if !mcpCmdSelector(status) {
		t.Errorf("auth status 应被暴露")
	}
}

func TestMCPSelector_ExcludesDangerousCommands(t *testing.T) {
	root := buildMCPSelectorTree()

	cases := []struct {
		path string
		want bool // true = 应暴露
	}{
		{"auth login", false},
		{"auth status", true},
		{"event consume", false},
		{"schema", true},
	}
	for _, tc := range cases {
		cmd, _, err := root.Find(strings.Fields(tc.path))
		if err != nil {
			t.Fatalf("查找 %q 失败: %v", tc.path, err)
		}
		if got := mcpCmdSelector(cmd); got != tc.want {
			t.Errorf("%q 暴露=%v，期望 %v", tc.path, got, tc.want)
		}
	}
}

func TestMCPSelector_ExcludesNonRunnableCommands(t *testing.T) {
	root := &cobra.Command{Use: "feishu-cli"}
	// 无 Run/RunE/PreRun 的纯容器命令（未经过守卫注入的形态）。
	container := &cobra.Command{Use: "group"}
	container.AddCommand(&cobra.Command{
		Use:  "child",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(container)

	cmd, _, _ := root.Find([]string{"group"})
	if mcpCmdSelector(cmd) {
		t.Errorf("不可运行的纯容器命令不得暴露为 MCP 工具")
	}
}

func TestMCPConfig_ExcludesSensitiveFlags(t *testing.T) {
	// 直接验证配置中的 flag 排除器：bot-app-secret 必须被排除。
	excl := cobramcp.ExcludeFlags(mcpExcludedFlags...)

	if excl(&pflag.Flag{Name: "bot-app-secret"}) {
		t.Errorf("bot-app-secret 是敏感凭证 flag，必须被排除")
	}
	// 对照组：非敏感 flag 必须保留（确认裁剪不过度）。
	if !excl(&pflag.Flag{Name: "bot-app-id"}) {
		t.Errorf("bot-app-id 非敏感，应保留")
	}
}
