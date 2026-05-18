//go:build !darwin && !windows

package wxkey

func binaryNames() []string {
	return []string{"wxkey"}
}

func SetupSupported() bool {
	return false
}

func UnsupportedSetupMessage() string {
	return "Automatic key extraction is not implemented on this platform. Provide schema-2 keys in config.json or set WX_MCP_CONFIG to a ready config file."
}
