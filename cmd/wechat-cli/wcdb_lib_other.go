//go:build !darwin && !windows

package main

func wcdbLibraryCandidates() []string {
	return nil
}

func wcdbLibraryMissingMessage() string {
	return "WCDB dynamic library autodetection is not supported on this platform; set WECHAT_CLI_WCDB_LIB"
}
