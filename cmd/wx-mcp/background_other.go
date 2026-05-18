//go:build !darwin && !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func cacheLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wx-mcp", "logs"), nil
}

func configureBackgroundCommand(cmd *exec.Cmd) {
}
