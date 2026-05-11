package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r266-tech/wx-mcp/internal/wcdb"
)

func TestParseTS_UnixSeconds(t *testing.T) {
	got, err := parseTS("1776330000")
	if err != nil || got != 1776330000 {
		t.Errorf("parseTS('1776330000') = (%d,%v), want (1776330000,nil)", got, err)
	}
}

func TestParseTS_DateOnly(t *testing.T) {
	got, err := parseTS("2026-04-16")
	if err != nil {
		t.Fatalf("parseTS error: %v", err)
	}
	want := time.Date(2026, 4, 16, 0, 0, 0, 0, time.Local).Unix()
	if got != want {
		t.Errorf("parseTS('2026-04-16') = %d, want %d", got, want)
	}
}

func TestParseTS_DateTime(t *testing.T) {
	got, err := parseTS("2026-04-16T12:30:45")
	if err != nil {
		t.Fatalf("parseTS error: %v", err)
	}
	want := time.Date(2026, 4, 16, 12, 30, 45, 0, time.Local).Unix()
	if got != want {
		t.Errorf("parseTS = %d, want %d", got, want)
	}
}

func TestParseTS_Empty(t *testing.T) {
	got, err := parseTS("")
	if err != nil || got != 0 {
		t.Errorf("parseTS('') = (%d,%v), want (0,nil)", got, err)
	}
}

func TestParseTS_Bad(t *testing.T) {
	_, err := parseTS("not-a-time")
	if err == nil {
		t.Error("parseTS('not-a-time') should error")
	}
}

func TestTalkerHash(t *testing.T) {
	// md5("wxid_testtalker0001") known value (verified via python hashlib).
	got := talkerHash("wxid_testtalker0001")
	want := "b2ed09282c82cadc5646d5a6c462c429"
	if got != want {
		t.Errorf("talkerHash = %q, want %q", got, want)
	}
}

func TestSenderPrefixRe(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"wxid_puf:\nhello", "hello"},
		{"abc-123:\r\nworld", "world"},
		{"  wxid_x:\nbody", "body"}, // leading whitespace
		{"plain text no prefix", "plain text no prefix"},
		{"https://example.com\nstuff", "https://example.com\nstuff"}, // URL not stripped (':' followed by '/')
		{"wxid_x: still text", "wxid_x: still text"},                 // ':' not followed by newline
	}
	for _, c := range cases {
		got := senderPrefixRe.ReplaceAllString(c.in, "")
		if got != c.want {
			t.Errorf("strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContentSummary_Text(t *testing.T) {
	got := contentSummary(1, 0, "wxid_x:\nhello world", nil)
	if got != "hello world" {
		t.Errorf("text = %q, want 'hello world'", got)
	}
}

func TestContentSummary_Image(t *testing.T) {
	got := contentSummary(3, 0, "<msg>...", nil)
	if got != "[图片]" {
		t.Errorf("image = %q, want '[图片]'", got)
	}
}

func TestContentSummary_AppNoTitle(t *testing.T) {
	got := contentSummary(49, 0, "<msg>...", nil)
	if got != "[应用消息]" {
		t.Errorf("app no parsed = %q, want '[应用消息]'", got)
	}
}

func TestContentSummary_AppWithTitle(t *testing.T) {
	parsed := map[string]any{"title": "微信转账"}
	got := contentSummary(49, 0, "<msg>...", parsed)
	if got != "微信转账" {
		t.Errorf("app with title = %q, want '微信转账'", got)
	}
}

func TestContentSummary_Quote(t *testing.T) {
	parsed := map[string]any{
		"title": "好的",
		"refermsg": map[string]any{
			"type":        int(1),
			"content_raw": "原话",
		},
	}
	got := contentSummary(49, 57, "<msg>...", parsed)
	if !strings.HasPrefix(got, "[引用: ") {
		t.Errorf("quote should start with '[引用: ', got %q", got)
	}
	if !strings.Contains(got, "好的") {
		t.Errorf("quote should include reply title '好的', got %q", got)
	}
}

func TestContentSummary_System(t *testing.T) {
	got := contentSummary(10000, 0, "对方撤回了一条消息", nil)
	if got != "对方撤回了一条消息" {
		t.Errorf("system = %q", got)
	}
}

func TestLiteMessagesKeepsAgentContext(t *testing.T) {
	rows := []wcdb.Row{{
		"talker":              "wxid_a",
		"talker_display_name": "Alice",
		"chat_type":           "private",
		"local_id":            int64(1),
		"content_summary":     "hello",
		"message_content":     "raw body",
	}}

	got := liteMessages(rows, "lite")
	for _, key := range []string{"talker", "talker_display_name", "chat_type", "local_id", "content_summary"} {
		if _, ok := got[0][key]; !ok {
			t.Fatalf("liteMessages removed %q", key)
		}
	}
	if _, ok := got[0]["message_content"]; ok {
		t.Fatalf("liteMessages kept raw message_content")
	}
}

func TestValidateToolArgsRejectsBadInteger(t *testing.T) {
	if err := validateToolArgs("sessions", map[string]any{"limit": "bad"}); err == nil {
		t.Fatalf("validateToolArgs should reject string limit")
	}
	if err := validateToolArgs("sessions", map[string]any{"limit": 5001}); err == nil {
		t.Fatalf("validateToolArgs should reject oversized limit")
	}
	if err := validateToolArgs("sessions", map[string]any{"limit": 50}); err != nil {
		t.Fatalf("validateToolArgs rejected valid limit: %v", err)
	}
}

func TestValidateToolArgsRequired(t *testing.T) {
	if err := validateToolArgs("search", map[string]any{}); err == nil {
		t.Fatalf("validateToolArgs should reject missing required keyword")
	}
}

func TestValidateToolArgsRejectsUnknownAndBadEnums(t *testing.T) {
	if err := validateToolArgs("sessions", map[string]any{"bogus": "x"}); err == nil {
		t.Fatalf("validateToolArgs should reject unknown args")
	}
	if err := validateToolArgs("messages", map[string]any{"fields": "raw"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad fields enum")
	}
	if err := validateToolArgs("export_messages", map[string]any{"path": "/tmp/x", "format": "csv"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad format enum")
	}
	if err := validateToolArgs("sessions", map[string]any{"type_filter": "private,group"}); err != nil {
		t.Fatalf("validateToolArgs rejected comma type_filter: %v", err)
	}
	if err := validateToolArgs("sessions", map[string]any{"type_filter": "nope"}); err == nil {
		t.Fatalf("validateToolArgs should reject unknown type_filter")
	}
	if err := validateToolArgs("resolve_chat", map[string]any{"chat": "张三"}); err != nil {
		t.Fatalf("validateToolArgs should allow resolve_chat aliases: %v", err)
	}
}

func TestCacheCursorRoundTrip(t *testing.T) {
	cursor := makeCacheCursor(1776330000, "room:colon@chatroom", 42)
	got, err := parseCacheCursor(cursor)
	if err != nil {
		t.Fatalf("parseCacheCursor returned error: %v", err)
	}
	if got.CreateTime != 1776330000 || got.Talker != "room:colon@chatroom" || got.LocalID != 42 || got.LegacyRowID != 0 {
		t.Fatalf("parseCacheCursor = %#v", got)
	}
	legacy, err := parseCacheCursor("1776330000:99")
	if err != nil {
		t.Fatalf("legacy parseCacheCursor returned error: %v", err)
	}
	if legacy.CreateTime != 1776330000 || legacy.LegacyRowID != 99 {
		t.Fatalf("legacy parseCacheCursor = %#v", legacy)
	}
}

func TestParseSnsXMLMediaMetadata(t *testing.T) {
	raw := `<SnsDataItem><TimelineObject><id>tid1</id><username>wxid_a</username><createTime>1776330000</createTime><contentDesc>hello</contentDesc><ContentObject><type>1</type><mediaList><media><type>15</type><sub_type>10</sub_type><url enc_idx="1" key="video-key" token="video-token">https://example.test/video.mp4</url><thumb enc_idx="0" key="thumb-key" token="thumb-token">https://example.test/thumb.jpg</thumb><size width="720" height="1280" totalSize="123456" /><videomd5>video-md5</videomd5><videoDuration>37</videoDuration></media></mediaList></ContentObject></TimelineObject><LocalExtraInfo><nickname>Alice</nickname></LocalExtraInfo></SnsDataItem>`
	post, err := parseSnsXML(raw)
	if err != nil {
		t.Fatalf("parseSnsXML returned error: %v", err)
	}
	if len(post.Media) != 1 {
		t.Fatalf("media len = %d, want 1", len(post.Media))
	}
	m := post.Media[0]
	if m.Type != "video" || m.RawType != 15 || m.SubType != "10" {
		t.Fatalf("media type fields = %#v", m)
	}
	if m.URLKey != "video-key" || m.URLToken != "video-token" || m.URLEncIdx != "1" {
		t.Fatalf("url metadata missing: %#v", m)
	}
	if m.ThumbKey != "thumb-key" || m.ThumbToken != "thumb-token" || m.ThumbEncIdx != "0" {
		t.Fatalf("thumb metadata missing: %#v", m)
	}
	if m.Width != 720 || m.Height != 1280 || m.TotalSize != 123456 || m.VideoMD5 != "video-md5" || m.VideoDuration != 37 {
		t.Fatalf("size/video metadata missing: %#v", m)
	}
}

func TestBoundedReadSQL(t *testing.T) {
	got, err := boundedReadSQL("SELECT id FROM t ORDER BY id DESC", 10)
	if err != nil {
		t.Fatalf("boundedReadSQL returned error: %v", err)
	}
	want := "SELECT * FROM (SELECT id FROM t ORDER BY id DESC) LIMIT 10"
	if got != want {
		t.Fatalf("boundedReadSQL = %q, want %q", got, want)
	}

	if _, err := boundedReadSQL("DELETE FROM t", 10); err == nil {
		t.Fatalf("boundedReadSQL should reject writes")
	}
	if _, err := boundedReadSQL("SELECT 1; SELECT 2", 10); err == nil {
		t.Fatalf("boundedReadSQL should reject multiple statements")
	}
}

func TestAcquireCacheRefreshLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unlock, acquired, lockPath, err := acquireCacheRefreshLock()
	if err != nil {
		t.Fatalf("acquireCacheRefreshLock returned error: %v", err)
	}
	if !acquired {
		t.Fatalf("first lock acquire should succeed")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".wx-mcp", "cache-refresh.lock")); err != nil {
		t.Fatalf("lock dir missing at %s: %v", lockPath, err)
	}

	_, acquired2, _, err := acquireCacheRefreshLock()
	if err != nil {
		t.Fatalf("second acquire returned error: %v", err)
	}
	if acquired2 {
		t.Fatalf("second acquire should report busy")
	}
	unlock()

	_, err = os.Stat(lockPath)
	if !os.IsNotExist(err) {
		t.Fatalf("lock should be removed after unlock, stat err=%v", err)
	}
}
