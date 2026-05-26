//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

func wcdbLibraryCandidates() []string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			candidates = append(candidates,
				filepath.Join(dir, "libWCDB.dylib"),
				filepath.Join(dir, "lib", "libWCDB.dylib"),
				filepath.Join(dir, "..", "lib", "libWCDB.dylib"),
				filepath.Join(dir, "lib", "WCDB.framework", "Versions", "2.1.15", "WCDB"),
			)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "wxcli", "lib", "libWCDB.dylib"))
	}
	return candidates
}

func wcdbLibraryMissingMessage() string {
	return "libWCDB.dylib not found. Put it beside wechat-cli, under ./lib, under ~/.config/wxcli/lib, or set WECHAT_CLI_WCDB_LIB / WECHAT_CLI_WCDB_DYLIB"
}
