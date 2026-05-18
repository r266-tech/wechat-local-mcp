//go:build darwin

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
	return []string{
		filepath.Join(h, "Library", "Containers", "com.tencent.xinWeChat", "Data", "Documents", "xwechat_files"),
	}, nil
}
