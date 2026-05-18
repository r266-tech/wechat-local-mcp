package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathUsesExplicitConfig(t *testing.T) {
	want := filepath.Join(t.TempDir(), "wxcli", "config.json")
	t.Setenv("WX_MCP_CONFIG", want)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("Path = %q, want %q", got, filepath.Clean(want))
	}
}

func TestLoadAppliesDBRootOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	root := filepath.Join(dir, "wxid_test_1234")
	if err := os.MkdirAll(filepath.Join(root, "db_storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"wxid":"old","db_root":"old","keys":{"salt":"key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	t.Setenv("WX_MCP_DB_ROOT", root)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBRoot != filepath.Clean(root) {
		t.Fatalf("DBRoot = %q, want %q", cfg.DBRoot, filepath.Clean(root))
	}
	if cfg.Wxid != "old" {
		t.Fatalf("existing wxid should be preserved, got %q", cfg.Wxid)
	}
}

func TestAutoDetectDBRootUsesEnvOverride(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wxid_env_9999")
	if err := os.MkdirAll(filepath.Join(root, "db_storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WX_MCP_DB_ROOT", root)
	gotRoot, wxid, err := AutoDetectDBRoot()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != filepath.Clean(root) {
		t.Fatalf("root = %q, want %q", gotRoot, filepath.Clean(root))
	}
	if wxid != "wxid_env" {
		t.Fatalf("wxid = %q, want wxid_env", wxid)
	}
}

func TestWithXWeChatFilesBase(t *testing.T) {
	root := filepath.Join("Users", "v", "Documents", "WeChat Files")
	got := withXWeChatFilesBase(root)
	want := []string{root, filepath.Join(root, "xwechat_files")}
	if len(got) != len(want) {
		t.Fatalf("variants = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("variants[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
