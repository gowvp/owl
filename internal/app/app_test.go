package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gowvp/owl/internal/conf"
)

// TestIsValidAPISecret 测试 API 秘钥语法规则校验
// 为什么: 确保只允许数字、大小写字母、下划线，且长度限制在 1 到 32 之间
func TestIsValidAPISecret(t *testing.T) {
	cases := []struct {
		name  string
		input string
		valid bool
	}{
		{"空字符串非法", "", false},
		{"单字符合法", "a", true},
		{"标准数字字母下划线", "abc_123_XYZ", true},
		{"恰好32位合法", strings.Repeat("a", 32), true},
		{"超过32位非法", strings.Repeat("a", 33), false},
		{"包含减号连字符非法", "abc-123", false},
		{"包含空格非法", "abc 123", false},
		{"包含点号非法", "abc.123", false},
		{"包含特殊字符非法", "abc@123", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidAPISecret(tc.input)
			if got != tc.valid {
				t.Errorf("isValidAPISecret(%q) = %v, 期望 %v", tc.input, got, tc.valid)
			}
		})
	}
}

// TestInitAPISecret 测试启动时为空或非法时的自愈生成与持久化
// 为什么: 验证当配置为空时自动生成 32 位 UUID，当配置不符合语法时记录告警并重新生成合规密钥
func TestInitAPISecret(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// 1. 测试为空场景
	bc := &conf.Bootstrap{
		ConfigPath: cfgPath,
	}
	// 先写一个空配置
	if err := conf.WriteConfig(bc, cfgPath); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	initAPISecret(bc)
	secret1 := bc.Server.HTTP.APISecret
	if !isValidAPISecret(secret1) {
		t.Errorf("为空时生成的 secret %q 不合规", secret1)
	}
	if len(secret1) != 32 {
		t.Errorf("期望生成32位长度，实际 %d", len(secret1))
	}

	// 验证已持久化到文件
	var readBc conf.Bootstrap
	if err := conf.SetupConfig(&readBc, cfgPath); err != nil {
		t.Fatalf("读取持久化配置失败: %v", err)
	}
	if readBc.Server.HTTP.APISecret != secret1 {
		t.Errorf("持久化文件中的秘钥 %q 与内存 %q 不一致", readBc.Server.HTTP.APISecret, secret1)
	}

	// 2. 测试非法字符场景（例如包含连字符）
	bc.Server.HTTP.APISecret = "invalid-secret-with-dash"
	initAPISecret(bc)
	secret2 := bc.Server.HTTP.APISecret
	if !isValidAPISecret(secret2) {
		t.Errorf("非法重置后生成的 secret %q 不合规", secret2)
	}
	if secret2 == "invalid-secret-with-dash" {
		t.Errorf("未重新生成合规秘钥")
	}

	// 3. 测试合法场景（不应被修改）
	const validCustom = "my_custom_valid_secret_99"
	bc.Server.HTTP.APISecret = validCustom
	initAPISecret(bc)
	if bc.Server.HTTP.APISecret != validCustom {
		t.Errorf("合法秘钥被错误修改: 期望 %q, 实际 %q", validCustom, bc.Server.HTTP.APISecret)
	}
}
