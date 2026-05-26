//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func wcdbLibraryCandidates() []string {
	names := []string{"libWCDB.dll", "WCDB.dll"}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			dirs = append(dirs, dir, filepath.Join(dir, "lib"), filepath.Join(dir, "..", "lib"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".config", "wxcli", "lib"))
	}
	var candidates []string
	for _, dir := range dirs {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	return candidates
}

func wcdbLibraryMissingMessage() string {
	return "WCDB DLL not found. Put libWCDB.dll or WCDB.dll beside wechat-cli.exe, under .\\lib, under %USERPROFILE%\\.config\\wxcli\\lib, or set WECHAT_CLI_WCDB_LIB"
}
