package cmd

import (
	"strings"
	"testing"

	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// 本测试钉住 MCP 暴露面的白名单不变量：暴露策略一旦被改动（新增命令组、
// 放宽匹配、改挂载时机），这些断言会立即失败。
//
// 背景：feishu-cli 有 400+ 命令，默认全量暴露会显著占用 LLM 上下文并让危险命令
// （交互式登录、长驻订阅、写操作）一并暴露。因此「默认全关、只开白名单组」与
// 「敏感 flag 不暴露」是必须被测试守护的契约，而不是一次性人工核对。

// buildMCPSelectorTree 构造一棵贴近真实形态的命令树：
// 一个带守卫标记的分组命令（wiki）、一个非白名单组（doc）、一个名字里含
// 白名单词的越界命令（move-docs-to-wiki，用于验证精确匹配而非子串匹配）。
func buildMCPSelectorTree() *cobra.Command {
	root := &cobra.Command{Use: "feishu-cli"}

	// 白名单组：模拟 installUnknownSubcommandGuard 注入 RunE + annotation 后的形态。
	wiki := &cobra.Command{Use: "wiki", RunE: func(*cobra.Command, []string) error { return nil }}
	wiki.Annotations = map[string]string{groupGuardAnnotation: "1"}
	wiki.AddCommand(&cobra.Command{
		Use:  "create",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	wiki.AddCommand(&cobra.Command{
		Use:  "get <node_token>",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(wiki)

	// 非白名单组：默认全关，必须不可见。
	doc := &cobra.Command{Use: "doc"}
	doc.AddCommand(&cobra.Command{
		Use:  "create",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(doc)

	// 越界检查：名字里含 "wiki" 但不属于 wiki 组。
	root.AddCommand(&cobra.Command{
		Use:  "move-docs-to-wiki",
		RunE: func(*cobra.Command, []string) error { return nil },
	})

	return root
}

func TestMCPSelector_AllowsOnlyWhitelistedGroups(t *testing.T) {
	root := buildMCPSelectorTree()

	cases := []struct {
		path string
		want bool // true = 应暴露
	}{
		{"wiki create", true},
		{"wiki get", true},
		{"doc create", false},        // 非白名单组
		{"move-docs-to-wiki", false}, // 名字含 wiki 但非 wiki 组（精确匹配，非子串）
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

func TestMCPSelector_ExcludesGroupGuardCommands(t *testing.T) {
	root := buildMCPSelectorTree()

	wiki, _, err := root.Find([]string{"wiki"})
	if err != nil {
		t.Fatal(err)
	}
	// 分组命令自身带守卫 RunE，仍必须被排除——否则会作为一个空工具暴露。
	if mcpCmdSelector(wiki) {
		t.Errorf("带 groupGuardAnnotation 的分组命令不得暴露为 MCP 工具")
	}
}

func TestMCPSelector_ExcludesNonRunnableCommands(t *testing.T) {
	root := &cobra.Command{Use: "feishu-cli"}
	// 无 Run/RunE/PreRun 的纯容器命令（未经过守卫注入的形态）。
	container := &cobra.Command{Use: "wiki"}
	container.AddCommand(&cobra.Command{
		Use:  "child",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(container)

	cmd, _, _ := root.Find([]string{"wiki"})
	if mcpCmdSelector(cmd) {
		t.Errorf("不可运行的纯容器命令不得暴露为 MCP 工具")
	}
}

func TestMCPSelector_ExcludesRootCommand(t *testing.T) {
	root := &cobra.Command{Use: "feishu-cli", RunE: func(*cobra.Command, []string) error { return nil }}
	// 根命令不属于任何组，不得暴露。
	if mcpCmdSelector(root) {
		t.Errorf("根命令不得暴露为 MCP 工具")
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

// TestMCPAllowedGroups_ContainsWiki 钉住「知识库能力默认开放」这一当前产品决策。
// 若有人误删白名单项导致知识库能力消失，这里会失败。
func TestMCPAllowedGroups_ContainsWiki(t *testing.T) {
	found := false
	for _, g := range mcpAllowedGroups {
		if g == "wiki" {
			found = true
		}
	}
	if !found {
		t.Errorf("mcpAllowedGroups 必须包含 wiki（知识库能力）")
	}
}
