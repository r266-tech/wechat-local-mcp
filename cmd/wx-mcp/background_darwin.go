//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func cacheLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "wx-mcp"), nil
}

func configureBackgroundCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
