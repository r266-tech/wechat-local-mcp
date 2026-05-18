//go:build !darwin && !windows

package config

import "fmt"

func DefaultWeChatBases() ([]string, error) {
	return nil, fmt.Errorf("WeChat DB autodetection is not supported on this platform; set WX_MCP_DB_ROOT")
}
