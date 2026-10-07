package conf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestDefaultONVIFCredentials 验证新安装有独立凭据，避免沿用已知的网页登录密码。
func TestDefaultONVIFCredentials(t *testing.T) {
	data, err := toml.Marshal(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ONVIF struct {
			Username string
			Password string
		}
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.ONVIF.Username != "admin" {
		t.Fatalf("ONVIF 缺省用户名应为 admin，实际 %q", config.ONVIF.Username)
	}
	if len(config.ONVIF.Password) != 10 {
		t.Fatalf("ONVIF 随机密码应为 10 位，实际 %d 位", len(config.ONVIF.Password))
	}
	for _, digit := range config.ONVIF.Password {
		if digit < '0' || digit > '9' {
			t.Fatal("ONVIF 随机密码应只包含数字")
		}
	}
}

// TestInitONVIF 验证旧配置和部分配置能自动补全，且自定义值、网页登录凭据与重启后的值保持稳定。
func TestInitONVIF(t *testing.T) {
	cases := []struct {
		name, input, username, password string
	}{
		{"缺少配置段", "", "admin", ""},
		{"空配置", "[ONVIF]\nUsername = ''\nPassword = ''", "admin", ""},
		{"只配用户名", "[ONVIF]\nUsername = 'nvr'", "nvr", ""},
		{"只配密码", "[ONVIF]\nPassword = 'custom-password'", "admin", "custom-password"},
		{"完整配置", "[ONVIF]\nUsername = 'nvr'\nPassword = 'custom-password'", "nvr", "custom-password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			input := "[Server]\nUsername = 'web-user'\nPassword = 'web-password'\n" + tc.input
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			var cfg Bootstrap
			if err := SetupConfig(&cfg, path); err != nil {
				t.Fatal(err)
			}
			cfg.ConfigPath = path
			if err := cfg.InitONVIF(); err != nil {
				t.Fatal(err)
			}
			checkONVIFCredentials(t, &cfg, tc.username, tc.password)
			checkPersistedONVIF(t, &cfg)
		})
	}
}

// checkONVIFCredentials 检查补全后的值，避免密码规则正确却误改 Web 或自定义凭据。
func checkONVIFCredentials(t *testing.T, cfg *Bootstrap, username, password string) {
	t.Helper()
	if cfg.Server.Username != "web-user" || cfg.Server.Password != "web-password" {
		t.Fatal("ONVIF 初始化不应修改网页登录凭据")
	}
	if cfg.ONVIF.Username != username {
		t.Fatal("ONVIF 用户名与期望不符")
	}
	if password != "" {
		if cfg.ONVIF.Password != password {
			t.Fatal("自定义 ONVIF 密码不应被修改")
		}
		return
	}
	if len(cfg.ONVIF.Password) != 10 {
		t.Fatal("生成的密码必须为 10 位")
	}
	for _, digit := range cfg.ONVIF.Password {
		if digit < '0' || digit > '9' {
			t.Fatal("生成的密码必须为纯数字")
		}
	}
}

// checkPersistedONVIF 从真实配置文件重新加载，验证重启不换密码且完整配置无需重复写入。
func checkPersistedONVIF(t *testing.T, cfg *Bootstrap) {
	t.Helper()
	var reloaded Bootstrap
	if err := SetupConfig(&reloaded, cfg.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if reloaded.ONVIF != cfg.ONVIF || reloaded.Server.Username != cfg.Server.Username || reloaded.Server.Password != cfg.Server.Password {
		t.Fatal("保存后的配置与内存不一致")
	}
	reloaded.ConfigPath = filepath.Join(t.TempDir(), "不存在", "config.toml")
	if err := reloaded.InitONVIF(); err != nil {
		t.Fatal("完整凭据不应触发写配置", err)
	}
	if reloaded.ONVIF != cfg.ONVIF {
		t.Fatal("重启后不应重新生成 ONVIF 凭据")
	}
}

// TestInitONVIFWriteFailure 验证保存失败会返回错误，避免内存中悄悄生效一组无法恢复的凭据。
func TestInitONVIFWriteFailure(t *testing.T) {
	cfg := Bootstrap{ConfigPath: filepath.Join(t.TempDir(), "不存在", "config.toml")}
	if err := cfg.InitONVIF(); err == nil {
		t.Fatal("保存失败必须返回错误")
	}
	if cfg.ONVIF != (ONVIF{}) {
		t.Fatal("保存失败不应修改内存凭据")
	}
}
