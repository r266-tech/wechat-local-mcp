//go:build windows

package wxkey

import (
	"testing"
	"time"
)

func TestScanRawKeyLiteralsFindsTargetSalt(t *testing.T) {
	key := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	salt := "ffeeddccbbaa99887766554433221100"
	data := []byte("noise x'" + key + salt + "' tail")
	found := map[string]string{}
	hits := scanRawKeyLiterals(data, map[string]bool{salt: true}, found)
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if got := found[salt]; got != key {
		t.Fatalf("found key = %q, want %q", got, key)
	}
}

func TestScanRawKeyLiteralsIgnoresNonTargetSalt(t *testing.T) {
	key := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	salt := "ffeeddccbbaa99887766554433221100"
	data := []byte("x'" + key + salt + "'")
	found := map[string]string{}
	hits := scanRawKeyLiterals(data, map[string]bool{"00000000000000000000000000000000": true}, found)
	if hits != 0 {
		t.Fatalf("hits = %d, want 0", hits)
	}
	if len(found) != 0 {
		t.Fatalf("found = %v, want empty", found)
	}
}

func TestWindowsKeyScanTimeoutEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "duration", value: "1500ms", want: 1500 * time.Millisecond},
		{name: "seconds", value: "7", want: 7 * time.Second},
		{name: "disabled", value: "-1", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WECHAT_CLI_KEY_SCAN_TIMEOUT", tt.value)
			if got := windowsKeyScanTimeout(); got != tt.want {
				t.Fatalf("windowsKeyScanTimeout = %s, want %s", got, tt.want)
			}
		})
	}
}
