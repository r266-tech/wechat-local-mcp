//go:build darwin

package wxkey

func binaryNames() []string {
	return []string{"wxkey"}
}

func SetupSupported() bool {
	return true
}

func UnsupportedSetupMessage() string {
	return ""
}
