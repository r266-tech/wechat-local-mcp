// Package wxkey is wx-mcp's thin client for the standalone `wxkey` CLI.
// The CLI handles task_for_pid + memory scan + SQLCipher verification;
// this package finds the binary,
// invokes `wxkey setup`, and parses the JSON it prints to stdout. First-run
// human/agent setup should usually call `wxkey bootstrap` explicitly; wx-mcp
// keeps runtime startup on the narrower setup path so it does not silently
// re-sign or restart WeChat.
package wxkey

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FindBinary locates the wxkey CLI. Resolution order:
//  1. $WX_KEY_BIN — explicit override
//  2. next to the calling executable (the recommended distribution layout)
//  3. PATH lookup
func FindBinary() (string, error) {
	if p := os.Getenv("WX_KEY_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range binaryNames() {
			cand := filepath.Join(dir, name)
			if _, err := os.Stat(cand); err == nil {
				return cand, nil
			}
		}
	}
	for _, name := range binaryNames() {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("wxkey binary not found — set $WX_KEY_BIN, install wxkey alongside wx-mcp, or put wxkey on PATH")
}

// SetupResult mirrors what `wxkey setup` writes to stdout. We only consume
// the bits wx-mcp needs.
type SetupResult struct {
	PID        int               `json:"pid"`
	Root       string            `json:"scan_root"`
	WxID       string            `json:"wxid"`
	ConfigPath string            `json:"config_path"`
	Stats      json.RawMessage   `json:"stats"`
	Results    []ResultEntry     `json:"results"`
	Keys       map[string]string `json:"-"` // populated from Results post-decode
}

type ResultEntry struct {
	DBRel    string `json:"db_rel"`
	DBPath   string `json:"db_path"`
	SaltHex  string `json:"salt_hex"`
	KeyHex   string `json:"key_hex"`
	VerifyAs string `json:"verify_as"`
}

// RunSetup refreshes schema-2 per-DB keys. On macOS/Linux builds this invokes
// the standalone `wxkey setup` helper and parses its JSON output. On Windows it
// uses the in-process adapter in setup_windows.go.
//
// The macOS path intentionally does not run `wxkey bootstrap`, because
// bootstrap may quit, ad-hoc re-sign, and reopen WeChat.
// stderrText is also returned so wx-mcp can surface progress / errors.
func RunSetup() (*SetupResult, string, error) {
	return runSetup()
}

func runSetupExternal() (*SetupResult, string, error) {
	bin, err := FindBinary()
	if err != nil {
		return nil, "", err
	}
	cmd := exec.Command(bin, "setup", "--quiet")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	if runErr != nil {
		return nil, stderr.String(), fmt.Errorf("wxkey setup failed: %w (stderr: %s)", runErr, stderr.String())
	}
	// Elevated wxkey children can still write progress or sudo diagnostics ahead
	// of the JSON. Strip everything before the first '{' so the JSON object
	// parses cleanly.
	payload := stdout
	if i := bytes.IndexByte(payload, '{'); i > 0 {
		payload = payload[i:]
	}
	var res SetupResult
	if err := json.Unmarshal(payload, &res); err != nil {
		// stdout contains key_hex on the success path; never echo it back through
		// an error message that may surface to LLM clients. Diagnose by re-running
		// `wxkey setup` directly in a terminal.
		return nil, stderr.String(), fmt.Errorf("parse wxkey setup output: %w (stdout %d bytes; rerun `wxkey setup` directly to inspect)", err, len(stdout))
	}
	res.Keys = make(map[string]string, len(res.Results))
	for _, r := range res.Results {
		res.Keys[r.SaltHex] = r.KeyHex
	}
	return &res, stderr.String(), nil
}
