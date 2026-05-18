//go:build windows

package wxkey

func binaryNames() []string {
	return []string{"wxkey.exe", "wxkey"}
}

func SetupSupported() bool {
	return true
}

func UnsupportedSetupMessage() string {
	return ""
}
