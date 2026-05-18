//go:build windows

package wxkey

import "testing"

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
