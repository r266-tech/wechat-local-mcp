package main

import "testing"

func TestCacheWorkerCountHonorsEnv(t *testing.T) {
	t.Setenv("WX_MCP_CACHE_WORKERS", "2")
	if got := cacheWorkerCount(10); got != 2 {
		t.Fatalf("cacheWorkerCount = %d, want 2", got)
	}
	t.Setenv("WX_MCP_CACHE_WORKERS", "99")
	if got := cacheWorkerCount(3); got != 3 {
		t.Fatalf("cacheWorkerCount should cap at total, got %d", got)
	}
	t.Setenv("WX_MCP_CACHE_WORKERS", "0")
	if got := cacheWorkerCount(3); got != 1 {
		t.Fatalf("cacheWorkerCount should floor at 1, got %d", got)
	}
}

func TestQueryMessageIndexPageRejectsBadTableByConstruction(t *testing.T) {
	if validMsgTable("Msg_not_hex") {
		t.Fatalf("bad message table should not be valid")
	}
	if !validMsgTable("Msg_0123456789abcdef0123456789abcdef") {
		t.Fatalf("valid message table rejected")
	}
}
