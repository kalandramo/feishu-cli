// skillmeta 使用项目已有 YAML 解析器读取 Skill frontmatter，供无第三方依赖的 Python 校验器调用。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type result struct {
	Metadata map[string]any `json:"metadata,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func parse(path string) result {
	data, err := os.ReadFile(path)
	if err != nil {
		return result{Error: err.Error()}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return result{Error: "缺少起始 frontmatter 分隔符 ---"}
	}
	end := 1
	for end < len(lines) && lines[end] != "---" {
		end++
	}
	if end == len(lines) {
		return result{Error: "缺少结束 frontmatter 分隔符 ---"}
	}
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "\n")))
	var metadata map[string]any
	// 解码到 map 会校验重复键，并保留数字/布尔等标量类型，避免把它们强制转成字符串。
	if err := decoder.Decode(&metadata); err != nil {
		return result{Error: fmt.Sprintf("YAML 解析失败: %v", err)}
	}
	if metadata == nil {
		return result{Error: "frontmatter 必须是非空 YAML mapping"}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result{Error: "frontmatter 只能包含一个 YAML 文档"}
	}
	if _, err := json.Marshal(metadata); err != nil {
		return result{Error: fmt.Sprintf("frontmatter 字段必须使用字符串键: %v", err)}
	}
	return result{Metadata: metadata}
}

func main() {
	results := make(map[string]result, len(os.Args)-1)
	for _, path := range os.Args[1:] {
		results[path] = parse(path)
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
