//go:build !windows

package wxkey

func runSetup() (*SetupResult, string, error) {
	return runSetupExternal()
}
