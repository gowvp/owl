package conf

import "testing"

func TestNewInstallHasNoDefaultWebAccount(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.Username != "" || cfg.Server.Password != "" {
		t.Fatal("new install still has a default Web account")
	}
	var sample Bootstrap
	if err := SetupConfig(&sample, "../../configs/config.toml"); err != nil {
		t.Fatal(err)
	}
	if !sample.NeedsInit() {
		t.Fatal("shipped configuration bypasses initialization")
	}
}
