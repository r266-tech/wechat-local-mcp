//go:build windows

package config

import (
	"os"
	"path/filepath"
)

func DefaultWeChatBases() ([]string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var bases []string
	if documents := os.Getenv("USERPROFILE"); documents != "" {
		for _, root := range []string{
			filepath.Join(documents, "Documents", "WeChat Files"),
			filepath.Join(documents, "WeChat Files"),
			filepath.Join(documents, "AppData", "Roaming", "Tencent", "WeChat", "WeChat Files"),
		} {
			bases = append(bases, withXWeChatFilesBase(root)...)
		}
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		bases = append(bases, withXWeChatFilesBase(filepath.Join(appData, "Tencent", "WeChat", "WeChat Files"))...)
	}
	for _, root := range []string{
		filepath.Join(h, "Documents", "WeChat Files"),
		filepath.Join(h, "WeChat Files"),
	} {
		bases = append(bases, withXWeChatFilesBase(root)...)
	}
	return uniquePaths(bases), nil
}

func uniquePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		key := filepath.ToSlash(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}
