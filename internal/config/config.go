package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is wechat-cli's persistent key map. By default it intentionally stays
// at ~/.config/wxcli/config.json for wxkey / wx-cli compatibility;
// WECHAT_CLI_CONFIG or the legacy WX_MCP_CONFIG can point at an explicit file.
//
// Schema 2 carries a per-DB enc_key map: each WCDB file's SQLCipher salt maps
// to its 32-byte post-PBKDF2 encryption key. This is the only ready runtime
// state. Schema 1's legacy master password is intentionally ignored.
type Config struct {
	SchemaVersion int               `json:"schema_version,omitempty"`
	Wxid          string            `json:"wxid"`
	DBRoot        string            `json:"db_root"`
	Keys          map[string]string `json:"keys,omitempty"`
	ImageKey      string            `json:"image_key,omitempty"`
	ImageXORKey   *int              `json:"image_xor_key,omitempty"`
	Key           string            `json:"key,omitempty"`
	KeyPID        int               `json:"key_pid,omitempty"`
	KeyEpoch      int64             `json:"key_epoch,omitempty"`
}

// Ready reports whether the config has enough material to open WCDB files via
// wechat-cli's supported runtime path: schema-2 per-salt enc_keys.
func (c *Config) Ready() bool {
	if c == nil {
		return false
	}
	return len(c.Keys) > 0
}

func dir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(h, ".config", "wxcli")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func Path() (string, error) {
	if p := firstEnv("WECHAT_CLI_CONFIG", "WX_MCP_CONFIG"); p != "" {
		return filepath.Clean(p), nil
	}
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := &Config{}
			applyEnvOverrides(cfg)
			return cfg, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	applyEnvOverrides(&c)
	return &c, nil
}

func Save(c *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func applyEnvOverrides(c *Config) {
	if c == nil {
		return
	}
	if root := firstEnv("WECHAT_CLI_DB_ROOT", "WX_MCP_DB_ROOT"); root != "" {
		c.DBRoot = filepath.Clean(root)
		if c.Wxid == "" {
			c.Wxid = wxidFromAccountDir(c.DBRoot)
		}
	}
	if key := firstEnv("WECHAT_CLI_IMAGE_KEY", "WX_MCP_IMAGE_KEY"); key != "" {
		c.ImageKey = key
	}
}

func DefaultWeChatBase() (string, error) {
	bases, err := DefaultWeChatBases()
	if err != nil {
		return "", err
	}
	if len(bases) == 0 {
		return "", fmt.Errorf("no default WeChat data roots for this platform")
	}
	return bases[0], nil
}

func AutoDetectDBRoot() (string, string, error) {
	if root := firstEnv("WECHAT_CLI_DB_ROOT", "WX_MCP_DB_ROOT"); root != "" {
		root = filepath.Clean(root)
		if _, err := os.Stat(filepath.Join(root, "db_storage")); err != nil {
			return "", "", fmt.Errorf("WECHAT_CLI_DB_ROOT=%s does not contain db_storage: %w", root, err)
		}
		return root, wxidFromAccountDir(root), nil
	}

	type cand struct{ full, wxid string }
	var cands []cand
	var checked []string
	bases, err := DefaultWeChatBases()
	if err != nil {
		return "", "", err
	}
	for _, base := range bases {
		checked = append(checked, base)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			switch name {
			case "all_users", "applet", "backup", "wmpf":
				continue
			}
			full := filepath.Join(base, name)
			if _, err := os.Stat(filepath.Join(full, "db_storage")); err == nil {
				cands = append(cands, cand{full, wxidFromAccountDir(full)})
			}
		}
	}
	switch len(cands) {
	case 0:
		return "", "", fmt.Errorf("no account directory with db_storage found under checked WeChat roots:\n%s", strings.Join(checked, "\n"))
	case 1:
		return cands[0].full, cands[0].wxid, nil
	}

	var lines []string
	for _, c := range cands {
		lines = append(lines, fmt.Sprintf("  %s  (wxid=%s)", c.full, c.wxid))
	}
	return "", "", fmt.Errorf("multiple WeChat accounts found; refusing to autodetect.\nCandidates:\n%s\nSet WECHAT_CLI_DB_ROOT to the intended account directory", strings.Join(lines, "\n"))
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func wxidFromAccountDir(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if idx := lastIndex(name, "_"); idx > 0 {
		return name[:idx]
	}
	return name
}

func withXWeChatFilesBase(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return []string{path, filepath.Join(path, "xwechat_files")}
}

func lastIndex(s, sep string) int {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
