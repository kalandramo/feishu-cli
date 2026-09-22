package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func TestValidateSheetImageURL(t *testing.T) {
	tests := []struct {
		url          string
		allowPrivate bool
		wantErr      bool
	}{
		{"https://8.8.8.8/image.png", false, false},   // 合规公网 IP 字面量，消除离线测试 DNS 依赖
		{"http://example.com/image.png", false, true}, // 非 https
		{"https://localhost/image.png", false, true},  // localhost
		{"https://foo.localhost/img.png", false, true},
		{"https://127.0.0.1/image.png", false, true}, // loopback
		{"https://10.0.0.1/image.png", false, true},  // private ip 默认阻断
		{"https://10.0.0.1/image.png", true, false},  // allowPrivate 放行私有网络
		{"https://192.168.1.100/a.jpg", false, true},
		{"https://192.168.1.100/a.jpg", true, false},
		{"https://user:pass@example.com/img.png", false, true}, // 带凭据
		{"", false, true},
	}

	for _, tt := range tests {
		err := validateSheetImageURL(tt.url, tt.allowPrivate)
		if tt.wantErr && err == nil {
			t.Errorf("validateSheetImageURL(%q, %v) expected error, got nil", tt.url, tt.allowPrivate)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("validateSheetImageURL(%q, %v) unexpected error: %v", tt.url, tt.allowPrivate, err)
		}
	}
}

func TestDownloadSheetImage_MockTransport(t *testing.T) {
	pngData := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 100)...)

	// 1. 成功下载与 MIME 探测、建议文件名提取
	mockClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(bytes.NewReader(pngData)),
				ContentLength: int64(len(pngData)),
				Header:        make(http.Header),
			}, nil
		}),
	}

	path, name, err := downloadSheetImage(context.Background(), mockClient, "https://10.0.0.1/products/item1.png", 1024*1024, true)
	if err != nil {
		t.Fatalf("downloadSheetImage 预期成功，实际失败: %v", err)
	}
	defer os.Remove(path)

	if name != "item1.png" {
		t.Errorf("suggested name = %q, want item1.png", name)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Errorf("path %q 应该以 .png 结尾", path)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Size() != int64(len(pngData)) {
		t.Errorf("临时文件状态异常: %v", statErr)
	}

	// 2. 超出大小截断
	_, _, err = downloadSheetImage(context.Background(), mockClient, "https://10.0.0.1/products/item1.png", 50, true)
	if err == nil {
		t.Fatal("超过 maxBytes 预期报错，实际返回 nil")
	}

	// 3. 非法 MIME 类型
	txtClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader("hello plain text")),
				ContentLength: 16,
				Header:        make(http.Header),
			}, nil
		}),
	}
	_, _, err = downloadSheetImage(context.Background(), txtClient, "https://10.0.0.1/test.txt", 1024, true)
	if err == nil || !strings.Contains(err.Error(), "不支持的图片格式") && !strings.Contains(err.Error(), "支持的图片类型") {
		t.Fatalf("文本文件预期报图片类型错误，实际: %v", err)
	}

	// 4. HTTP 协议直接拒绝
	_, _, err = downloadSheetImage(context.Background(), mockClient, "http://10.0.0.1/test.png", 1024, true)
	if err == nil {
		t.Fatal("http 协议应被拒绝")
	}
}

func TestValidateLocalSheetImage(t *testing.T) {
	dir := t.TempDir()

	// 1. 合法 PNG
	pngPath := filepath.Join(dir, "test.png")
	pngBytes := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 50)...)
	if err := os.WriteFile(pngPath, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLocalSheetImage(pngPath, 1024); err != nil {
		t.Fatalf("合法 PNG 校验失败: %v", err)
	}

	// 2. 超出大小
	if _, err := validateLocalSheetImage(pngPath, 10); err == nil {
		t.Error("超过 maxBytes 应报错")
	}

	// 3. 非图片文件 (文本)
	txtPath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(txtPath, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLocalSheetImage(txtPath, 1024); err == nil {
		t.Error("文本文件应校验失败")
	}

	// 4. 空文件
	emptyPath := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(emptyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLocalSheetImage(emptyPath, 1024); err == nil {
		t.Error("空文件应校验失败")
	}
}

func TestExtractCellCoordinate(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0b1212!B2:B2", "B2"},
		{"0b1212!B2", "B2"},
		{"'Sheet1'!B2:B2", "B2"},
		{"'2024!Q1'!B2:B2", "B2"}, // 工作表名带感叹号
		{"'My!Sheet!'!C3", "C3"},
		{"B2:B2", "B2"},
		{"B2", "B2"},
		{"s1!AA10:AA10", "AA10"},
		{"b2", "B2"},
	}

	for _, tt := range tests {
		got := extractCellCoordinate(tt.input)
		if got != tt.want {
			t.Errorf("extractCellCoordinate(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestVerifiedSheetImagesByCell(t *testing.T) {
	ranges := []*CellRangeV3{
		{
			Range: "0b1212!B2:B2",
			Values: [][][]*CellElement{{{
				{Type: "image", Image: &ImageElement{ImageToken: "token-b2"}},
			}}},
		},
		{
			Range: "C3:C3", // 无前缀形式
			Values: [][][]*CellElement{{{
				{Type: "image", Image: &ImageElement{ImageToken: "token-c3"}},
			}}},
		},
		{
			Range: "'2024!Q1'!D4:D4", // 工作表名含感叹号
			Values: [][][]*CellElement{{{
				{Type: "image", Image: &ImageElement{ImageToken: "token-d4"}},
			}}},
		},
		{
			Range: "'MySheet'!E5:E5",
			Values: [][][]*CellElement{{{
				{Type: "formula", Formula: &FormulaElement{Formula: "=IMAGE()", FormulaValue: "#ERROR"}},
			}}},
		},
	}

	result := verifiedSheetImagesByCell(ranges)
	if result["B2"] != "token-b2" {
		t.Errorf("B2 expected token-b2, got %q", result["B2"])
	}
	if result["C3"] != "token-c3" {
		t.Errorf("C3 expected token-c3, got %q", result["C3"])
	}
	if result["D4"] != "token-d4" {
		t.Errorf("D4 expected token-d4, got %q", result["D4"])
	}
	if _, ok := result["E5"]; ok {
		t.Errorf("E5 was formula, should not be verified")
	}
}
