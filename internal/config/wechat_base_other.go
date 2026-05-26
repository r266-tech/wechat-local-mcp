//go:build !darwin && !windows

package config

import "fmt"

func DefaultWeChatBases() ([]string, error) {
	return nil, fmt.Errorf("WeChat DB autodetection is not supported on this platform; set WECHAT_CLI_DB_ROOT")
}
