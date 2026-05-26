package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"

	"github.com/r266-tech/wechat-local-mcp/internal/config"
	"github.com/r266-tech/wechat-local-mcp/internal/wcdb"
	"github.com/r266-tech/wechat-local-mcp/internal/wxkey"
	"github.com/r266-tech/wechat-local-mcp/internal/wxkind"
	"github.com/r266-tech/wechat-local-mcp/internal/wxparse"
)

// ──────────────────── MCP protocol types ────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"` // nil for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema any            `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ──────────────────── server state ────────────────────

type server struct {
	cfg                       *config.Config
	wcdbPath                  string
	ok                        bool
	keyRefreshMu              sync.Mutex
	keyRefreshLast            map[string]time.Time
	imageKeyRefreshMu         sync.Mutex
	imageKeyRefreshLast       time.Time
	readableImageIndexMu      sync.Mutex
	readableImageIndexRoot    string
	readableImageIndexBuiltAt time.Time
	readableImageIndex        map[string][]readableImageCandidate
}

var (
	runWxkeySetup    = wxkey.RunSetup
	runWxkeyImageKey = wxkey.RunImageKey
)

// findWCDB locates the platform WCDB dynamic library.
func findWCDB() (string, error) {
	var candidates []string
	if p := envFirst("WECHAT_CLI_WCDB_LIB", "WECHAT_CLI_WCDB_DYLIB", "WX_MCP_WCDB_LIB", "WX_MCP_WCDB_DYLIB"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, wcdbLibraryCandidates()...)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New(wcdbLibraryMissingMessage())
}

/*
		return "", fmt.Errorf("libWCDB.dylib 未找到。把它放在 wechat-cli 旁边 (./lib/libWCDB.dylib), ~/.config/wxcli/lib/, 或设置 WECHAT_CLI_WCDB_DYLIB")
	}
*/
func (s *server) ensure() error {
	if s.ok {
		return nil
	}
	wcdbPath, err := findWCDB()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DBRoot == "" {
		root, wxid, err := config.AutoDetectDBRoot()
		if err != nil {
			return fmt.Errorf("未找到微信数据目录 (微信已登录?): %w", err)
		}
		cfg.DBRoot = root
		if cfg.Wxid == "" {
			cfg.Wxid = wxid
		}
		_ = config.Save(cfg)
	}
	if !cfg.Ready() {
		s.cfg = cfg
		s.wcdbPath = wcdbPath
		if err := s.refreshKeysFromWxkey("no schema-2 DB keys cached"); err != nil {
			return err
		}
		cfg = s.cfg
	}
	s.cfg = cfg
	s.wcdbPath = wcdbPath
	s.ok = true
	return nil
}

func (s *server) refreshKeysFromWxkey(reason string) error {
	s.keyRefreshMu.Lock()
	defer s.keyRefreshMu.Unlock()

	if s.refreshReasonAlreadySatisfied(reason) {
		return nil
	}
	if !wxkey.SetupSupported() {
		msg := wxkey.UnsupportedSetupMessage()
		if msg == "" {
			msg = "automatic key extraction is not supported on this platform"
		}
		return fmt.Errorf("%s Reason: %s", msg, reason)
	}
	const retryInterval = 30 * time.Second
	key := keyRefreshReasonKey(reason)
	if s.keyRefreshLast == nil {
		s.keyRefreshLast = map[string]time.Time{}
	}
	if last, ok := s.keyRefreshLast[key]; ok && time.Since(last) < retryInterval {
		return fmt.Errorf("%s; wxkey setup was already attempted recently for this condition", reason)
	}
	s.keyRefreshLast[key] = time.Now()
	fmt.Fprintf(os.Stderr, "[%s] %s — running wxkey key setup...\n", appName, reason)
	res, stderr, err := runWxkeySetup()
	if err != nil {
		return fmt.Errorf("wxkey setup failed: %w\n%s\nOn macOS, run `wxkey bootstrap` once to prepare the no-SIP key cache. On Windows, keep WeChat logged in, verify WECHAT_CLI_DB_ROOT matches the logged-in account, then retry.", err, stderr)
	}
	fresh, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config after wxkey setup: %w", err)
	}
	if !fresh.Ready() {
		return fmt.Errorf("wxkey setup completed but config still has no schema-2 enc_key map")
	}
	s.cfg = fresh
	s.ok = true
	fmt.Fprintf(os.Stderr, "[%s] wxkey setup OK — %d per-DB keys cached for wxid=%s\n", appName,
		len(res.Keys), res.WxID)
	return nil
}

func (s *server) refreshImageKeyFromWxkey(reason string, force bool) error {
	if envFirst("WECHAT_CLI_IMAGE_KEY", "WX_MCP_IMAGE_KEY") != "" {
		return fmt.Errorf("WECHAT_CLI_IMAGE_KEY is set; not overriding explicit image_key env")
	}
	s.imageKeyRefreshMu.Lock()
	defer s.imageKeyRefreshMu.Unlock()

	root := ""
	if s.cfg != nil {
		root = s.cfg.DBRoot
	}
	if fresh, err := config.Load(); err == nil {
		if fresh.DBRoot != "" {
			root = fresh.DBRoot
		}
		if !force && strings.TrimSpace(fresh.ImageKey) != "" && fresh.ImageXORKey != nil {
			s.cfg = fresh
			s.ok = fresh.Ready()
			return nil
		}
	}
	const retryInterval = 30 * time.Second
	if !s.imageKeyRefreshLast.IsZero() && time.Since(s.imageKeyRefreshLast) < retryInterval {
		return fmt.Errorf("%s; wxkey image-key was already attempted recently", reason)
	}
	s.imageKeyRefreshLast = time.Now()
	fmt.Fprintf(os.Stderr, "[%s] %s — running wxkey image-key setup...\n", appName, reason)
	img, stderr, err := runWxkeyImageKey(root)
	if err != nil {
		return fmt.Errorf("wxkey image-key failed: %w\n%s\nOn macOS, run `wxkey bootstrap` once to prepare the no-SIP key cache, keep WeChat logged in, open an image chat, then retry.", err, stderr)
	}
	if img == nil || strings.TrimSpace(img.Key) == "" {
		return fmt.Errorf("wxkey image-key completed without an image_key")
	}
	fresh, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload config after wxkey image-key: %w", err)
	}
	fresh.ImageKey = strings.TrimSpace(img.Key)
	fresh.ImageXORKey = img.XORKey
	if err := config.Save(fresh); err != nil {
		return fmt.Errorf("save image_key config: %w", err)
	}
	s.cfg = fresh
	s.ok = fresh.Ready()
	fmt.Fprintf(os.Stderr, "[%s] wxkey image-key OK — image_key cached\n", appName)
	return nil
}

func (s *server) refreshReasonAlreadySatisfied(reason string) bool {
	fresh, err := config.Load()
	if err != nil || !fresh.Ready() {
		return false
	}
	reasonKey := keyRefreshReasonKey(reason)
	if strings.HasPrefix(reasonKey, "salt:") {
		salt := strings.TrimPrefix(reasonKey, "salt:")
		if _, ok := fresh.Keys[salt]; !ok {
			return false
		}
	} else if reasonKey != "empty-schema2-key-map" {
		return false
	}
	s.cfg = fresh
	s.ok = true
	return true
}

func keyRefreshReasonKey(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "unknown"
	}
	if i := strings.Index(reason, "no enc_key for salt "); i >= 0 {
		rest := reason[i+len("no enc_key for salt "):]
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			return "salt:" + fields[0]
		}
	}
	if strings.Contains(reason, "no schema-2 DB keys cached") {
		return "empty-schema2-key-map"
	}
	if len(reason) > 160 {
		return reason[:160]
	}
	return reason
}

func (s *server) openDB(subdir, file string) (*wcdb.DB, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if err := wcdb.Bootstrap(s.wcdbPath); err != nil {
		return nil, err
	}
	resolvedDB, err := s.validatedDBPath(subdir, file)
	if err != nil {
		return nil, err
	}
	if len(s.cfg.Keys) > 0 {
		db, err := wcdb.OpenWithKeyMap(resolvedDB, s.cfg.Keys)
		if err == nil || !isMissingEncKeyErr(err) {
			return db, err
		}
		if setupErr := s.refreshKeysFromWxkey(err.Error()); setupErr != nil {
			return nil, setupErr
		}
		return wcdb.OpenWithKeyMap(resolvedDB, s.cfg.Keys)
	}
	if err := s.refreshKeysFromWxkey("no schema-2 DB keys cached"); err != nil {
		return nil, err
	}
	return wcdb.OpenWithKeyMap(resolvedDB, s.cfg.Keys)
}

func isMissingEncKeyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no enc_key for salt")
}

func (s *server) validatedDBPath(subdir, file string) (string, error) {
	dbStorage := filepath.Join(s.cfg.DBRoot, "db_storage")
	dbPath := filepath.Clean(filepath.Join(dbStorage, subdir, file))
	// Containment layer 1 (lexical): catch obvious `..` / absolute escape before
	// any FS call. Defense in depth — readonly is not a scope.
	rel, err := filepath.Rel(dbStorage, dbPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes db_storage: subdir=%q file=%q", subdir, file)
	}
	// Containment layer 2 (resolved): catch symlink escape — a planted symlink
	// under db_storage could point anywhere on disk and lexical check wouldn't
	// see it. EvalSymlinks both ends and re-check rel against resolved root.
	resolvedStorage, err := filepath.EvalSymlinks(dbStorage)
	if err != nil {
		return "", fmt.Errorf("resolve db_storage: %w", err)
	}
	resolvedDB, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return "", fmt.Errorf("resolve db path: %w", err)
	}
	relReal, err := filepath.Rel(resolvedStorage, resolvedDB)
	if err != nil || strings.HasPrefix(relReal, "..") || filepath.IsAbs(relReal) {
		return "", fmt.Errorf("path escapes db_storage after symlink resolution: subdir=%q file=%q", subdir, file)
	}
	// Pass resolvedDB (real path) to wcdb so the file we validated is the file
	// that gets opened — closes any TOCTOU window between check and open.
	return resolvedDB, nil
}

// msgShardRE matches message / biz_message shard filenames.
// message_<n>.db holds regular (friend/group) chat; biz_message_<n>.db holds
// official-account (gh_ / brandsessionholder) chat. Shard count is dynamic —
// WCDB grows new files as data scales — so glob instead of hardcoding 0..4.
var msgShardRE = regexp.MustCompile(`^(message|biz_message)_\d+\.db$`)

type msgShardDB struct {
	Name string
	DB   *wcdb.DB
}

func (s *server) findMsgDB(tableName string) (*wcdb.DB, error) {
	shards, err := s.findMsgDBs(tableName)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(shards); i++ {
		shards[i].DB.Close()
	}
	return shards[0].DB, nil
}

func (s *server) findMsgDBs(tableName string) ([]msgShardDB, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.cfg.DBRoot, "db_storage", "message")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read message dir: %w", err)
	}
	var shards []string
	for _, e := range entries {
		if !e.IsDir() && msgShardRE.MatchString(e.Name()) {
			shards = append(shards, e.Name())
		}
	}
	var openErrs []error
	var found []msgShardDB
	for _, name := range shards {
		db, err := s.openDB("message", name)
		if err != nil {
			openErrs = append(openErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		rows, err := db.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", tableName)
		if err == nil && len(rows) > 0 {
			found = append(found, msgShardDB{Name: name, DB: db})
			continue
		}
		db.Close()
	}
	if len(found) > 0 {
		return found, nil
	}
	if len(openErrs) > 0 {
		return nil, fmt.Errorf("table %s not found in opened message shards; %d/%d shards could not be opened, first error: %v", tableName, len(openErrs), len(shards), openErrs[0])
	}
	return nil, fmt.Errorf("table %s not found in %d message shards", tableName, len(shards))
}

func closeMsgDBs(shards []msgShardDB) {
	for _, shard := range shards {
		shard.DB.Close()
	}
}

// ──────────────────── main loop ────────────────────

func main() {
	if maybeRunCLI(os.Args[1:]) {
		return
	}
	if len(os.Args) > 0 {
		printCLIUsage()
	}
	os.Exit(2)
}

func runMCPServer() {
	srv := &server{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		if req.ID == nil { // notification — no response
			continue
		}
		resp := srv.handle(req)
		out, _ := json.Marshal(resp)
		fmt.Fprintf(os.Stdout, "%s\n", out)
	}
}

func (s *server) handle(req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": appName, "version": appVersion},
			"instructions": "Errors and partial-success signals are embedded in normal tool returns — read them, don't paper over.\n" +
				"- Per-record `error` fields (e.g. `no enc_key for salt ...`) mean that specific db is unreadable; surface that to the user, do not silently treat it as `no data`.\n" +
				"- wechat-cli automatically refreshes missing DB enc_keys and WeChat V4 image_key when the stored no-SIP wxkey credential is available; if a tool still returns warnings/errors, surface the exact reason and next action.\n" +
				"- Freshness check: `sessions limit=1`, compare `last_timestamp` to now. Stale by hours = WeChat likely not running.",
		}}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": listedToolDefs()}}
	case "tools/call":
		var p toolCallParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: errResult("invalid tools/call params: " + err.Error())}
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: s.callTool(p)}
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32601, Message: "unknown method"}}
	}
}

func (s *server) callTool(p toolCallParams) toolResult {
	handlers := map[string]func(map[string]any) (any, error){
		"sessions":               s.toolSessions,
		"resolve_chat":           s.toolResolveChat,
		"contacts":               s.toolContacts,
		"messages":               s.toolMessages,
		"chat_timeline":          s.toolChatTimeline,
		"media_resources":        s.toolMediaResources,
		"group_members":          s.toolGroupMembers,
		"sns":                    s.toolSns,
		"sns_feed":               s.toolSnsFeed,
		"sns_search":             s.toolSnsSearch,
		"sns_notifications":      s.toolSnsNotifications,
		"search":                 s.toolSearch,
		"sql":                    s.toolSQL,
		"transfers":              s.toolTransfers,
		"red_packets":            s.toolRedPackets,
		"favorites":              s.toolFavorites,
		"chatroom_announcements": s.toolChatroomAnnouncements,
		"forward_history":        s.toolForwardHistory,
		"schema":                 s.toolSchema,
		"cache_status":           s.toolCacheStatus,
		"cache_refresh":          s.toolCacheRefresh,
		"cache_rebuild":          s.toolCacheRebuild,
		"unread":                 s.toolUnread,
		"stats":                  s.toolStats,
		"export_messages":        s.toolExportMessages,
	}
	fn, ok := handlers[p.Name]
	if !ok {
		return errResult("unknown tool: " + p.Name)
	}
	if err := validateToolArgs(p.Name, p.Arguments); err != nil {
		return errResult(err.Error())
	}
	result, err := fn(p.Arguments)
	if err != nil {
		return errResult(err.Error())
	}
	b, _ := json.Marshal(result)
	return toolResult{Content: []contentBlock{{Type: "text", Text: string(b)}}}
}

func errResult(msg string) toolResult {
	return toolResult{IsError: true, Content: []contentBlock{{Type: "text", Text: msg}}}
}

// ──────────────────── tool handlers ────────────────────

func (s *server) toolSessions(a map[string]any) (any, error) {
	if rows, ok, err := s.cacheSessions(a); ok || err != nil {
		return rows, err
	}
	db, err := s.openDB("session", "session.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	where = append(where, "COALESCE(is_hidden, 0) = 0")
	if tf := getStr(a, "type_filter"); tf != "" && tf != "all" {
		switch tf {
		case "group":
			where = append(where, "username LIKE '%@chatroom'")
		case "friend":
			where = append(where, `username NOT LIKE '%@chatroom'
				AND username NOT LIKE 'gh!_%' ESCAPE '!'
				AND username NOT LIKE '%@openim'
				AND username NOT LIKE '%@weclaw'
				AND username NOT LIKE '%@stranger'`)
		case "official_account":
			where = append(where, "username LIKE 'gh!_%' ESCAPE '!'")
		case "bot":
			where = append(where, "username LIKE '%@weclaw'")
		}
	}
	if kw := getStr(a, "keyword"); kw != "" {
		// Cross-db: also include sessions whose talker matches display_name /
		// nick_name / remark / alias in contact.db (fuzzy, case+space insensitive).
		matched := s.findUsernamesByFuzzyName(kw)
		clauses := []string{"username LIKE ? COLLATE NOCASE", "summary LIKE ? COLLATE NOCASE"}
		like := "%" + kw + "%"
		args = append(args, like, like)
		if len(matched) > 0 {
			ph := make([]string, len(matched))
			for i, u := range matched {
				ph[i] = "?"
				args = append(args, u)
			}
			clauses = append(clauses, fmt.Sprintf("username IN (%s)", strings.Join(ph, ",")))
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	args = append(args, getInt(a, "limit", 50))
	rows, err := db.Query(fmt.Sprintf(`SELECT username, unread_count, summary,
		last_timestamp, sort_timestamp,
		last_msg_sender AS last_sender_wxid, last_sender_display_name,
		last_msg_type, last_msg_sub_type
		FROM SessionTable
		WHERE %s
		ORDER BY sort_timestamp DESC
		LIMIT ?`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	s.attachDisplayNames(rows, [2]string{"username", "display_name"})
	for _, r := range rows {
		bk, _ := r["last_msg_type"].(int64)
		st, _ := r["last_msg_sub_type"].(int64)
		r["last_msg_kind_name"] = wxkind.Resolve(int32(bk), int32(st))
		u, _ := r["username"].(string)
		r["chat_type"] = agentChatType(u, wxkind.ClassifyUsername(u), false)
		// Aggregator sessions (brandsessionholder / brandservicesessionholder)
		// wrap the real sender in "_$_CUSTOM_USERNAME_PREFIX_$_<aggId>:<realId>".
		// The aggId is UI-internal noise; keep only the real wxid / gh_ id.
		if v, ok := r["last_sender_wxid"].(string); ok {
			r["last_sender_wxid"] = stripAggSenderPrefix(v)
		}
		for _, k := range []string{"last_sender_wxid", "last_sender_display_name"} {
			if v, ok := r[k].(string); ok && v == "" {
				delete(r, k)
			}
		}
	}
	return rows, nil
}

const aggSenderPrefix = "_$_CUSTOM_USERNAME_PREFIX_$_"

// aggregatorSessions are WeChat UI "folder" sessions that bundle other sessions.
// They have no Msg_<hash> table of their own — the real messages live under
// the contained account's own wxid (e.g. each gh_* public account).
var aggregatorSessions = map[string]bool{
	"brandsessionholder":        true, // 订阅号合集
	"brandservicesessionholder": true, // 服务号合集
}

func stripAggSenderPrefix(s string) string {
	if !strings.HasPrefix(s, aggSenderPrefix) {
		return s
	}
	rest := s[len(aggSenderPrefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[i+1:]
	}
	return rest
}

func (s *server) toolContacts(a map[string]any) (any, error) {
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if getBool(a, "groups_only") {
		where = append(where, "username LIKE '%@chatroom'")
	}
	if getBool(a, "friends_only") {
		where = append(where, "username NOT LIKE '%@chatroom' AND username NOT LIKE 'gh_%' AND username NOT LIKE '%@openim'")
	}
	if kw := getStr(a, "keyword"); kw != "" {
		// Fuzzy match: case-insensitive (COLLATE NOCASE) + whitespace-tolerant
		// via REPLACE(field, ' ', '') — so "aiagent" / "AI agent" / "Ai Agent"
		// all match a contact named "AI Agent".
		where = append(where, `(username LIKE ? COLLATE NOCASE
			OR nick_name LIKE ? COLLATE NOCASE
			OR REPLACE(nick_name, ' ', '') LIKE ? COLLATE NOCASE
			OR remark LIKE ? COLLATE NOCASE
			OR REPLACE(remark, ' ', '') LIKE ? COLLATE NOCASE
			OR alias LIKE ? COLLATE NOCASE
			OR pin_yin_initial LIKE ? COLLATE NOCASE)`)
		like := "%" + kw + "%"
		likeNoSpace := "%" + strings.ReplaceAll(kw, " ", "") + "%"
		args = append(args, like, like, likeNoSpace, like, likeNoSpace, like, like)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := db.Query(fmt.Sprintf(`SELECT username, alias, remark, nick_name,
		COALESCE(NULLIF(remark, ''), NULLIF(nick_name, ''), username) AS display_name,
		description, verify_flag
		FROM contact %s
		ORDER BY nick_name
		LIMIT %d`, wc, getInt(a, "limit", 50)), args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		u, _ := r["username"].(string)
		typ := wxkind.ClassifyUsername(u)
		r["type"] = typ
		vf, _ := r["verify_flag"].(int64)
		r["is_verified"] = vf != 0
		r["chat_type"] = agentChatType(u, typ, vf != 0)
		delete(r, "verify_flag")
		for _, k := range []string{"alias", "remark", "description"} {
			if v, ok := r[k].(string); ok && v == "" {
				delete(r, k)
			}
		}
	}
	return rows, nil
}

func (s *server) toolMessages(a map[string]any) (any, error) {
	rows, page, queryOrder, displayOrder, err := s.loadMessageRowsForOutput(a)
	if err != nil {
		return nil, err
	}
	view, err := messagesView(a)
	if err != nil {
		return nil, err
	}
	if view == "agent" {
		msgs := agentMessages(rows, includeDebugOutput(a))
		return messageTimelineEnvelope(a, rows, msgs, page, queryOrder, displayOrder), nil
	}
	mode, err := fieldsMode(a)
	if err != nil {
		return nil, err
	}
	return liteMessages(rows, mode, includeDebugOutput(a)), nil
}

func (s *server) toolChatTimeline(a map[string]any) (any, error) {
	args := chatTimelineMessageArgs(a)
	rows, page, queryOrder, displayOrder, err := s.loadMessageRowsForOutput(args)
	if err != nil {
		return nil, err
	}
	msgs := agentMessages(rows, includeDebugOutput(args))
	return messageTimelineEnvelope(args, rows, msgs, page, queryOrder, displayOrder), nil
}

type messagePageInfo struct {
	Limit      int
	Offset     int
	Returned   int
	HasMore    bool
	NextOffset int
}

func (s *server) loadMessageRowsForOutput(a map[string]any) ([]wcdb.Row, messagePageInfo, string, string, error) {
	queryOrder, err := messageQueryOrderSQL(a)
	if err != nil {
		return nil, messagePageInfo{}, "", "", err
	}
	rows, page, err := s.queryLiveMessages(a, queryOrder)
	if err != nil {
		return nil, messagePageInfo{}, "", "", err
	}
	if includeMediaPathsForMessages(a) {
		if err := s.enrichMessageMediaResources(rows); err != nil {
			return nil, messagePageInfo{}, "", "", err
		}
	}
	displayOrder, err := messagesDisplayOrder(a)
	if err != nil {
		return nil, messagePageInfo{}, "", "", err
	}
	applyMessageDisplayOrder(rows, displayOrder)
	page.Returned = len(rows)
	if page.HasMore {
		page.NextOffset = page.Offset + page.Returned
	}
	return rows, page, queryOrder, displayOrder, nil
}

func chatTimelineMessageArgs(a map[string]any) map[string]any {
	args := copyToolArgs(a)
	args["view"] = "agent"
	if _, ok := args["order"]; !ok {
		args["order"] = "desc"
	}
	if _, ok := args["display_order"]; !ok {
		args["display_order"] = "asc"
	}
	if include, ok := args["include_images"].(bool); ok {
		args["include_media_paths"] = include
		delete(args, "include_images")
	}
	return args
}

func copyToolArgs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a)+4)
	for k, v := range a {
		out[k] = v
	}
	return out
}

func messageTimelineEnvelope(args map[string]any, rows []wcdb.Row, messages []map[string]any, page messagePageInfo, queryOrder, displayOrder string) map[string]any {
	return compactMap(map[string]any{
		"query":     chatTimelineQueryMeta(args, rows, page, queryOrder, displayOrder, len(messages)),
		"freshness": chatTimelineFreshnessMeta(args, rows),
		"messages":  messages,
	})
}

func chatTimelineQueryMeta(args map[string]any, rows []wcdb.Row, page messagePageInfo, queryOrder, displayOrder string, returned int) map[string]any {
	limit := page.Limit
	if limit == 0 {
		limit = getInt(args, "limit", 50)
	}
	offset := page.Offset
	if offset == 0 {
		offset = getInt(args, "offset", 0)
	}
	meta := compactMap(map[string]any{
		"chat":          getStr(args, "chat"),
		"talker":        firstNonEmpty(rowString(firstRow(rows), "talker"), getStr(args, "talker")),
		"display_name":  rowString(firstRow(rows), "talker_display_name"),
		"limit":         limit,
		"offset":        offset,
		"order":         normalizeOrderArg(getStr(args, "order")),
		"display_order": displayOrder,
		"after":         getStr(args, "after"),
		"before":        getStr(args, "before"),
		"keyword":       getStr(args, "keyword"),
		"type":          firstNonEmpty(getStr(args, "kind_name"), getStr(args, "type")),
		"sender":        getStr(args, "sender"),
		"returned":      returned,
	})
	if meta["order"] == "" {
		meta["order"] = "desc"
	}
	if meta["display_order"] == "" {
		meta["display_order"] = "query"
	}
	meta["limit"] = limit
	meta["offset"] = offset
	meta["returned"] = returned
	meta["has_more"] = page.HasMore
	if page.HasMore {
		meta["next_offset"] = page.NextOffset
	}
	if oldest, newest := messageTimeBounds(rows); oldest != "" || newest != "" {
		meta["oldest_time"] = oldest
		meta["newest_time"] = newest
	}
	_ = queryOrder
	return meta
}

func chatTimelineFreshnessMeta(args map[string]any, rows []wcdb.Row) map[string]any {
	meta := compactMap(map[string]any{
		"message_source":      "live_message_db",
		"metadata_cache_role": metadataCacheRole(args),
		"last_message_time":   newestMessageTime(rows),
	})
	return meta
}

func metadataCacheRole(args map[string]any) string {
	chat := strings.TrimSpace(getStr(args, "chat"))
	sender := strings.TrimSpace(getStr(args, "sender"))
	if (chat != "" && !looksLikeRawChatID(chat)) || (sender != "" && !looksLikeRawChatID(sender)) {
		return "name_resolution_only"
	}
	return ""
}

func firstRow(rows []wcdb.Row) wcdb.Row {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func messageTimeBounds(rows []wcdb.Row) (string, string) {
	var minTS, maxTS int64
	for _, r := range rows {
		ts := rowInt64(r, "create_time")
		if ts == 0 {
			continue
		}
		if minTS == 0 || ts < minTS {
			minTS = ts
		}
		if maxTS == 0 || ts > maxTS {
			maxTS = ts
		}
	}
	return formatUnixLocal(minTS), formatUnixLocal(maxTS)
}

func newestMessageTime(rows []wcdb.Row) string {
	_, newest := messageTimeBounds(rows)
	return newest
}

func formatUnixLocal(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func messageQueryOrderSQL(a map[string]any) (string, error) {
	switch normalizeOrderArg(getStr(a, "order")) {
	case "", "desc":
		return "sort_seq DESC, local_id DESC", nil
	case "asc":
		return "sort_seq ASC, local_id ASC", nil
	default:
		return "", fmt.Errorf("invalid order=%q: must be \"desc\" or \"asc\"", getStr(a, "order"))
	}
}

func messagesDisplayOrder(a map[string]any) (string, error) {
	switch normalizeOrderArg(getStr(a, "display_order")) {
	case "", "query":
		return "query", nil
	case "desc":
		return "desc", nil
	case "asc":
		return "asc", nil
	default:
		return "", fmt.Errorf("invalid display_order=%q: must be \"query\", \"desc\", or \"asc\"", getStr(a, "display_order"))
	}
}

func normalizeOrderArg(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "newest", "newest_first", "latest", "latest_first":
		return "desc"
	case "oldest", "oldest_first", "chronological":
		return "asc"
	default:
		return s
	}
}

func applyMessageDisplayOrder(rows []wcdb.Row, order string) {
	if order == "" || order == "query" || len(rows) < 2 {
		return
	}
	asc := order == "asc"
	sort.SliceStable(rows, func(i, j int) bool {
		ti := rowInt64(rows[i], "create_time")
		tj := rowInt64(rows[j], "create_time")
		if ti != tj {
			if asc {
				return ti < tj
			}
			return ti > tj
		}
		li := rowInt64(rows[i], "local_id")
		lj := rowInt64(rows[j], "local_id")
		if asc {
			return li < lj
		}
		return li > lj
	})
}

func (s *server) queryLiveMessages(a map[string]any, order string) ([]wcdb.Row, messagePageInfo, error) {
	talker, err := s.resolveLooseChatArg(a)
	if err != nil {
		return nil, messagePageInfo{}, err
	}
	if talker == "" {
		return nil, messagePageInfo{}, fmt.Errorf("talker or chat is required")
	}
	if aggregatorSessions[talker] {
		return nil, messagePageInfo{}, fmt.Errorf("%q 是订阅号合集入口 (UI 聚合 session), 本身无消息表. 真实消息在各 gh_* 公众号下, 按具体 gh_<id> 查", talker)
	}
	tableName := "Msg_" + talkerHash(talker)
	shards, err := s.findMsgDBs(tableName)
	if err != nil {
		return nil, messagePageInfo{}, err
	}
	defer closeMsgDBs(shards)

	sender := ""
	if senderArg := getStr(a, "sender"); senderArg != "" {
		resolved, err := s.resolveLooseSenderArg(a)
		if err != nil {
			return nil, messagePageInfo{}, err
		}
		sender = resolved
	}

	var where []string
	var args []any
	if s := getStr(a, "after"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, messagePageInfo{}, err
		}
		where = append(where, "create_time >= ?")
		args = append(args, ts)
	}
	if s := getStr(a, "before"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, messagePageInfo{}, err
		}
		where = append(where, "create_time < ?")
		args = append(args, ts)
	}
	if baseKind := getInt(a, "base_kind", 0); baseKind != 0 {
		where = append(where, "(local_type & 4294967295) = ?")
		args = append(args, baseKind)
	}
	if kind := firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type")); kind != "" {
		kwc, kargs, err := messageKindWhere(kind, "local_type")
		if err != nil {
			return nil, messagePageInfo{}, err
		}
		where = append(where, kwc)
		args = append(args, kargs...)
	}
	if order == "" {
		order = "sort_seq DESC, local_id DESC"
	}
	limit := getInt(a, "limit", 50)
	offset := getInt(a, "offset", 0)
	page := messagePageInfo{Limit: limit, Offset: offset}
	fetchLimit := limit
	if fetchLimit > 0 {
		fetchLimit++
	}
	kw := getStr(a, "keyword")
	sqlLimit := fetchLimit + offset
	if sqlLimit <= 0 {
		sqlLimit = fetchLimit
	}
	if kw != "" {
		sqlLimit = 5000
	}

	var rows []wcdb.Row
	senderSeen := sender == ""
	for _, shard := range shards {
		shardWhere := append([]string(nil), where...)
		shardArgs := append([]any(nil), args...)
		n2i, _ := loadName2Id(shard.DB)
		if sender != "" {
			var senderID int64
			found := false
			for id, wxid := range n2i {
				if wxid == sender {
					senderID = id
					found = true
					break
				}
			}
			if !found {
				continue
			}
			senderSeen = true
			shardWhere = append(shardWhere, "real_sender_id = ?")
			shardArgs = append(shardArgs, senderID)
		}
		shardWC := ""
		if len(shardWhere) > 0 {
			shardWC = "WHERE " + strings.Join(shardWhere, " AND ")
		}
		shardArgs = append(shardArgs, sqlLimit, 0)
		shardRows, err := shard.DB.Query(fmt.Sprintf(`SELECT local_id, server_id, local_type, sort_seq,
			real_sender_id, create_time, status, message_content, source
			FROM %s %s
			ORDER BY %s
			LIMIT ? OFFSET ?`, quoteIdent(tableName), shardWC, order), shardArgs...)
		if err != nil {
			return nil, messagePageInfo{}, fmt.Errorf("%s: %w", shard.Name, err)
		}
		if n2i != nil {
			shardRows = resolveSenders(shardRows, n2i)
		}
		shardRows = enrichMessages(decodeFields(shardRows, "message_content", "source"))
		for _, r := range shardRows {
			r["talker"] = talker
			r["chat_type"] = agentChatType(talker, wxkind.ClassifyUsername(talker), false)
			baseKind, subtype, kindName := wxkind.Unpack(rowInt64(r, "local_type"))
			r["base_kind"] = baseKind
			r["subtype"] = subtype
			r["kind_name"] = kindName
		}
		rows = append(rows, shardRows...)
	}
	if !senderSeen {
		return []wcdb.Row{}, page, nil
	}
	sortLiveMessageRows(rows, order)
	if kw != "" {
		filtered := rows[:0]
		for _, r := range rows {
			content, _ := r["message_content"].(string)
			summary, _ := r["content_summary"].(string)
			if strings.Contains(content, kw) || strings.Contains(summary, kw) {
				filtered = append(filtered, r)
			}
		}
		if offset < len(filtered) {
			filtered = filtered[offset:]
		} else {
			filtered = nil
		}
		if fetchLimit > 0 && len(filtered) > fetchLimit {
			filtered = filtered[:fetchLimit]
		}
		if limit > 0 && len(filtered) > limit {
			page.HasMore = true
			filtered = filtered[:limit]
		}
		rows = filtered
	} else {
		rows, page.HasMore = sliceRowsWithHasMore(rows, offset, limit)
	}
	s.attachDisplayNames(rows,
		[2]string{"talker", "talker_display_name"},
		[2]string{"sender_wxid", "sender_display_name"})
	if selfWxid := s.selfWxid(); selfWxid != "" {
		for _, r := range rows {
			sw, _ := r["sender_wxid"].(string)
			r["is_from_me"] = (sw == selfWxid)
		}
	}
	for _, r := range rows {
		if sid := rowInt64(r, "server_id"); sid != 0 {
			r["server_id_str"] = strconv.FormatInt(sid, 10)
		}
		delete(r, "real_sender_id")
		delete(r, "sort_seq")
		delete(r, "status")
		delete(r, "source")
		delete(r, "local_type")
	}
	return rows, page, nil
}

func sortLiveMessageRows(rows []wcdb.Row, order string) {
	lower := strings.ToLower(order)
	primary := "sort_seq"
	primaryAsc := false
	if strings.Contains(lower, "create_time") {
		primary = "create_time"
		primaryAsc = strings.Contains(lower, "create_time asc")
	} else if strings.Contains(lower, "sort_seq asc") {
		primaryAsc = true
	}
	localAsc := strings.Contains(lower, "local_id asc")
	sort.SliceStable(rows, func(i, j int) bool {
		li := rowInt64(rows[i], primary)
		lj := rowInt64(rows[j], primary)
		if li != lj {
			if primaryAsc {
				return li < lj
			}
			return li > lj
		}
		ii := rowInt64(rows[i], "local_id")
		ij := rowInt64(rows[j], "local_id")
		if localAsc {
			return ii < ij
		}
		return ii > ij
	})
}

func sliceRows(rows []wcdb.Row, offset, limit int) []wcdb.Row {
	out, _ := sliceRowsWithHasMore(rows, offset, limit)
	return out
}

func sliceRowsWithHasMore(rows []wcdb.Row, offset, limit int) ([]wcdb.Row, bool) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return nil, false
	}
	rows = rows[offset:]
	if limit >= 0 && len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// liteMessages strips raw XML / parsed / source / housekeeping fields when
// mode=lite. Keeps the 8 fields that matter for human-readable summarization
// (typical 100-row response: ~250KB full → ~12KB lite, ~95% reduction).
// mode=full passes through unchanged.
func liteMessages(rows []wcdb.Row, mode string, includeDebugOpt ...bool) []wcdb.Row {
	if mode != "lite" {
		return rows
	}
	includeDebug := false
	if len(includeDebugOpt) > 0 {
		includeDebug = includeDebugOpt[0]
	}
	keep := map[string]bool{
		"talker": true, "talker_display_name": true, "chat_type": true,
		"local_id": true, "server_id": true, "server_id_str": true,
		"create_time": true, "create_time_human": true,
		"sender_wxid": true, "sender_display_name": true, "is_from_me": true,
		"base_kind": true, "kind_name": true, "content_summary": true,
		"id": true, "display": true, "images": true, "videos": true, "files": true,
		"link": true, "music": true, "miniprogram": true, "forward_chat": true, "quote": true,
		"transfer": true, "red_packet": true, "location": true, "card": true,
		"voice": true, "video": true, "sticker": true, "solitaire": true, "announcement": true, "pat": true,
		"warnings": true,
	}
	if includeDebug {
		for _, k := range []string{
			"media_local_paths", "media_local_path_uris",
			"decoded_media_local_paths", "decoded_media_local_path_uris",
			"media_resources", "media_read_hints",
		} {
			keep[k] = true
		}
	}
	for _, r := range rows {
		attachDisplayReadyFields(r)
		for k := range r {
			if !keep[k] {
				delete(r, k)
			}
		}
	}
	return rows
}

func attachDisplayReadyFields(r wcdb.Row) {
	msg := agentMessage(r)
	if id, ok := msg["id"]; ok {
		r["id"] = id
	}
	if images, ok := msg["images"]; ok {
		r["images"] = images
	}
	if videos, ok := msg["videos"]; ok {
		r["videos"] = videos
	}
	if files, ok := msg["files"]; ok {
		r["files"] = files
	}
	if quote, ok := msg["quote"]; ok {
		r["quote"] = quote
	}
	for _, key := range []string{"link", "music", "miniprogram", "forward_chat", "transfer", "red_packet", "location", "card", "voice", "video", "sticker", "solitaire", "announcement", "pat"} {
		if v, ok := msg[key]; ok {
			r[key] = v
		}
	}
	if warnings, ok := msg["warnings"]; ok {
		r["warnings"] = warnings
	}
	if display := agentDisplayObject(msg); len(display) > 0 {
		r["display"] = display
	}
}

func messagesView(a map[string]any) (string, error) {
	view := getStr(a, "view")
	if view == "" || view == "default" {
		return "default", nil
	}
	if view != "agent" {
		return "", fmt.Errorf("invalid view=%q: must be \"default\" or \"agent\"", view)
	}
	return view, nil
}

func agentMessages(rows []wcdb.Row, includeDebugOpt ...bool) []map[string]any {
	includeDebug := false
	if len(includeDebugOpt) > 0 {
		includeDebug = includeDebugOpt[0]
	}
	out := make([]map[string]any, 0, len(rows))
	index := agentSourceMessageIndex(rows)
	for _, r := range rows {
		out = append(out, agentMessageWithIndex(r, index, includeDebug))
	}
	return out
}

func agentMessage(r wcdb.Row, includeDebugOpt ...bool) map[string]any {
	return agentMessageWithIndex(r, nil, includeDebugOpt...)
}

func agentMessageWithIndex(r wcdb.Row, sourceIndex map[string]wcdb.Row, includeDebugOpt ...bool) map[string]any {
	includeDebug := false
	if len(includeDebugOpt) > 0 {
		includeDebug = includeDebugOpt[0]
	}
	out := map[string]any{
		"id":          agentMessageID(r),
		"time":        agentMessageTime(r),
		"create_time": rowInt64(r, "create_time"),
		"time_iso":    agentMessageTimeISO(r),
		"sender":      agentMessageSender(r),
		"sender_wxid": rowString(r, "sender_wxid"),
		"is_from_me":  r["is_from_me"],
		"kind":        rowString(r, "kind_name"),
		"text":        agentMessageText(r),
	}
	if warnings := agentMessageWarnings(r); len(warnings) > 0 {
		out["warnings"] = warnings
	}
	if images, warnings := agentImageRefs(r, false); len(images) > 0 || len(warnings) > 0 {
		if len(images) > 0 {
			out["images"] = images
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
	if videos, warnings := agentVideoRefs(r, false); len(videos) > 0 || len(warnings) > 0 {
		if len(videos) > 0 {
			out["videos"] = videos
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
	if files, warnings := agentFileRefs(r, false); len(files) > 0 || len(warnings) > 0 {
		if len(files) > 0 {
			out["files"] = files
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
	if quote := agentQuote(r, sourceIndex); len(quote) > 0 {
		out["quote"] = quote
	}
	attachAgentStructuredPayloads(out, r, sourceIndex)
	if includeDebug {
		if debug := agentDebugFields(r); len(debug) > 0 {
			out["debug"] = debug
		}
	}
	return compactMap(out)
}

func agentSourceMessageIndex(rows []wcdb.Row) map[string]wcdb.Row {
	index := map[string]wcdb.Row{}
	for _, r := range rows {
		for _, key := range agentSourceMessageKeys(
			rowString(r, "talker"),
			rowInt64(r, "local_id"),
			rowString(r, "server_id_str"),
			rowInt64(r, "server_id"),
		) {
			index[key] = r
		}
	}
	if len(index) == 0 {
		return nil
	}
	return index
}

func agentSourceMessageKeys(talker string, localID int64, serverIDStr string, serverID int64) []string {
	var keys []string
	if talker != "" && localID != 0 {
		keys = append(keys, "talker:"+talker+"\x00local:"+strconv.FormatInt(localID, 10))
	}
	if serverIDStr == "" && serverID != 0 {
		serverIDStr = strconv.FormatInt(serverID, 10)
	}
	if serverIDStr != "" {
		keys = append(keys, "server:"+serverIDStr)
		if talker != "" {
			keys = append(keys, "talker:"+talker+"\x00server:"+serverIDStr)
		}
	}
	return keys
}

func lookupAgentSourceMessage(index map[string]wcdb.Row, talker string, localID int64, serverIDStr string, serverID int64) wcdb.Row {
	if len(index) == 0 {
		return nil
	}
	for _, key := range agentSourceMessageKeys(talker, localID, serverIDStr, serverID) {
		if r := index[key]; r != nil {
			return r
		}
	}
	return nil
}

func agentMessageID(r wcdb.Row) map[string]any {
	serverIDStr := rowString(r, "server_id_str")
	if serverIDStr == "" {
		if sid := rowInt64(r, "server_id"); sid != 0 {
			serverIDStr = strconv.FormatInt(sid, 10)
		}
	}
	return compactMap(map[string]any{
		"talker":        rowString(r, "talker"),
		"local_id":      rowInt64(r, "local_id"),
		"server_id_str": serverIDStr,
	})
}

func agentDebugFields(r wcdb.Row) map[string]any {
	out := compactMap(map[string]any{
		"local_id":         r["local_id"],
		"server_id":        r["server_id"],
		"server_id_str":    r["server_id_str"],
		"media_resources":  r["media_resources"],
		"media_read_hints": r["media_read_hints"],
	})
	return out
}

func agentDisplayObject(msg map[string]any) map[string]any {
	var render []map[string]any
	for _, img := range mapSliceAny(msg["images"]) {
		item := copyAgentMediaRef(img)
		item["type"] = "image"
		render = append(render, compactMap(item))
	}
	for _, file := range mapSliceAny(msg["files"]) {
		item := copyAgentMediaRef(file)
		item["type"] = "file"
		render = append(render, compactMap(item))
	}
	for _, video := range mapSliceAny(msg["videos"]) {
		item := copyAgentMediaRef(video)
		item["type"] = "video"
		render = append(render, compactMap(item))
	}
	if link, ok := msg["link"].(map[string]any); ok {
		item := compactMap(map[string]any{
			"type":   "link",
			"title":  link["title"],
			"url":    link["url"],
			"source": link["source"],
		})
		if len(item) > 1 {
			render = append(render, item)
		}
	}
	if mini, ok := msg["miniprogram"].(map[string]any); ok {
		item := compactMap(map[string]any{
			"type":  "miniprogram",
			"title": mini["title"],
			"app":   mini["app"],
			"url":   mini["url"],
		})
		if len(item) > 1 {
			render = append(render, item)
		}
	}
	out := compactMap(map[string]any{
		"speaker":      msg["sender"],
		"display_text": msg["text"],
	})
	if len(render) > 0 {
		out["render"] = render
	}
	return out
}

func copyAgentMediaRef(in map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"path", "name", "file_size", "file_ext"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	return out
}

func agentMessageTime(r wcdb.Row) string {
	if s := rowString(r, "create_time_human"); s != "" {
		return s
	}
	if ts := rowInt64(r, "create_time"); ts != 0 {
		return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
	}
	return ""
}

func agentMessageTimeISO(r wcdb.Row) string {
	ts := rowInt64(r, "create_time")
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}

func agentMessageSender(r wcdb.Row) string {
	sender := firstNonEmpty(rowString(r, "sender_display_name"), rowString(r, "sender_wxid"))
	if sender != "" {
		return sender
	}
	if rowString(r, "kind_name") == "system" {
		return "系统"
	}
	return ""
}

func agentMessageText(r wcdb.Row) string {
	if rowString(r, "kind_name") == "quote" {
		if p, _ := r["message_content_parsed"].(map[string]any); p != nil {
			if title := strings.TrimSpace(stringMapValue(p, "title")); title != "" {
				return title
			}
		}
	}
	if rowString(r, "kind_name") == "voice" {
		if text := voiceTranscriptTextFromHints(r, false); text != "" {
			return "[语音] " + text
		}
	}
	text := rowString(r, "content_summary")
	if rowString(r, "kind_name") == "system" {
		return agentSystemText(text)
	}
	return text
}

func agentMessageWarnings(r wcdb.Row) []string {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	var warnings []string
	if stringMapValue(p, "parse_error") != "" {
		warnings = appendUniqueStrings(warnings, "message_parse_error")
	}
	if stringMapValue(p, "forward_items_parse_error") != "" {
		warnings = appendUniqueStrings(warnings, "forward_items_parse_error")
	}
	return warnings
}

func agentSystemText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "<sysmsg") {
		return raw
	}
	type revokeMsg struct {
		Content string `xml:"revokemsg>content"`
	}
	var parsed revokeMsg
	if err := xml.Unmarshal([]byte(raw), &parsed); err == nil {
		if content := strings.TrimSpace(parsed.Content); content != "" {
			return content
		}
	}
	return raw
}

func agentQuote(r wcdb.Row, sourceIndex map[string]wcdb.Row) map[string]any {
	if rowString(r, "kind_name") != "quote" {
		return nil
	}
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	refer, _ := p["refermsg"].(map[string]any)
	if refer == nil {
		return nil
	}
	refType := int64(0)
	if n, ok := integerArgValue(refer["type"]); ok {
		refType = n
	}
	refRaw, _ := refer["content_raw"].(string)
	refParsed := mapAny(refer["content_parsed"])
	refSubtype := int32(0)
	if refType == 49 {
		if n, ok := integerArgValue(refParsed["app_subtype"]); ok {
			refSubtype = int32(n)
		}
	}
	refKind := wxkind.Resolve(int32(refType), refSubtype)
	refText := contentSummary(int32(refType), refSubtype, refRaw, refParsed)
	refSender := firstNonEmpty(stringMapValue(refer, "displayname"), stringMapValue(refer, "fromusr"))
	if stringMapValue(refer, "displayname") == "" && stringMapValue(refer, "fromusr") == rowString(r, "sender_wxid") {
		refSender = agentMessageSender(r)
	}
	quote := map[string]any{
		"sender": refSender,
		"kind":   refKind,
		"text":   refText,
	}
	if id := agentQuoteID(refer); len(id) > 0 {
		quote["id"] = id
	}
	if ts, ok := integerArgValue(refer["createtime"]); ok && ts != 0 {
		quote["time"] = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
		quote["create_time"] = ts
		quote["time_iso"] = time.Unix(ts, 0).Format(time.RFC3339)
	}
	if refParsed != nil {
		refHints := refer["media_read_hints"]
		if refHints == nil {
			refHints = r["media_read_hints"]
		}
		refRow := wcdb.Row{
			"talker":                    firstNonEmpty(stringMapValue(refer, "chatusr"), rowString(r, "talker")),
			"server_id_str":             stringMapValue(refer, "svrid"),
			"base_kind":                 refType,
			"subtype":                   refSubtype,
			"kind_name":                 refKind,
			"message_content_parsed":    refParsed,
			"content_summary":           refText,
			"message_content":           refRaw,
			"sender_display_name":       refSender,
			"create_time_human":         quote["time"],
			"media_read_hints":          refHints,
			"decoded_media_local_paths": r["decoded_media_local_paths"],
		}
		if ts, ok := integerArgValue(refer["createtime"]); ok {
			refRow["create_time"] = ts
		}
		attachAgentStructuredPayloads(quote, refRow, sourceIndex)
		attachAgentReferencedMediaRefs(quote, refRow)
	}
	if src := lookupAgentSourceMessage(sourceIndex, stringMapValue(refer, "chatusr"), 0, stringMapValue(refer, "svrid"), 0); src != nil {
		attachAgentVisiblePayloadFromSource(quote, src)
	}
	pruneResolvedMediaWarnings(quote)
	return compactMap(quote)
}

func agentQuoteID(refer map[string]any) map[string]any {
	serverIDStr := stringMapValue(refer, "svrid")
	if serverIDStr == "" {
		if sid, ok := integerArgValue(refer["svrid"]); ok {
			serverIDStr = strconv.FormatInt(sid, 10)
		}
	}
	return compactMap(map[string]any{
		"talker":        stringMapValue(refer, "chatusr"),
		"server_id_str": serverIDStr,
	})
}

func attachAgentReferencedMediaRefs(out map[string]any, r wcdb.Row) {
	if images, warnings := agentImageRefs(r, true); len(images) > 0 || len(warnings) > 0 {
		if len(images) > 0 {
			out["images"] = images
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
	if videos, warnings := agentVideoRefs(r, true); len(videos) > 0 || len(warnings) > 0 {
		if len(videos) > 0 {
			out["videos"] = videos
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
	if files, warnings := agentFileRefs(r, true); len(files) > 0 || len(warnings) > 0 {
		if len(files) > 0 {
			out["files"] = files
		}
		if len(warnings) > 0 {
			out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
		}
	}
}

func attachAgentStructuredPayloads(out map[string]any, r wcdb.Row, sourceIndexOpt ...map[string]wcdb.Row) {
	var sourceIndex map[string]wcdb.Row
	if len(sourceIndexOpt) > 0 {
		sourceIndex = sourceIndexOpt[0]
	}
	switch rowString(r, "kind_name") {
	case "link", "channel_video":
		if link := agentLinkPayload(r); len(link) > 0 {
			out["link"] = link
		}
	case "music":
		if music := agentMusicPayload(r); len(music) > 0 {
			out["music"] = music
		}
	case "miniprogram":
		if mini := agentMiniprogramPayload(r); len(mini) > 0 {
			out["miniprogram"] = mini
		}
	case "forward_chat":
		if forward := agentForwardChatPayload(r, sourceIndex); len(forward) > 0 {
			out["forward_chat"] = forward
		}
	case "transfer":
		if transfer := agentTransferPayload(r); len(transfer) > 0 {
			out["transfer"] = transfer
		}
	case "red_packet":
		if redPacket := agentRedPacketPayload(r); len(redPacket) > 0 {
			out["red_packet"] = redPacket
		}
	case "location":
		if loc := agentLocationPayload(r); len(loc) > 0 {
			out["location"] = loc
		}
	case "card":
		if card := agentCardPayload(r); len(card) > 0 {
			out["card"] = card
		}
	case "voice":
		if voice := agentVoicePayload(r); len(voice) > 0 {
			out["voice"] = voice
		}
	case "video":
		if video := agentVideoPayload(r); len(video) > 0 {
			out["video"] = video
		}
	case "sticker":
		if sticker := agentStickerPayload(r); len(sticker) > 0 {
			out["sticker"] = sticker
		}
	case "solitaire":
		if solitaire := agentSolitairePayload(r); len(solitaire) > 0 {
			out["solitaire"] = solitaire
		}
	case "announcement":
		if announcement := agentSimplePayload(r, "announcement"); len(announcement) > 0 {
			out["announcement"] = announcement
		}
	case "pat":
		if pat := agentSimplePayload(r, "pat"); len(pat) > 0 {
			out["pat"] = pat
		}
	case "app":
		if app := agentAppPayload(r); len(app) > 0 {
			out["app"] = app
		}
	}
}

func attachAgentVisiblePayloadFromSource(out map[string]any, src wcdb.Row) {
	if src == nil {
		return
	}
	srcMsg := agentMessageWithIndex(src, nil)
	for _, key := range []string{
		"images", "videos", "files", "link", "music", "miniprogram", "forward_chat",
		"transfer", "red_packet", "location", "card", "voice", "video", "sticker",
		"solitaire", "announcement", "pat",
	} {
		if key == "forward_chat" && rowString(src, "kind_name") == "forward_chat" && rowString(wcdb.Row(out), "kind") == "forward_chat" {
			continue
		}
		if v, ok := srcMsg[key]; ok {
			if shouldUseSourcePayload(out, key, v) {
				out[key] = v
			}
		}
	}
	if sourceID, ok := srcMsg["id"].(map[string]any); ok && len(sourceID) > 0 {
		out["source_id"] = sourceID
	}
	if warnings := stringSliceAny(srcMsg["warnings"]); len(warnings) > 0 {
		out["warnings"] = appendUniqueStrings(stringSliceAny(out["warnings"]), warnings...)
	}
	pruneResolvedMediaWarnings(out)
}

func shouldUseSourcePayload(out map[string]any, key string, src any) bool {
	current, exists := out[key]
	if !exists {
		return true
	}
	switch key {
	case "images", "videos":
		return len(mapSliceAny(current)) == 0 && len(mapSliceAny(src)) > 0
	case "files":
		return !filesHaveReadablePath(mapSliceAny(current)) && filesHaveReadablePath(mapSliceAny(src))
	default:
		return false
	}
}

func pruneResolvedMediaWarnings(out map[string]any) {
	warnings := stringSliceAny(out["warnings"])
	if len(warnings) == 0 {
		return
	}
	remove := map[string]bool{}
	if len(mapSliceAny(out["images"])) > 0 {
		for _, w := range []string{"image_available_only_as_wechat_cdn", "image_paths_not_direct_readable", "image_decode_needs_image_key", "forward_image_not_resolved"} {
			remove[w] = true
		}
	}
	if len(mapSliceAny(out["videos"])) > 0 {
		for _, w := range []string{"video_paths_not_direct_readable", "forward_video_not_resolved"} {
			remove[w] = true
		}
	}
	if filesHaveReadablePath(mapSliceAny(out["files"])) {
		for _, w := range []string{"file_paths_not_direct_readable", "forward_file_not_resolved"} {
			remove[w] = true
		}
	}
	if len(remove) == 0 {
		return
	}
	var kept []string
	for _, w := range warnings {
		if !remove[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		delete(out, "warnings")
		return
	}
	out["warnings"] = kept
}

func filesHaveReadablePath(files []map[string]any) bool {
	for _, f := range files {
		if rowString(wcdb.Row(f), "path") != "" {
			return true
		}
	}
	return false
}

func agentAppPayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	return compactMap(map[string]any{
		"title":           stringMapValue(p, "title"),
		"description":     stringMapValue(p, "des"),
		"url":             stringMapValue(p, "url"),
		"source":          stringMapValue(p, "source_display_name"),
		"source_username": stringMapValue(p, "source_username"),
		"thumb_url":       stringMapValue(p, "thumb_url"),
	})
}

func agentLinkPayload(r wcdb.Row) map[string]any {
	return agentAppPayload(r)
}

func agentMusicPayload(r wcdb.Row) map[string]any {
	p := agentAppPayload(r)
	if len(p) == 0 {
		return nil
	}
	return compactMap(map[string]any{
		"title":     p["title"],
		"artist":    p["description"],
		"url":       p["url"],
		"source":    p["source"],
		"thumb_url": p["thumb_url"],
	})
}

func agentMiniprogramPayload(r wcdb.Row) map[string]any {
	p := agentAppPayload(r)
	if len(p) == 0 {
		return nil
	}
	if source, ok := p["source"].(string); ok && source != "" {
		p["app"] = source
	}
	return p
}

func agentForwardChatPayload(r wcdb.Row, sourceIndex map[string]wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	items := forwardItemsPayload(p["forward_items"], mapSliceAny(r["media_read_hints"]), sourceIndex)
	return compactMap(map[string]any{
		"title":       stringMapValue(p, "title"),
		"description": stringMapValue(p, "des"),
		"item_count":  forwardItemCount(p["forward_items"]),
		"items":       items,
	})
}

func forwardItemCount(v any) int {
	switch x := v.(type) {
	case []wxparse.ForwardItem:
		return len(x)
	case []any:
		return len(x)
	default:
		return 0
	}
}

func forwardItemsPayload(v any, hints []map[string]any, sourceIndex map[string]wcdb.Row) []map[string]any {
	return forwardItemsPayloadAt(v, nil, hints, sourceIndex)
}

func forwardItemsPayloadAt(v any, prefix []int, hints []map[string]any, sourceIndex map[string]wcdb.Row) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []wxparse.ForwardItem:
		for i, it := range x {
			out = append(out, forwardItemPayload(it, appendForwardPath(prefix, i), hints, sourceIndex))
		}
	case []any:
		for i, raw := range x {
			if m, ok := raw.(map[string]any); ok {
				out = append(out, forwardMapItemPayload(m, appendForwardPath(prefix, i), hints, sourceIndex))
			}
		}
	}
	return out
}

func forwardItemPayload(it wxparse.ForwardItem, path []int, hints []map[string]any, sourceIndex map[string]wcdb.Row) map[string]any {
	kind := forwardDataTypeKind(it.DataType)
	item := compactMap(map[string]any{
		"kind":        kind,
		"sender":      it.SourceName,
		"time":        it.SourceTime,
		"create_time": it.SrcMsgCreateTime,
		"text":        forwardItemText(kind, it.DataDesc),
		"title":       forwardItemTitle(kind, it.DataTitle, it.DataDesc),
		"description": forwardItemDescription(kind, it.DataDesc),
		"source_id":   forwardSourceID("", it.SrcMsgLocalID, it.FromNewMsgID, it.MessageUUID),
		"parse_error": it.ParseError,
	})
	if it.SrcMsgCreateTime != 0 {
		item["time_iso"] = time.Unix(it.SrcMsgCreateTime, 0).Format(time.RFC3339)
	}
	addForwardItemTypedPayload(item, kind, it, path, hints)
	if src := lookupAgentSourceMessage(sourceIndex, "", it.SrcMsgLocalID, it.FromNewMsgID, 0); src != nil {
		attachAgentVisiblePayloadFromSource(item, src)
	}
	if it.ReferMsg != nil {
		if quote := forwardReferMsgPayload(*it.ReferMsg); len(quote) > 0 {
			item["quote"] = quote
		}
	}
	if len(it.NestedItems) > 0 {
		item["item_count"] = len(it.NestedItems)
		item["items"] = forwardItemsPayloadAt(it.NestedItems, path, hints, sourceIndex)
	}
	attachForwardItemMissingMediaWarnings(item, kind)
	return item
}

func forwardMapItemPayload(m map[string]any, path []int, hints []map[string]any, sourceIndex map[string]wcdb.Row) map[string]any {
	datatype := int(rowInt64(wcdb.Row(m), "datatype"))
	kind := forwardDataTypeKind(datatype)
	localID := rowInt64(wcdb.Row(m), "src_msg_localid")
	serverIDStr := stringMapValue(m, "fromnewmsgid")
	item := compactMap(map[string]any{
		"kind":        kind,
		"sender":      stringMapValue(m, "sourcename"),
		"time":        stringMapValue(m, "sourcetime"),
		"create_time": m["src_msg_create_time"],
		"text":        forwardItemText(kind, stringMapValue(m, "datadesc")),
		"title":       forwardItemTitle(kind, stringMapValue(m, "datatitle"), stringMapValue(m, "datadesc")),
		"description": forwardItemDescription(kind, stringMapValue(m, "datadesc")),
		"source_id":   forwardSourceID("", localID, serverIDStr, stringMapValue(m, "messageuuid")),
	})
	if src := lookupAgentSourceMessage(sourceIndex, "", localID, serverIDStr, 0); src != nil {
		attachAgentVisiblePayloadFromSource(item, src)
	}
	if nested := forwardItemsPayloadAt(m["nested_items"], path, hints, sourceIndex); len(nested) > 0 {
		item["item_count"] = len(nested)
		item["items"] = nested
	}
	attachForwardItemMissingMediaWarnings(item, kind)
	return item
}

func forwardSourceID(talker string, localID int64, serverIDStr, messageUUID string) map[string]any {
	return compactMap(map[string]any{
		"talker":        talker,
		"local_id":      localID,
		"server_id_str": serverIDStr,
		"message_uuid":  messageUUID,
	})
}

func attachForwardItemMissingMediaWarnings(item map[string]any, kind string) {
	var warning string
	switch kind {
	case "image":
		if len(mapSliceAny(item["images"])) == 0 {
			warning = "forward_image_not_resolved"
		}
	case "video":
		if len(mapSliceAny(item["videos"])) == 0 {
			warning = "forward_video_not_resolved"
		}
	case "file":
		if !filesHaveReadablePath(mapSliceAny(item["files"])) {
			warning = "forward_file_not_resolved"
		}
	}
	if warning != "" {
		item["warnings"] = appendUniqueStrings(stringSliceAny(item["warnings"]), warning)
	}
	pruneResolvedMediaWarnings(item)
}

func appendForwardPath(prefix []int, next int) []int {
	out := make([]int, 0, len(prefix)+1)
	out = append(out, prefix...)
	out = append(out, next)
	return out
}

func forwardItemText(kind, desc string) string {
	if kind == "text" {
		return desc
	}
	return ""
}

func forwardItemTitle(kind, title, desc string) string {
	if kind == "text" {
		return ""
	}
	return firstNonEmpty(title, desc)
}

func forwardItemDescription(kind, desc string) string {
	if kind == "text" {
		return ""
	}
	return desc
}

func addForwardItemTypedPayload(item map[string]any, kind string, it wxparse.ForwardItem, path []int, hints []map[string]any) {
	switch kind {
	case "image":
		if images := forwardImageRefs(it, path, hints); len(images) > 0 {
			item["images"] = images
		}
	case "video":
		if videos := forwardVideoRefs(it, path, hints); len(videos) > 0 {
			item["videos"] = videos
		}
	case "voice":
		if voice := forwardVoicePayload(it); len(voice) > 0 {
			item["voice"] = voice
		}
	case "file":
		if file := forwardFilePayload(it); len(file) > 0 {
			item["files"] = []map[string]any{file}
		}
	case "link":
		if link := forwardLinkPayload(it); len(link) > 0 {
			item["link"] = link
		}
	case "miniprogram":
		if mini := forwardGenericAppPayload(it); len(mini) > 0 {
			item["miniprogram"] = mini
		}
	case "music":
		if music := forwardGenericAppPayload(it); len(music) > 0 {
			item["music"] = music
		}
	case "location":
		if loc := forwardGenericLocationPayload(it); len(loc) > 0 {
			item["location"] = loc
		}
	case "card":
		if card := forwardGenericCardPayload(it); len(card) > 0 {
			item["card"] = card
		}
	case "red_packet":
		if redPacket := forwardGenericRedPacketPayload(it); len(redPacket) > 0 {
			item["red_packet"] = redPacket
		}
	}
}

func forwardImageRefs(it wxparse.ForwardItem, path []int, hints []map[string]any) []map[string]any {
	if refs := forwardMediaRefsFromHints(path, hints, "image"); len(refs) > 0 {
		return refs
	}
	return nil
}

func forwardVideoRefs(it wxparse.ForwardItem, path []int, hints []map[string]any) []map[string]any {
	if refs := forwardMediaRefsFromHints(path, hints, "video"); len(refs) > 0 {
		return refs
	}
	return nil
}

func forwardVoicePayload(it wxparse.ForwardItem) map[string]any {
	return nil
}

func forwardFilePayload(it wxparse.ForwardItem) map[string]any {
	return compactMap(map[string]any{
		"name":      firstNonEmpty(it.DataTitle, it.DataDesc),
		"file_ext":  it.DataFmt,
		"file_size": it.DataSize,
	})
}

func forwardLinkPayload(it wxparse.ForwardItem) map[string]any {
	if it.Link == nil {
		return nil
	}
	return compactMap(map[string]any{
		"title":           firstNonEmpty(it.Link.Title, it.DataTitle, it.DataDesc),
		"url":             it.Link.URL,
		"source":          it.Link.SourceDisplayName,
		"source_username": it.Link.SourceUsername,
		"thumb_url":       it.Link.ThumbURL,
	})
}

func forwardGenericAppPayload(it wxparse.ForwardItem) map[string]any {
	out := compactMap(map[string]any{
		"title":       firstNonEmpty(it.DataTitle, it.DataDesc),
		"description": it.DataDesc,
	})
	if it.Link != nil {
		if it.Link.SourceDisplayName != "" {
			out["source"] = it.Link.SourceDisplayName
		}
		if it.Link.SourceUsername != "" {
			out["source_username"] = it.Link.SourceUsername
		}
		if it.Link.URL != "" {
			out["url"] = it.Link.URL
		}
		if it.Link.ThumbURL != "" {
			out["thumb_url"] = it.Link.ThumbURL
		}
	}
	return out
}

func forwardGenericLocationPayload(it wxparse.ForwardItem) map[string]any {
	return compactMap(map[string]any{
		"label":       firstNonEmpty(it.DataDesc, it.DataTitle),
		"name":        it.DataTitle,
		"description": it.DataDesc,
	})
}

func forwardGenericCardPayload(it wxparse.ForwardItem) map[string]any {
	return compactMap(map[string]any{
		"display_name": firstNonEmpty(it.DataTitle, it.DataDesc),
		"description":  it.DataDesc,
	})
}

func forwardGenericRedPacketPayload(it wxparse.ForwardItem) map[string]any {
	return compactMap(map[string]any{
		"title":       firstNonEmpty(it.DataTitle, it.DataDesc),
		"description": it.DataDesc,
	})
}

func forwardMediaRefsFromHints(path []int, hints []map[string]any, family string) []map[string]any {
	key := forwardPathString(path)
	var out []map[string]any
	for _, h := range hints {
		if rowString(wcdb.Row(h), "source") != "message_forward_item" || rowString(wcdb.Row(h), "forward_path") != key || !agentHintMatchesFamily(h, family) {
			continue
		}
		row := wcdb.Row{"media_read_hints": []map[string]any{h}}
		paths := appendUniqueStrings(stringSliceAny(h["direct_readable_local_paths"]), stringSliceAny(h["decoded_local_paths"])...)
		for _, p := range stringSliceAny(h["local_paths"]) {
			if directReadableMediaPath(family, p) {
				paths = appendUniqueStrings(paths, p)
			}
		}
		out = append(out, agentMediaRefsFromPaths(row, paths, false, family)...)
	}
	return out
}

func forwardReferMsgPayload(ref wxparse.ForwardReferMsg) map[string]any {
	refType := int32(ref.Type)
	parsed := mapAny(parseMessageContent(refType, 0, ref.Content, messageContentParseDepth))
	refSubtype := int32(0)
	if refType == 49 {
		if n, ok := integerArgValue(parsed["app_subtype"]); ok {
			refSubtype = int32(n)
		}
	}
	kind := wxkind.Resolve(refType, refSubtype)
	quote := compactMap(map[string]any{
		"id": compactMap(map[string]any{
			"server_id_str": ref.SvrID,
		}),
		"sender": ref.DisplayName,
		"kind":   kind,
		"text":   firstNonEmpty(ref.ReferDesc, contentSummary(refType, refSubtype, ref.Content, parsed)),
	})
	if parsed != nil {
		refRow := wcdb.Row{
			"kind_name":              kind,
			"base_kind":              int64(refType),
			"subtype":                int64(refSubtype),
			"message_content":        ref.Content,
			"message_content_parsed": parsed,
			"content_summary":        contentSummary(refType, refSubtype, ref.Content, parsed),
			"sender_display_name":    ref.DisplayName,
		}
		attachAgentStructuredPayloads(quote, refRow)
	}
	return quote
}

func forwardPathString(path []int) string {
	parts := make([]string, 0, len(path))
	for _, n := range path {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}

func forwardDataTypeKind(datatype int) string {
	switch datatype {
	case 1:
		return "text"
	case 2:
		return "image"
	case 3:
		return "voice"
	case 4:
		return "video"
	case 5:
		return "link"
	case 6:
		return "location"
	case 7:
		return "music"
	case 8:
		return "file"
	case 16:
		return "card"
	case 17:
		return "forward_chat"
	case 18:
		return "miniprogram"
	case 2001:
		return "red_packet"
	default:
		if datatype == 0 {
			return ""
		}
		return fmt.Sprintf("datatype_%d", datatype)
	}
}

func agentTransferPayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	wcpay := mapAny(p["wcpayinfo"])
	amount := stringMapValue(wcpay, "feedesc")
	description := firstNonEmpty(stringMapValue(p, "des"), stringMapValue(wcpay, "description"))
	memo := stringMapValue(wcpay, "pay_memo")
	if amount == "" && description == "" && memo == "" {
		if a, d, m, err := wxparse.TransferInfo(rowString(r, "message_content")); err == nil {
			amount, description, memo = a, d, m
		}
	}
	return compactMap(map[string]any{
		"amount":       amount,
		"description":  description,
		"memo":         memo,
		"pay_sub_type": wcpay["paysubtype"],
	})
}

func agentRedPacketPayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	wcpay := mapAny(p["wcpayinfo"])
	wishing := firstNonEmpty(stringMapValue(wcpay, "sendertitle"), stringMapValue(p, "title"))
	sceneText := stringMapValue(wcpay, "scenetext")
	if wishing == "" && sceneText == "" {
		if w, s, err := wxparse.RedPacketInfo(rowString(r, "message_content")); err == nil {
			wishing, sceneText = w, s
		}
	}
	return compactMap(map[string]any{
		"title":          wishing,
		"scene":          sceneText,
		"receiver_title": stringMapValue(wcpay, "receivertitle"),
	})
}

func agentLocationPayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	return compactMap(map[string]any{
		"label":     stringMapValue(p, "label"),
		"name":      stringMapValue(p, "poiname"),
		"latitude":  p["latitude"],
		"longitude": p["longitude"],
		"scale":     p["scale"],
	})
}

func agentCardPayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return agentSimplePayload(r, "card")
	}
	return compactMap(map[string]any{
		"username":          stringMapValue(p, "username"),
		"nickname":          stringMapValue(p, "nickname"),
		"alias":             stringMapValue(p, "alias"),
		"display_name":      firstNonEmpty(stringMapValue(p, "nickname"), stringMapValue(p, "username")),
		"small_avatar_url":  stringMapValue(p, "small_head_img_url"),
		"avatar_url":        stringMapValue(p, "big_head_img_url"),
		"province":          stringMapValue(p, "province"),
		"city":              stringMapValue(p, "city"),
		"signature":         stringMapValue(p, "signature"),
		"gender":            cardGenderName(p["sex"]),
		"sex_code":          p["sex"],
		"source_scene_code": p["scene"],
	})
}

func cardGenderName(v any) string {
	n, ok := integerArgValue(v)
	if !ok {
		return ""
	}
	switch n {
	case 1:
		return "male"
	case 2:
		return "female"
	default:
		return "unknown"
	}
}

func agentStickerPayload(r wcdb.Row) map[string]any {
	return nil
}

func agentSolitairePayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	text := firstNonEmpty(stringMapValue(p, "title"), rowString(r, "content_summary"))
	entries := solitaireEntries(text)
	return compactMap(map[string]any{
		"text":        text,
		"entries":     entries,
		"entry_count": len(entries),
	})
}

func solitaireEntries(text string) []string {
	var entries []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ".")
		if idx <= 0 {
			idx = strings.Index(line, "、")
		}
		if idx <= 0 {
			continue
		}
		prefix := strings.TrimSpace(line[:idx])
		if _, err := strconv.Atoi(prefix); err != nil {
			continue
		}
		if entry := strings.TrimSpace(line[idx+1:]); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func agentSimplePayload(r wcdb.Row, kind string) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	out := compactMap(map[string]any{
		"title":       stringMapValue(p, "title"),
		"description": stringMapValue(p, "des"),
		"text":        rowString(r, "content_summary"),
	})
	if len(out) == 0 && kind != "" && rowString(r, "content_summary") != "" {
		out["text"] = rowString(r, "content_summary")
	}
	return out
}

func agentImageRefs(r wcdb.Row, referenced bool) ([]map[string]any, []string) {
	paths, warnings := agentImagePaths(r, referenced)
	return agentMediaRefsFromPaths(r, paths, referenced, "image"), warnings
}

func agentVideoRefs(r wcdb.Row, referenced bool) ([]map[string]any, []string) {
	if !referenced {
		switch rowString(r, "kind_name") {
		case "video", "channel_video":
		default:
			return nil, nil
		}
	}
	paths, warnings := agentVideoPaths(r, referenced)
	return agentMediaRefsFromPaths(r, paths, referenced, "video"), warnings
}

func agentFileRefs(r wcdb.Row, referenced bool) ([]map[string]any, []string) {
	paths, warnings := agentFilePaths(r, referenced)
	refs := agentMediaRefsFromPaths(r, paths, referenced, "file")
	if len(refs) == 0 && rowString(r, "kind_name") == "file" {
		if meta := agentFileMetadata(r); len(meta) > 0 {
			refs = append(refs, meta)
		}
	}
	return dedupeAgentFileRefs(refs), warnings
}

func agentFileMetadata(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	appAttach := mapAny(p["app_attach"])
	return compactMap(map[string]any{
		"name":      firstNonEmpty(stringMapValue(p, "title"), stringMapValue(appAttach, "file_name")),
		"file_size": appAttach["total_len"],
		"file_ext":  stringMapValue(appAttach, "file_ext"),
		"readable":  false,
	})
}

func agentVoicePayload(r wcdb.Row) map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	out := compactMap(map[string]any{
		"duration_ms": p["duration_ms"],
	})
	transcript, warnings := agentVoiceTranscript(r, false)
	if len(transcript) == 0 && len(warnings) == 0 {
		transcript, warnings = agentVoiceTranscript(r, true)
	}
	if len(transcript) > 0 {
		out["transcript"] = transcript
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out
}

func agentVoiceRefs(r wcdb.Row, referenced bool) ([]map[string]any, []string) {
	paths, warnings := agentMediaPathsByFamily(r, referenced, "voice", "voice_paths_not_direct_readable")
	return agentMediaRefsFromPaths(r, paths, referenced, "voice"), warnings
}

func agentVoiceTranscript(r wcdb.Row, referenced bool) (map[string]any, []string) {
	for _, h := range mapSliceAny(r["media_read_hints"]) {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintMatchesFamily(h, "voice") {
			continue
		}
		transcript := agentVoiceTranscriptPayload(mapAny(h["transcript"]))
		if len(transcript) == 0 {
			continue
		}
		var warnings []string
		switch stringMapValue(transcript, "status") {
		case "ok":
		case "no_speech":
			warnings = appendUniqueStrings(warnings, "voice_transcription_no_speech")
		case "failed":
			warnings = appendUniqueStrings(warnings, "voice_transcription_failed")
		default:
			warnings = appendUniqueStrings(warnings, "voice_transcription_unavailable")
		}
		return transcript, warnings
	}
	return nil, nil
}

func agentVoiceTranscriptPayload(t map[string]any) map[string]any {
	if len(t) == 0 {
		return nil
	}
	return compactMap(map[string]any{
		"status":   stringMapValue(t, "status"),
		"text":     stringMapValue(t, "text"),
		"engine":   stringMapValue(t, "engine"),
		"model":    stringMapValue(t, "model"),
		"language": stringMapValue(t, "language"),
	})
}

func voiceTranscriptTextFromHints(r wcdb.Row, referenced bool) string {
	transcript, _ := agentVoiceTranscript(r, referenced)
	if stringMapValue(transcript, "status") != "ok" {
		return ""
	}
	return stringMapValue(transcript, "text")
}

func agentVideoPayload(r wcdb.Row) map[string]any {
	out := agentSimplePayload(r, "video")
	if covers, warnings := agentVideoCoverRefs(r, false); len(covers) > 0 || len(warnings) > 0 {
		if len(covers) > 0 {
			out["cover_images"] = covers
		}
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
	}
	return out
}

func agentVideoCoverRefs(r wcdb.Row, referenced bool) ([]map[string]any, []string) {
	paths, warnings := agentMediaPathsByFamily(r, referenced, "cover", "video_cover_paths_not_direct_readable")
	return agentMediaRefsFromPaths(r, paths, referenced, "cover"), warnings
}

func agentFilePaths(r wcdb.Row, referenced bool) ([]string, []string) {
	hints := mapSliceAny(r["media_read_hints"])
	direct, local := agentFilePathsFromHints(hints, referenced)
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{"file_paths_not_direct_readable"}
	}
	if referenced {
		return nil, nil
	}
	if rowString(r, "kind_name") != "file" {
		return nil, nil
	}
	for _, p := range stringSliceAny(r["media_local_paths"]) {
		if directReadableMediaPath("file", p) {
			direct = appendUniqueStrings(direct, p)
		} else {
			local = appendUniqueStrings(local, p)
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{"file_paths_not_direct_readable"}
	}
	return nil, nil
}

func agentFilePathsFromHints(hints []map[string]any, referenced bool) ([]string, []string) {
	var direct []string
	var local []string
	for _, h := range hints {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintMatchesFamily(h, "file") {
			continue
		}
		for _, p := range stringSliceAny(h["direct_readable_local_paths"]) {
			if directReadableMediaPath("file", p) {
				direct = appendUniqueStrings(direct, p)
			} else {
				local = appendUniqueStrings(local, p)
			}
		}
		for _, p := range stringSliceAny(h["local_paths"]) {
			if directReadableMediaPath("file", p) {
				direct = appendUniqueStrings(direct, p)
			} else {
				local = appendUniqueStrings(local, p)
			}
		}
	}
	return direct, local
}

func agentMediaPathsByFamily(r wcdb.Row, referenced bool, family, unreadableWarning string) ([]string, []string) {
	hints := mapSliceAny(r["media_read_hints"])
	var direct []string
	var local []string
	for _, h := range hints {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintMatchesFamily(h, family) {
			continue
		}
		for _, p := range stringSliceAny(h["direct_readable_local_paths"]) {
			if directReadableMediaPath(family, p) {
				direct = appendUniqueStrings(direct, p)
			} else {
				local = appendUniqueStrings(local, p)
			}
		}
		for _, p := range stringSliceAny(h["local_paths"]) {
			if directReadableMediaPath(family, p) {
				direct = appendUniqueStrings(direct, p)
			} else {
				local = appendUniqueStrings(local, p)
			}
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{unreadableWarning}
	}
	if referenced {
		return nil, nil
	}
	for _, p := range stringSliceAny(r["media_local_paths"]) {
		if directReadableMediaPath(family, p) {
			direct = appendUniqueStrings(direct, p)
		} else {
			local = appendUniqueStrings(local, p)
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{unreadableWarning}
	}
	return nil, nil
}

func agentMediaRefsFromPaths(r wcdb.Row, paths []string, referenced bool, family string) []map[string]any {
	out := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		detail := agentMediaPathDetail(r, p, referenced, family)
		ref := map[string]any{
			"path": p,
		}
		if family == "file" {
			ref["name"] = agentFileName(r, p)
			for _, k := range []string{"file_size", "file_ext"} {
				if v, ok := detail[k]; ok {
					ref[k] = v
				}
			}
		}
		out = append(out, compactMap(ref))
	}
	return out
}

func dedupeAgentFileRefs(refs []map[string]any) []map[string]any {
	if len(refs) <= 1 {
		return refs
	}
	var out []map[string]any
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref["name"] != nil || ref["file_size"] != nil {
			key := fmt.Sprintf("%v\x00%v", ref["name"], ref["file_size"])
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, ref)
	}
	return out
}

func agentMediaPathDetail(r wcdb.Row, path string, referenced bool, family string) map[string]any {
	for _, h := range mapSliceAny(r["media_read_hints"]) {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintMatchesFamily(h, family) {
			continue
		}
		for _, d := range mapSliceAny(h["local_path_details"]) {
			if rowString(wcdb.Row(d), "path") == path || rowString(wcdb.Row(d), "uri") == localFileURI(path) {
				return d
			}
		}
	}
	return nil
}

func agentFileName(r wcdb.Row, path string) string {
	for _, res := range mapSliceAny(r["media_resources"]) {
		if rowString(wcdb.Row(res), "resource_family") != "file" {
			continue
		}
		if name := rowString(wcdb.Row(res), "file_name"); name != "" {
			return name
		}
	}
	return filepath.Base(path)
}

func agentImagePaths(r wcdb.Row, referenced bool) ([]string, []string) {
	if !referenced && rowString(r, "kind_name") != "image" {
		return nil, nil
	}
	hints := mapSliceAny(r["media_read_hints"])
	direct, decoded, local, warnings := agentImagePathsFromHints(hints, referenced)
	if len(direct) > 0 {
		return direct, nil
	}
	if len(decoded) > 0 {
		return decoded, nil
	}
	if len(local) > 0 {
		warnings = appendUniqueStrings(warnings, "image_paths_not_direct_readable")
		return nil, warnings
	}
	if len(warnings) > 0 {
		return nil, warnings
	}
	if referenced {
		return nil, nil
	}
	direct = appendUniqueStrings(direct, stringSliceAny(r["decoded_media_local_paths"])...)
	for _, p := range stringSliceAny(r["media_local_paths"]) {
		if directReadableMediaPath("image", p) {
			direct = appendUniqueStrings(direct, p)
		} else {
			local = appendUniqueStrings(local, p)
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{"image_paths_not_direct_readable"}
	}
	return nil, nil
}

func agentVideoPaths(r wcdb.Row, referenced bool) ([]string, []string) {
	hints := mapSliceAny(r["media_read_hints"])
	var direct []string
	var local []string
	for _, h := range hints {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintMatchesFamily(h, "video") {
			continue
		}
		for _, p := range stringSliceAny(h["direct_readable_local_paths"]) {
			if directVideoFilePath(p) {
				direct = appendUniqueStrings(direct, p)
			}
		}
		for _, p := range stringSliceAny(h["local_paths"]) {
			if directVideoFilePath(p) {
				direct = appendUniqueStrings(direct, p)
			} else if strings.ToLower(filepath.Ext(p)) == ".dat" {
				local = appendUniqueStrings(local, p)
			}
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{"video_paths_not_direct_readable"}
	}
	if referenced {
		return nil, nil
	}
	for _, p := range stringSliceAny(r["media_local_paths"]) {
		if directVideoFilePath(p) {
			direct = appendUniqueStrings(direct, p)
		} else if strings.ToLower(filepath.Ext(p)) == ".dat" {
			local = appendUniqueStrings(local, p)
		}
	}
	if len(direct) > 0 {
		return direct, nil
	}
	if len(local) > 0 {
		return nil, []string{"video_paths_not_direct_readable"}
	}
	return nil, nil
}

func agentImagePathsFromHints(hints []map[string]any, referenced bool) ([]string, []string, []string, []string) {
	var direct []string
	var decoded []string
	var local []string
	var warnings []string
	var sawImage bool
	var sawCDN bool
	var sawNeedsKey bool
	for _, h := range hints {
		if !agentHintMatchesReferenced(h, referenced) || !agentHintIsImage(h) {
			continue
		}
		sawImage = true
		if rowString(wcdb.Row(h), "address_type") == "wechat_cdn" {
			sawCDN = true
		}
		direct = appendUniqueStrings(direct, stringSliceAny(h["direct_readable_local_paths"])...)
		decoded = appendUniqueStrings(decoded, stringSliceAny(h["decoded_local_paths"])...)
		for _, p := range stringSliceAny(h["local_paths"]) {
			if directReadableMediaPath("image", p) {
				direct = appendUniqueStrings(direct, p)
			} else {
				local = appendUniqueStrings(local, p)
			}
		}
		for _, d := range mapSliceAny(h["local_path_details"]) {
			if rowString(wcdb.Row(d), "decode_status") == "needs_image_key" {
				sawNeedsKey = true
			}
		}
	}
	if len(direct) == 0 && len(decoded) == 0 {
		if sawNeedsKey {
			warnings = appendUniqueStrings(warnings, "image_decode_needs_image_key")
		}
		if sawImage && sawCDN && len(local) == 0 {
			warnings = appendUniqueStrings(warnings, "image_available_only_as_wechat_cdn")
		}
	}
	return direct, decoded, local, warnings
}

func agentHintMatchesReferenced(h map[string]any, referenced bool) bool {
	row := wcdb.Row(h)
	isReferenced := rowString(row, "message_role") == "referenced_message" || rowString(row, "source") == "message_refermsg"
	return isReferenced == referenced
}

func agentHintIsImage(h map[string]any) bool {
	return agentHintMatchesFamily(h, "image") || agentHintMatchesFamily(h, "cover")
}

func agentHintMatchesFamily(h map[string]any, want string) bool {
	family := rowString(wcdb.Row(h), "resource_family")
	if family == want {
		return true
	}
	if want == "image" && family == "cover" {
		return true
	}
	for _, got := range stringSliceAny(h["resource_families"]) {
		if got == want || (want == "image" && got == "cover") {
			return true
		}
	}
	return false
}

func (s *server) enrichMessageMediaResources(rows []wcdb.Row) error {
	if len(rows) == 0 {
		return nil
	}
	byTalker := map[string][]int64{}
	for _, r := range rows {
		if !messageMayHaveMedia(rowInt64(r, "base_kind"), rowString(r, "kind_name")) {
			continue
		}
		talker := rowString(r, "talker")
		localID := rowInt64(r, "local_id")
		if talker == "" || localID == 0 {
			continue
		}
		byTalker[talker] = append(byTalker[talker], localID)
	}
	if len(byTalker) == 0 {
		s.attachMediaReadHintsToMessages(rows)
		return nil
	}

	db, err := s.openDB("message", "message_resource.db")
	if err != nil {
		return err
	}
	defer db.Close()

	var resourceRows []wcdb.Row
	for talker, ids := range byTalker {
		ids = uniqueInt64s(ids)
		for start := 0; start < len(ids); start += 500 {
			end := start + 500
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[start:end]
			ph := make([]string, len(chunk))
			args := make([]any, 0, len(chunk)+1)
			args = append(args, talker)
			for i, id := range chunk {
				ph[i] = "?"
				args = append(args, id)
			}
			q := fmt.Sprintf(`WITH filtered AS (
				SELECT MAX(i.message_id) AS message_id, c.user_name AS talker,
					i.message_local_type, i.message_create_time, i.message_local_id, i.message_svr_id,
					i.message_origin_source, i.packed_info AS message_packed_info
				FROM MessageResourceInfo i
				LEFT JOIN ChatName2Id c ON c.rowid = i.chat_id
				WHERE c.user_name = ? AND i.message_local_id IN (%s)
				GROUP BY c.user_name, i.message_local_type, i.message_create_time,
					i.message_local_id, i.message_svr_id, i.message_origin_source, i.packed_info
			)
			SELECT f.talker, f.message_local_type, f.message_create_time, f.message_local_id,
				f.message_svr_id, f.message_origin_source, f.message_packed_info,
				d.resource_id, d.type AS resource_type_raw, d.size AS resource_size,
				d.create_time AS resource_create_time, d.access_time AS resource_access_time,
				d.status AS resource_status, d.data_index AS resource_data_index,
				d.packed_info AS resource_packed_info
			FROM filtered f
			JOIN MessageResourceDetail d ON d.message_id = f.message_id
			ORDER BY f.message_create_time DESC, f.message_local_id DESC, d.resource_id ASC`, strings.Join(ph, ","))
			rows, err := db.Query(q, args...)
			if err != nil {
				return err
			}
			resourceRows = append(resourceRows, rows...)
		}
	}
	s.attachMediaResourceRowsToMessages(rows, resourceRows)
	s.attachMediaReadHintsToMessages(rows)
	return nil
}

func messageMayHaveMedia(baseKind int64, kindName string) bool {
	switch baseKind {
	case 3, 34, 43, 47:
		return true
	case 49:
		return true
	}
	switch strings.TrimSpace(strings.ToLower(kindName)) {
	case "image", "voice", "video", "file", "sticker", "forward_chat", "miniprogram", "channel_video":
		return true
	default:
		return false
	}
}

func (s *server) attachMediaResourceRowsToMessages(messages []wcdb.Row, resourceRows []wcdb.Row) {
	if len(messages) == 0 || len(resourceRows) == 0 {
		return
	}
	type mediaBundle struct {
		resources     []map[string]any
		hintResources []map[string]any
		paths         []string
		pathURIs      []string
		decodedPaths  []string
		decodedURIs   []string
	}
	messageByKey := map[string]wcdb.Row{}
	for _, r := range messages {
		if key := mediaMessageKey(rowString(r, "talker"), rowInt64(r, "local_id"), rowInt64(r, "server_id"), rowInt64(r, "create_time")); key != "" {
			messageByKey[key] = r
		}
	}
	byMessage := map[string]*mediaBundle{}
	for _, r := range resourceRows {
		key := mediaMessageKey(rowString(r, "talker"), rowInt64(r, "message_local_id"), rowInt64(r, "message_svr_id"), rowInt64(r, "message_create_time"))
		if key == "" {
			continue
		}
		bundle := byMessage[key]
		if bundle == nil {
			bundle = &mediaBundle{}
			byMessage[key] = bundle
		}
		fullRes := s.mediaResourceFromRow(r, true)
		s.attachReadableImageMatchesFromMessage(fullRes, messageByKey[key])
		bundle.hintResources = append(bundle.hintResources, fullRes)
		bundle.resources = append(bundle.resources, compactMessageMediaResource(fullRes))
		if paths, ok := fullRes["local_paths"].([]string); ok {
			bundle.paths = appendUniqueStrings(bundle.paths, paths...)
		}
		if uris, ok := fullRes["local_path_uris"].([]string); ok {
			bundle.pathURIs = appendUniqueStrings(bundle.pathURIs, uris...)
		}
		if paths, ok := fullRes["decoded_local_paths"].([]string); ok {
			bundle.decodedPaths = appendUniqueStrings(bundle.decodedPaths, paths...)
		}
		if uris, ok := fullRes["decoded_local_path_uris"].([]string); ok {
			bundle.decodedURIs = appendUniqueStrings(bundle.decodedURIs, uris...)
		}
	}
	for _, r := range messages {
		key := mediaMessageKey(rowString(r, "talker"), rowInt64(r, "local_id"), rowInt64(r, "server_id"), rowInt64(r, "create_time"))
		if bundle := byMessage[key]; bundle != nil {
			if len(bundle.paths) > 0 {
				r["media_local_paths"] = bundle.paths
			}
			if len(bundle.pathURIs) > 0 {
				r["media_local_path_uris"] = bundle.pathURIs
			}
			if len(bundle.resources) > 0 {
				r["media_resources"] = bundle.resources
			}
			if len(bundle.decodedPaths) > 0 {
				r["decoded_media_local_paths"] = bundle.decodedPaths
			}
			if len(bundle.decodedURIs) > 0 {
				r["decoded_media_local_path_uris"] = bundle.decodedURIs
			}
			if len(bundle.hintResources) > 0 {
				r["_media_read_hint_resources"] = bundle.hintResources
			}
		}
	}
}

func (s *server) attachReadableImageMatchesFromMessage(res map[string]any, msg wcdb.Row) {
	p, _ := msg["message_content_parsed"].(map[string]any)
	contentMD5 := strings.ToLower(strings.TrimSpace(stringMapValue(p, "md5")))
	s.attachReadableImageMatchesByContentMD5(res, rowInt64(msg, "create_time"), contentMD5)
}

func (s *server) attachReadableImageMatchesToMediaResourceItems(items []map[string]any) {
	byTalker := map[string][]int64{}
	for _, item := range items {
		if !itemHasImageResources(item) {
			continue
		}
		talker, _ := item["talker"].(string)
		localID, _ := integerArgValue(item["local_id"])
		if talker == "" || localID == 0 {
			continue
		}
		byTalker[talker] = append(byTalker[talker], localID)
	}
	contentMD5s := s.messageImageContentMD5s(byTalker)
	if len(contentMD5s) == 0 {
		return
	}
	for _, item := range items {
		talker, _ := item["talker"].(string)
		localID, _ := integerArgValue(item["local_id"])
		createTime, _ := integerArgValue(item["create_time"])
		serverID, _ := integerArgValue(item["server_id"])
		key := mediaMessageKey(talker, localID, serverID, createTime)
		contentMD5 := contentMD5s[key]
		if contentMD5 == "" {
			continue
		}
		resources, _ := item["resources"].([]map[string]any)
		for _, res := range resources {
			s.attachReadableImageMatchesByContentMD5(res, createTime, contentMD5)
		}
	}
}

func itemHasImageResources(item map[string]any) bool {
	resources, _ := item["resources"].([]map[string]any)
	for _, res := range resources {
		family, _ := res["resource_family"].(string)
		if family == "image" || family == "cover" {
			return true
		}
	}
	return false
}

func (s *server) messageImageContentMD5s(byTalker map[string][]int64) map[string]string {
	out := map[string]string{}
	for talker, ids := range byTalker {
		ids = uniqueInt64s(ids)
		if talker == "" || len(ids) == 0 {
			continue
		}
		tableName := "Msg_" + talkerHash(talker)
		shards, err := s.findMsgDBs(tableName)
		if err != nil {
			continue
		}
		func() {
			defer closeMsgDBs(shards)
			for _, shard := range shards {
				for start := 0; start < len(ids); start += 500 {
					end := start + 500
					if end > len(ids) {
						end = len(ids)
					}
					chunk := ids[start:end]
					ph := make([]string, len(chunk))
					args := make([]any, 0, len(chunk))
					for i, id := range chunk {
						ph[i] = "?"
						args = append(args, id)
					}
					rows, err := shard.DB.Query(fmt.Sprintf(`SELECT local_id, server_id, create_time, local_type, message_content
						FROM %s WHERE local_id IN (%s)`, quoteIdent(tableName), strings.Join(ph, ",")), args...)
					if err != nil {
						continue
					}
					rows = decodeFields(rows, "message_content")
					for _, r := range rows {
						baseKind, subtype, _ := wxkind.Unpack(rowInt64(r, "local_type"))
						if baseKind != 3 {
							continue
						}
						content := rowString(r, "message_content")
						if content == "" {
							continue
						}
						parsed, _ := parseMessageContent(baseKind, subtype, content, 1).(map[string]any)
						contentMD5 := strings.ToLower(strings.TrimSpace(stringMapValue(parsed, "md5")))
						if md5LikeRe.MatchString(contentMD5) {
							out[mediaMessageKey(talker, rowInt64(r, "local_id"), rowInt64(r, "server_id"), rowInt64(r, "create_time"))] = contentMD5
						}
					}
				}
			}
		}()
	}
	return out
}

func (s *server) attachReadableImageMatchesByContentMD5(res map[string]any, createTime int64, contentMD5 string) {
	family, _ := res["resource_family"].(string)
	if family != "image" && family != "cover" {
		return
	}
	contentMD5 = strings.ToLower(strings.TrimSpace(contentMD5))
	if !md5LikeRe.MatchString(contentMD5) {
		return
	}
	paths := s.readableImageMatchesByMD5(createTime, contentMD5)
	if len(paths) == 0 {
		return
	}
	if md5Value, _ := res["md5"].(string); !strings.EqualFold(md5Value, contentMD5) {
		res["content_md5"] = contentMD5
	}
	allPaths := appendUniqueStrings(paths, stringSliceAny(res["local_paths"])...)
	res["local_paths"] = allPaths
	s.attachLocalPathAgentFields(res, family, allPaths)
}

func compactMessageMediaResource(res map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{
		"resource_id", "resource_family", "resource_type_raw", "variant_code",
		"size", "status", "md5", "content_md5", "file_name", "direct_readable",
	} {
		if v, ok := res[k]; ok {
			out[k] = v
		}
	}
	paths := stringSliceAny(res["local_paths"])
	if len(paths) > 0 {
		out["local_path_count"] = len(paths)
	}
	decodedPaths := stringSliceAny(res["decoded_local_paths"])
	if len(decodedPaths) > 0 {
		out["decoded_local_path_count"] = len(decodedPaths)
	}
	if formats := storageFormatsFromDetails(mapSliceAny(res["local_path_details"])); len(formats) > 0 {
		out["storage_formats"] = formats
	}
	if formats := stringSliceAny(res["decoded_storage_formats"]); len(formats) > 0 {
		out["decoded_storage_formats"] = formats
	}
	if mimes := stringSliceAny(res["decoded_mime_types"]); len(mimes) > 0 {
		out["decoded_mime_types"] = mimes
	}
	return out
}

func storageFormatsFromDetails(details []map[string]any) []string {
	var out []string
	for _, d := range details {
		if f, ok := d["storage_format"].(string); ok && f != "" {
			out = appendUniqueStrings(out, f)
		}
	}
	return out
}

func mediaRowKey(talker string, localID int64) string {
	if talker == "" || localID == 0 {
		return ""
	}
	return talker + "\x00" + strconv.FormatInt(localID, 10)
}

func mediaMessageKey(talker string, localID, serverID, createTime int64) string {
	base := mediaRowKey(talker, localID)
	if base == "" {
		return ""
	}
	if serverID != 0 {
		return base + "\x00svr=" + strconv.FormatInt(serverID, 10)
	}
	if createTime != 0 {
		return base + "\x00time=" + strconv.FormatInt(createTime, 10)
	}
	return base
}

func (s *server) toolMediaResources(a map[string]any) (any, error) {
	db, err := s.openDB("message", "message_resource.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var where []string
	var args []any
	if talker, err := s.resolveLooseChatArg(a); err != nil {
		return nil, err
	} else if talker != "" {
		where = append(where, "c.user_name = ?")
		args = append(args, talker)
	}
	if sender, err := s.resolveLooseSenderArg(a); err != nil {
		return nil, err
	} else if sender != "" {
		where = append(where, "sn.user_name = ?")
		args = append(args, sender)
	}
	if id, ok, err := int64Arg(a, "local_id"); err != nil {
		return nil, err
	} else if ok {
		where = append(where, "i.message_local_id = ?")
		args = append(args, id)
	}
	if id, ok, err := mediaServerIDArg(a); err != nil {
		return nil, err
	} else if ok {
		where = append(where, "i.message_svr_id = ?")
		args = append(args, id)
	}
	if s := getStr(a, "after"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, err
		}
		where = append(where, "i.message_create_time >= ?")
		args = append(args, ts)
	}
	if s := getStr(a, "before"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, err
		}
		where = append(where, "i.message_create_time < ?")
		args = append(args, ts)
	}
	if baseKind := getInt(a, "base_kind", 0); baseKind != 0 {
		where = append(where, "(i.message_local_type & 4294967295) = ?")
		args = append(args, baseKind)
	}
	if kind := firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type")); kind != "" {
		kwc, kargs, err := mediaKindWhere(kind, "i")
		if err != nil {
			return nil, err
		}
		where = append(where, kwc)
		args = append(args, kargs...)
	}

	cteResourceWhere, cteResourceArgs, err := resourceDetailWhere(a, "rd")
	if err != nil {
		return nil, err
	}
	outerResourceWhere, outerResourceArgs, err := resourceDetailWhere(a, "d")
	if err != nil {
		return nil, err
	}
	if cteResourceWhere != "" {
		where = append(where, "EXISTS (SELECT 1 FROM MessageResourceDetail rd WHERE rd.message_id = i.message_id AND "+cteResourceWhere+")")
		args = append(args, cteResourceArgs...)
	}
	wc := "1=1"
	if len(where) > 0 {
		wc = strings.Join(where, " AND ")
	}
	outerWC := ""
	if outerResourceWhere != "" {
		outerWC = "WHERE " + outerResourceWhere
	}
	limit := getInt(a, "limit", 50)
	offset := getInt(a, "offset", 0)
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	queryArgs = append(queryArgs, outerResourceArgs...)
	rows, err := db.Query(fmt.Sprintf(`WITH filtered AS (
		SELECT MAX(i.message_id) AS message_id, c.user_name AS talker, sn.user_name AS sender_wxid,
			i.message_local_type, i.message_create_time, i.message_local_id, i.message_svr_id,
			i.message_origin_source, i.packed_info AS message_packed_info
		FROM MessageResourceInfo i
		LEFT JOIN ChatName2Id c ON c.rowid = i.chat_id
		LEFT JOIN SenderName2Id sn ON sn.rowid = i.sender_id
		WHERE %s
		GROUP BY c.user_name, sn.user_name, i.message_local_type, i.message_create_time,
			i.message_local_id, i.message_svr_id, i.message_origin_source, i.packed_info
		ORDER BY i.message_create_time DESC, i.message_local_id DESC
		LIMIT ? OFFSET ?
	)
	SELECT f.message_id, f.talker, f.sender_wxid, f.message_local_type,
		f.message_create_time, f.message_local_id, f.message_svr_id, f.message_origin_source,
		f.message_packed_info,
		d.resource_id, d.type AS resource_type_raw, d.size AS resource_size,
		d.create_time AS resource_create_time, d.access_time AS resource_access_time,
		d.status AS resource_status, d.data_index AS resource_data_index,
		d.packed_info AS resource_packed_info
	FROM filtered f
	JOIN MessageResourceDetail d ON d.message_id = f.message_id
	%s
	ORDER BY f.message_create_time DESC, f.message_local_id DESC, d.resource_id ASC`, wc, outerWC), queryArgs...)
	if err != nil {
		return nil, err
	}
	s.attachDisplayNames(rows, [2]string{"talker", "talker_display_name"}, [2]string{"sender_wxid", "sender_display_name"})
	return s.buildMediaResourceOutput(rows, getBoolDefault(a, "include_local_paths", true), includeDebugOutput(a)), nil
}

func (s *server) buildMediaResourceOutput(rows []wcdb.Row, includeLocalPaths bool, includeDebug bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	byMessage := map[int64]int{}
	self := s.selfWxid()
	for _, r := range rows {
		msgID := rowInt64(r, "message_id")
		idx, ok := byMessage[msgID]
		if !ok {
			baseKind, subtype, kindName := wxkind.Unpack(rowInt64(r, "message_local_type"))
			createTime := rowInt64(r, "message_create_time")
			msgStrings := packedStrings(rowBytes(r, "message_packed_info"))
			item := map[string]any{
				"talker":                rowString(r, "talker"),
				"talker_display_name":   rowString(r, "talker_display_name"),
				"chat_type":             agentChatType(rowString(r, "talker"), wxkind.ClassifyUsername(rowString(r, "talker")), false),
				"local_id":              rowInt64(r, "message_local_id"),
				"server_id":             rowInt64(r, "message_svr_id"),
				"server_id_str":         strconv.FormatInt(rowInt64(r, "message_svr_id"), 10),
				"create_time":           createTime,
				"create_time_human":     time.Unix(createTime, 0).Format("2006-01-02 15:04:05"),
				"sender_wxid":           rowString(r, "sender_wxid"),
				"sender_display_name":   rowString(r, "sender_display_name"),
				"base_kind":             baseKind,
				"subtype":               subtype,
				"kind_name":             kindName,
				"message_origin_source": rowInt64(r, "message_origin_source"),
				"resources":             []map[string]any{},
			}
			if self != "" && rowString(r, "sender_wxid") != "" {
				item["is_from_me"] = rowString(r, "sender_wxid") == self
			}
			if len(msgStrings) > 0 {
				item["message_packed_strings"] = msgStrings
			}
			out = append(out, item)
			idx = len(out) - 1
			byMessage[msgID] = idx
		}
		res := s.mediaResourceFromRow(r, includeLocalPaths)
		resources := out[idx]["resources"].([]map[string]any)
		resources = append(resources, res)
		out[idx]["resources"] = resources
		out[idx]["resource_count"] = len(resources)
	}
	if includeLocalPaths {
		s.attachReadableImageMatchesToMediaResourceItems(out)
	}
	for _, item := range out {
		resources, _ := item["resources"].([]map[string]any)
		paths, uris, hints := mediaResourceReadHints(resources)
		if len(paths) > 0 {
			item["media_local_paths"] = paths
		}
		if len(uris) > 0 {
			item["media_local_path_uris"] = uris
		}
		if len(hints) > 0 {
			item["media_read_hints"] = hints
		}
	}
	if !includeDebug {
		agentReadyMediaResourceOutput(out)
	}
	return out
}

func agentReadyMediaResourceOutput(items []map[string]any) {
	for _, item := range items {
		row := wcdb.Row(item)
		if images, warnings := agentImageRefs(row, false); len(images) > 0 || len(warnings) > 0 {
			if len(images) > 0 {
				item["images"] = images
			}
			if len(warnings) > 0 {
				item["warnings"] = appendUniqueStrings(stringSliceAny(item["warnings"]), warnings...)
			}
		}
		if videos, warnings := agentVideoRefs(row, false); len(videos) > 0 || len(warnings) > 0 {
			if len(videos) > 0 {
				item["videos"] = videos
			}
			if len(warnings) > 0 {
				item["warnings"] = appendUniqueStrings(stringSliceAny(item["warnings"]), warnings...)
			}
		}
		if files, warnings := agentFileRefs(row, false); len(files) > 0 || len(warnings) > 0 {
			if len(files) > 0 {
				item["files"] = files
			}
			if len(warnings) > 0 {
				item["warnings"] = appendUniqueStrings(stringSliceAny(item["warnings"]), warnings...)
			}
		}

		resources := mapSliceAny(item["resources"])
		if len(resources) > 0 {
			cleanResources := make([]map[string]any, 0, len(resources))
			seenResourcePaths := map[string]bool{}
			for _, res := range resources {
				if clean := agentReadyMediaResource(res); len(clean) > 0 {
					if path := rowString(wcdb.Row(clean), "path"); path != "" {
						if seenResourcePaths[path] {
							continue
						}
						seenResourcePaths[path] = true
					}
					cleanResources = append(cleanResources, clean)
				}
			}
			if len(cleanResources) > 0 {
				item["resources"] = cleanResources
			} else {
				delete(item, "resources")
			}
		}

		for _, k := range []string{
			"base_kind", "subtype", "message_origin_source", "message_packed_strings",
			"media_local_paths", "media_local_path_uris", "media_read_hints", "resource_count",
		} {
			delete(item, k)
		}
	}
}

func agentReadyMediaResource(res map[string]any) map[string]any {
	paths := agentReadyMediaResourcePaths(res)
	out := map[string]any{}
	if len(paths) > 0 {
		out["path"] = paths[0]
		for _, k := range []string{"size", "md5", "content_md5", "file_name", "file_names"} {
			if v, ok := res[k]; ok {
				out[k] = v
			}
		}
	}
	if warnings := agentReadyMediaResourceWarnings(res, paths); len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return compactMap(out)
}

func agentReadyMediaResourcePaths(res map[string]any) []string {
	family := rowString(wcdb.Row(res), "resource_family")
	var paths []string
	for _, p := range stringSliceAny(res["direct_readable_local_paths"]) {
		if directReadableMediaPath(family, p) {
			paths = appendUniqueStrings(paths, p)
		}
	}
	for _, p := range stringSliceAny(res["decoded_local_paths"]) {
		if directReadableMediaPath(family, p) {
			paths = appendUniqueStrings(paths, p)
		}
	}
	for _, p := range stringSliceAny(res["local_paths"]) {
		if directReadableMediaPath(family, p) {
			paths = appendUniqueStrings(paths, p)
		}
	}
	return paths
}

func agentReadyMediaResourceWarnings(res map[string]any, readablePaths []string) []string {
	if len(readablePaths) > 0 {
		return nil
	}
	family := rowString(wcdb.Row(res), "resource_family")
	localPaths := stringSliceAny(res["local_paths"])
	var warnings []string
	switch family {
	case "image", "cover":
		for _, d := range mapSliceAny(res["local_path_details"]) {
			if rowString(wcdb.Row(d), "decode_status") == "needs_image_key" {
				warnings = appendUniqueStrings(warnings, "image_decode_needs_image_key")
			}
		}
		if len(localPaths) > 0 {
			warnings = appendUniqueStrings(warnings, "image_paths_not_direct_readable")
		}
	case "video":
		if len(localPaths) > 0 {
			warnings = appendUniqueStrings(warnings, "video_paths_not_direct_readable")
		}
	case "file":
		if len(localPaths) > 0 {
			warnings = appendUniqueStrings(warnings, "file_paths_not_direct_readable")
		}
	}
	return warnings
}

func (s *server) mediaResourceFromRow(r wcdb.Row, includeLocalPaths bool) map[string]any {
	baseKind, _, kindName := wxkind.Unpack(rowInt64(r, "message_local_type"))
	rawType := rowInt64(r, "resource_type_raw")
	family := mediaResourceFamily(rawType, baseKind, kindName)
	msgStrings := packedStrings(rowBytes(r, "message_packed_info"))
	resStrings := packedStrings(rowBytes(r, "resource_packed_info"))
	allStrings := uniqueStrings(append(append([]string{}, msgStrings...), resStrings...))
	md5Value := firstMD5(allStrings)
	fileNames := fileNameStrings(allStrings)
	res := map[string]any{
		"resource_id":       rowInt64(r, "resource_id"),
		"resource_family":   family,
		"resource_type_raw": rawType,
		"variant_code":      rawType >> 16,
		"size":              rowInt64(r, "resource_size"),
		"status":            rowInt64(r, "resource_status"),
		"data_index":        rowString(r, "resource_data_index"),
	}
	if ct := rowInt64(r, "resource_create_time"); ct != 0 {
		res["create_time"] = ct
	}
	if at := rowInt64(r, "resource_access_time"); at != 0 {
		res["access_time"] = at
	}
	if len(resStrings) > 0 {
		res["packed_strings"] = resStrings
	}
	if md5Value != "" {
		res["md5"] = md5Value
	}
	if len(fileNames) > 0 {
		res["file_names"] = fileNames
		res["file_name"] = fileNames[0]
	}
	if includeLocalPaths {
		paths := s.localMediaPaths(rowString(r, "talker"), rowInt64(r, "message_create_time"), family, md5Value, fileNames)
		res["local_paths"] = paths
		s.attachLocalPathAgentFields(res, family, paths)
	}
	return res
}

func (s *server) attachLocalPathAgentFields(res map[string]any, family string, paths []string) {
	if len(paths) == 0 {
		return
	}
	details := make([]map[string]any, 0, len(paths))
	uris := make([]string, 0, len(paths))
	var directPaths []string
	var directURIs []string
	var decodedPaths []string
	var decodedURIs []string
	var decodedFormats []string
	var decodedMIMEs []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		uri := localFileURI(p)
		direct := directReadableMediaPath(family, p)
		info := map[string]any{
			"path":            p,
			"uri":             uri,
			"storage_format":  localMediaStorageFormat(family, p),
			"direct_readable": direct,
		}
		if mime := localMediaMIMEForPath(family, p); mime != "" {
			info["mime_type"] = mime
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			info["file_size"] = st.Size()
		}
		if direct {
			info["decode_status"] = "direct"
			if meta := inspectReadableMediaFile(p); len(meta) > 0 {
				for k, v := range meta {
					info[k] = v
				}
			}
		} else if family == "image" || family == "cover" {
			dec := s.decodeLocalImageForAgent(p, paths)
			for k, v := range dec {
				info[k] = v
			}
			if decodedPath, _ := dec["decoded_path"].(string); decodedPath != "" {
				decodedURI := localFileURI(decodedPath)
				info["direct_readable"] = true
				decodedPaths = appendUniqueStrings(decodedPaths, decodedPath)
				decodedURIs = appendUniqueStrings(decodedURIs, decodedURI)
				if storage, _ := dec["decoded_storage_format"].(string); storage != "" {
					decodedFormats = appendUniqueStrings(decodedFormats, storage)
				}
				if mime, _ := dec["mime_type"].(string); mime != "" {
					decodedMIMEs = appendUniqueStrings(decodedMIMEs, mime)
				}
				directPaths = appendUniqueStrings(directPaths, decodedPath)
				directURIs = appendUniqueStrings(directURIs, decodedURI)
			}
		}
		details = append(details, info)
		uris = append(uris, uri)
		if direct {
			directPaths = append(directPaths, p)
			directURIs = append(directURIs, uri)
		}
	}
	if len(uris) > 0 {
		res["local_path_uris"] = uris
	}
	if len(details) > 0 {
		res["local_path_details"] = details
	}
	res["direct_readable"] = len(directPaths) > 0
	if len(decodedPaths) > 0 {
		res["decoded_local_paths"] = decodedPaths
		res["decoded_local_path_uris"] = decodedURIs
	}
	if len(decodedFormats) > 0 {
		res["decoded_storage_formats"] = decodedFormats
	}
	if len(decodedMIMEs) > 0 {
		res["decoded_mime_types"] = decodedMIMEs
	}
	if len(directPaths) > 0 {
		res["direct_readable_local_paths"] = directPaths
		res["direct_readable_local_path_uris"] = directURIs
	}
}

func localFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func localMediaMIMEForPath(family, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if family == "voice" {
		switch ext {
		case ".silk":
			return "audio/silk"
		case ".amr":
			return "audio/amr"
		case ".mp3":
			return "audio/mpeg"
		case ".m4a":
			return "audio/mp4"
		case ".aac":
			return "audio/aac"
		case ".wav":
			return "audio/wav"
		case ".ogg":
			return "audio/ogg"
		case ".opus":
			return "audio/opus"
		}
	}
	return ""
}

func localMediaStorageFormat(family, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if family == "image" || family == "cover" {
		if ext == ".dat" {
			return "wechat_image_dat"
		}
		if directImageExt(ext) {
			return "image_file"
		}
	}
	if family == "video" {
		if ext == ".mp4" || ext == ".mov" || ext == ".m4v" {
			return "video_file"
		}
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			return "video_cover_file"
		}
	}
	if family == "voice" {
		switch ext {
		case ".silk":
			return "wechat_silk"
		case ".amr":
			return "amr_audio"
		case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".opus":
			return strings.TrimPrefix(ext, ".") + "_audio"
		}
	}
	if family == "file" {
		return "file"
	}
	if ext != "" {
		return strings.TrimPrefix(ext, ".") + "_file"
	}
	return "unknown"
}

func directReadableMediaPath(family, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch family {
	case "image", "cover":
		return directImageExt(ext)
	case "video":
		return directVideoFilePath(path) || directImageExt(ext)
	case "voice":
		switch ext {
		case ".silk", ".amr", ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".opus":
			return true
		default:
			return false
		}
	case "file":
		return ext != ".dat"
	default:
		return ext != "" && ext != ".dat"
	}
}

func directVideoFilePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".m4v":
		return true
	default:
		return false
	}
}

func directImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".heif":
		return true
	default:
		return false
	}
}

var (
	wechatV4ImageHeader1 = []byte{0x07, 0x08, 0x56, 0x31, 0x08, 0x07}
	wechatV4ImageHeader2 = []byte{0x07, 0x08, 0x56, 0x32, 0x08, 0x07}
	wechatV4FixedAESKey  = []byte("cfcd208495d565ef")
	jpegTail             = []byte{0xff, 0xd9}
)

func (s *server) decodeLocalImageForAgent(path string, siblingPaths []string) map[string]any {
	if strings.ToLower(filepath.Ext(path)) != ".dat" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{
			"decode_status": "decode_failed",
			"decode_error":  err.Error(),
		}
	}
	decoded, ext, meta, status, err := s.decodeWechatImageDAT(data, siblingPaths)
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	if err != nil {
		out["decode_status"] = status
		out["decode_error"] = err.Error()
		return out
	}
	decodedPath, err := s.writeDecodedMediaCache(path, decoded, ext)
	if err != nil {
		out["decode_status"] = "decode_failed"
		out["decode_error"] = err.Error()
		return out
	}
	out["decode_status"] = "decoded"
	out["decoded_path"] = decodedPath
	out["decoded_uri"] = localFileURI(decodedPath)
	out["decoded_storage_format"] = "image_file"
	if st, err := os.Stat(decodedPath); err == nil && !st.IsDir() {
		out["decoded_file_size"] = st.Size()
	}
	for k, v := range mediaBytesInfo(decoded) {
		out[k] = v
	}
	return out
}

func (s *server) decodeWechatImageDAT(data []byte, siblingPaths []string) ([]byte, string, map[string]any, string, error) {
	meta := map[string]any{}
	if len(data) < 4 {
		return nil, "", meta, "decode_failed", fmt.Errorf("data length is too short: %d", len(data))
	}
	if len(data) >= len(wechatV4ImageHeader1) &&
		(bytes.Equal(data[:len(wechatV4ImageHeader1)], wechatV4ImageHeader1) ||
			bytes.Equal(data[:len(wechatV4ImageHeader2)], wechatV4ImageHeader2)) {
		decoded, ext, v4Meta, status, err := s.decodeWechatV4ImageDAT(data, siblingPaths)
		for k, v := range v4Meta {
			meta[k] = v
		}
		return decoded, ext, meta, status, err
	}
	decoded, ext, ok := decodeWechatV3XORImage(data)
	if !ok {
		meta["decode_method"] = "unknown"
		return nil, "", meta, "unsupported_format", fmt.Errorf("unrecognized WeChat image dat header: %x", data[:minInt(len(data), 6)])
	}
	meta["decode_method"] = "wechat_v3_xor"
	return decoded, ext, meta, "decoded", nil
}

func (s *server) decodeWechatV4ImageDAT(data []byte, siblingPaths []string) ([]byte, string, map[string]any, string, error) {
	meta := map[string]any{
		"decode_method": "wechat_v4_dat_aes_ecb_xor",
	}
	if len(data) < 15 {
		return nil, "", meta, "decode_failed", fmt.Errorf("data length is too short for WeChat v4 image dat: %d", len(data))
	}
	aesSize := binary.LittleEndian.Uint32(data[6:10])
	xorSize := binary.LittleEndian.Uint32(data[10:14])
	meta["v4_aes_size"] = aesSize
	meta["v4_xor_size"] = xorSize
	xorKey, source := deriveWechatV4XORKey(data, siblingPaths)
	if source == "default" && s != nil && s.cfg != nil && s.cfg.ImageXORKey != nil && *s.cfg.ImageXORKey >= 0 && *s.cfg.ImageXORKey <= 255 {
		xorKey = byte(*s.cfg.ImageXORKey)
		source = "config"
	}
	meta["v4_xor_key"] = fmt.Sprintf("0x%02x", xorKey)
	meta["v4_xor_key_source"] = source

	var keys []imageKeyCandidate
	usesDynamicImageKey := false
	if bytes.Equal(data[:len(wechatV4ImageHeader1)], wechatV4ImageHeader1) {
		keys = []imageKeyCandidate{{key: wechatV4FixedAESKey, source: "format1_fixed"}}
	} else {
		usesDynamicImageKey = true
		keys = s.imageKeyCandidates()
		if len(keys) == 0 {
			if s.refreshImageKeyForDecode(meta, "missing WeChat V4 image_key", false) {
				keys = s.imageKeyCandidates()
			}
			if len(keys) == 0 {
				meta["v4_key_source"] = "missing"
				return nil, "", meta, "needs_image_key", fmt.Errorf("WeChat v4 image .dat requires image_key; wechat-cli could not refresh it automatically")
			}
		}
	}

	decoded, ext, source, lastErr := tryDecodeWechatV4WithImageKeys(data, xorKey, keys)
	if lastErr == nil {
		meta["v4_key_source"] = source
		if ext == "wxgf" {
			return nil, "", meta, "unsupported_format", fmt.Errorf("decoded WXGF image needs a converter before agent-visible output")
		}
		return decoded, ext, meta, "decoded", nil
	}
	if usesDynamicImageKey && envFirst("WECHAT_CLI_IMAGE_KEY", "WX_MCP_IMAGE_KEY") == "" && s.refreshImageKeyForDecode(meta, "configured WeChat V4 image_key failed", true) {
		keys = s.imageKeyCandidates()
		decoded, ext, source, lastErr = tryDecodeWechatV4WithImageKeys(data, xorKey, keys)
		if lastErr == nil {
			meta["v4_key_source"] = source
			if ext == "wxgf" {
				return nil, "", meta, "unsupported_format", fmt.Errorf("decoded WXGF image needs a converter before agent-visible output")
			}
			return decoded, ext, meta, "decoded", nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("image_key did not decrypt the image")
	}
	meta["v4_key_source"] = "config_or_env"
	return nil, "", meta, "decode_failed", lastErr
}

func (s *server) refreshImageKeyForDecode(meta map[string]any, reason string, force bool) bool {
	if s == nil {
		meta["image_key_refresh"] = "unavailable"
		return false
	}
	if err := s.refreshImageKeyFromWxkey(reason, force); err != nil {
		meta["image_key_refresh"] = "failed"
		meta["image_key_refresh_error"] = err.Error()
		return false
	}
	meta["image_key_refresh"] = "ok"
	return true
}

func tryDecodeWechatV4WithImageKeys(data []byte, xorKey byte, keys []imageKeyCandidate) ([]byte, string, string, error) {
	var lastErr error
	for _, cand := range keys {
		decoded, err := decodeWechatV4ImageData(data, cand.key, xorKey)
		if err != nil {
			lastErr = err
			continue
		}
		ext := imageExtForData(decoded)
		if ext == "" {
			lastErr = fmt.Errorf("unknown image type after decryption: %x", decoded[:minInt(len(decoded), 6)])
			continue
		}
		return decoded, ext, cand.source, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("image_key did not decrypt the image")
	}
	return nil, "", "", lastErr
}

func decodeWechatV4ImageData(data, aesKey []byte, xorKey byte) ([]byte, error) {
	if len(data) < 15 {
		return nil, fmt.Errorf("data length is too short for WeChat v4 image dat: %d", len(data))
	}
	aesSize := binary.LittleEndian.Uint32(data[6:10])
	xorSize := binary.LittleEndian.Uint32(data[10:14])
	fileData := data[15:]
	alignedAesSize := aesSize
	if aesSize > 0 {
		pad := uint32(aes.BlockSize) - aesSize%uint32(aes.BlockSize)
		if pad == 0 {
			pad = uint32(aes.BlockSize)
		}
		alignedAesSize += pad
	}
	if uint32(len(fileData)) < alignedAesSize {
		return nil, fmt.Errorf("file data too short for declared AES length")
	}
	aesPart := fileData[:alignedAesSize]
	remaining := fileData[alignedAesSize:]
	if uint32(len(remaining)) < xorSize {
		return nil, fmt.Errorf("file data too short for declared XOR length")
	}
	var decodedAES []byte
	var err error
	if len(aesPart) > 0 {
		decodedAES, err = decryptAESECBPKCS7(aesPart, aesKey)
		if err != nil {
			return nil, fmt.Errorf("AES decryption failed: %w", err)
		}
	}
	rawLen := uint32(len(remaining)) - xorSize
	rawMiddle := remaining[:rawLen]
	xorTail := remaining[rawLen:]
	decodedXOR := make([]byte, len(xorTail))
	for i := range xorTail {
		decodedXOR[i] = xorTail[i] ^ xorKey
	}
	out := make([]byte, 0, len(decodedAES)+len(rawMiddle)+len(decodedXOR))
	out = append(out, decodedAES...)
	out = append(out, rawMiddle...)
	out = append(out, decodedXOR...)
	return out, nil
}

func decryptAESECBPKCS7(data, key []byte) ([]byte, error) {
	if len(key) < aes.BlockSize {
		return nil, fmt.Errorf("image key length %d is shorter than AES-128 key length", len(key))
	}
	key = key[:aes.BlockSize]
	if len(data) == 0 {
		return []byte{}, nil
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("data length %d is not a multiple of AES block size", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	for start := 0; start < len(data); start += aes.BlockSize {
		block.Decrypt(out[start:start+aes.BlockSize], data[start:start+aes.BlockSize])
	}
	if len(out) == 0 {
		return out, nil
	}
	padding := int(out[len(out)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(out) {
		return nil, fmt.Errorf("invalid PKCS7 padding length: %d", padding)
	}
	for i := len(out) - padding; i < len(out); i++ {
		if out[i] != byte(padding) {
			return nil, fmt.Errorf("invalid PKCS7 padding content")
		}
	}
	return out[:len(out)-padding], nil
}

type imageKeyCandidate struct {
	key    []byte
	source string
}

func (s *server) imageKeyCandidates() []imageKeyCandidate {
	type rawKey struct {
		value  string
		source string
	}
	var raws []rawKey
	if raw := envFirst("WECHAT_CLI_IMAGE_KEY", "WX_MCP_IMAGE_KEY"); raw != "" {
		raws = append(raws, rawKey{value: raw, source: "env"})
	}
	if s != nil && s.cfg != nil {
		if raw := strings.TrimSpace(s.cfg.ImageKey); raw != "" {
			raws = append(raws, rawKey{value: raw, source: "config"})
		}
	}
	if len(raws) == 0 {
		return nil
	}
	var out []imageKeyCandidate
	seen := map[string]bool{}
	for _, rk := range raws {
		if len(rk.value) >= aes.BlockSize {
			key := []byte(rk.value[:aes.BlockSize])
			id := string(key)
			if !seen[id] {
				seen[id] = true
				out = append(out, imageKeyCandidate{key: key, source: rk.source + "_raw"})
			}
		}
		if decoded, err := hex.DecodeString(rk.value); err == nil && len(decoded) >= aes.BlockSize {
			key := decoded[:aes.BlockSize]
			id := string(key)
			if !seen[id] {
				seen[id] = true
				out = append(out, imageKeyCandidate{key: key, source: rk.source + "_hex"})
			}
		}
	}
	return out
}

func deriveWechatV4XORKey(data []byte, siblingPaths []string) (byte, string) {
	for _, p := range siblingPaths {
		if !strings.HasSuffix(strings.ToLower(filepath.Base(p)), "_t.dat") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if k, ok := deriveWechatV4XORKeyFromTail(b); ok {
			return k, "thumbnail_tail"
		}
	}
	if k, ok := deriveWechatV4XORKeyFromTail(data); ok {
		return k, "file_tail"
	}
	return 0x37, "default"
}

func deriveWechatV4XORKeyFromTail(data []byte) (byte, bool) {
	if len(data) < 17 || len(data) < len(wechatV4ImageHeader2) ||
		(!bytes.Equal(data[:len(wechatV4ImageHeader1)], wechatV4ImageHeader1) &&
			!bytes.Equal(data[:len(wechatV4ImageHeader2)], wechatV4ImageHeader2)) {
		return 0, false
	}
	xorSize := binary.LittleEndian.Uint32(data[10:14])
	if xorSize < 2 {
		return 0, false
	}
	fileData := data[15:]
	if uint32(len(fileData)) < xorSize {
		return 0, false
	}
	xorTail := fileData[uint32(len(fileData))-xorSize:]
	if len(xorTail) < 2 {
		return 0, false
	}
	k0 := xorTail[len(xorTail)-2] ^ jpegTail[0]
	k1 := xorTail[len(xorTail)-1] ^ jpegTail[1]
	if k0 != k1 {
		return 0, false
	}
	return k0, true
}

func decodeWechatV3XORImage(data []byte) ([]byte, string, bool) {
	formats := []struct {
		ext    string
		header []byte
	}{
		{"jpg", []byte{0xff, 0xd8, 0xff}},
		{"png", []byte{0x89, 0x50, 0x4e, 0x47}},
		{"gif", []byte{0x47, 0x49, 0x46, 0x38}},
		{"bmp", []byte{0x42, 0x4d}},
		{"tiff", []byte{0x49, 0x49, 0x2a, 0x00}},
	}
	for _, f := range formats {
		if len(data) < len(f.header) {
			continue
		}
		key := data[0] ^ f.header[0]
		ok := true
		for i := range f.header {
			if data[i]^key != f.header[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out := make([]byte, len(data))
		for i := range data {
			out[i] = data[i] ^ key
		}
		return out, f.ext, true
	}
	return nil, "", false
}

func (s *server) writeDecodedMediaCache(srcPath string, data []byte, ext string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("decoded media is empty")
	}
	dir, err := s.mediaDecodeCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	base := safeCacheID(strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath)))
	if base == "" {
		base = "media"
	}
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		ext = "bin"
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s.%s", base, hex.EncodeToString(sum[:8]), ext))
	if st, err := os.Stat(dst); err == nil && !st.IsDir() && st.Size() == int64(len(data)) {
		return dst, nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func (s *server) mediaDecodeCacheDir() (string, error) {
	stateDir, err := appStateDir()
	if err != nil {
		return "", err
	}
	id := "default"
	if s != nil && s.cfg != nil {
		id = s.cfg.Wxid
		if id == "" && s.cfg.DBRoot != "" {
			sum := md5.Sum([]byte(s.cfg.DBRoot))
			id = "root-" + hex.EncodeToString(sum[:8])
		}
	}
	return filepath.Join(stateDir, "media-cache", safeCacheID(id)), nil
}

func imageExtForData(data []byte) string {
	switch {
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		return "jpg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}):
		return "png"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x47, 0x49, 0x46, 0x38}):
		return "gif"
	case len(data) >= 2 && bytes.Equal(data[:2], []byte{0x42, 0x4d}):
		return "bmp"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x77, 0x78, 0x67, 0x66}):
		return "wxgf"
	default:
		return ""
	}
}

func mediaBytesInfo(data []byte) map[string]any {
	out := map[string]any{}
	if ext := imageExtForData(data); ext != "" {
		out["mime_type"] = imageMIMEForExt(ext)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		out["width"] = cfg.Width
		out["height"] = cfg.Height
	}
	return out
}

func inspectReadableMediaFile(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return mediaBytesInfo(b)
}

func imageMIMEForExt(ext string) string {
	switch strings.TrimPrefix(strings.ToLower(ext), ".") {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func attachMediaReadHintsToMessages(rows []wcdb.Row) {
	attachMediaReadHintsToMessagesWithServer(nil, rows)
}

func (s *server) attachMediaReadHintsToMessages(rows []wcdb.Row) {
	attachMediaReadHintsToMessagesWithServer(s, rows)
}

func attachMediaReadHintsToMessagesWithServer(s *server, rows []wcdb.Row) {
	for _, r := range rows {
		_, _, hints := mediaResourceReadHints(rowMediaResources(r))
		hints = append(hints, messageXMLMediaReadHints(s, r)...)
		if len(hints) > 0 {
			r["media_read_hints"] = hints
		}
		delete(r, "_media_read_hint_resources")
	}
}

func rowMediaResources(r wcdb.Row) []map[string]any {
	if resources, ok := r["_media_read_hint_resources"].([]map[string]any); ok {
		return resources
	}
	if resources, ok := r["media_resources"].([]map[string]any); ok {
		return resources
	}
	return nil
}

func mediaResourceReadHints(resources []map[string]any) ([]string, []string, []map[string]any) {
	var allPaths []string
	var allURIs []string
	var allDetails []map[string]any
	var directPaths []string
	var directURIs []string
	var decodedPaths []string
	var decodedURIs []string
	var resourceIDs []any
	var families []string
	for _, res := range resources {
		paths := stringSliceAny(res["local_paths"])
		uris := stringSliceAny(res["local_path_uris"])
		allPaths = appendUniqueStrings(allPaths, paths...)
		allURIs = appendUniqueStrings(allURIs, uris...)
		allDetails = appendUniquePathDetails(allDetails, mapSliceAny(res["local_path_details"])...)
		directPaths = appendUniqueStrings(directPaths, stringSliceAny(res["direct_readable_local_paths"])...)
		directURIs = appendUniqueStrings(directURIs, stringSliceAny(res["direct_readable_local_path_uris"])...)
		decodedPaths = appendUniqueStrings(decodedPaths, stringSliceAny(res["decoded_local_paths"])...)
		decodedURIs = appendUniqueStrings(decodedURIs, stringSliceAny(res["decoded_local_path_uris"])...)
		if id := res["resource_id"]; id != nil {
			resourceIDs = appendUniqueAny(resourceIDs, id)
		}
		if fam, ok := res["resource_family"].(string); ok && fam != "" {
			families = appendUniqueStrings(families, fam)
		}
	}
	if len(allPaths) == 0 && len(allURIs) == 0 {
		return allPaths, allURIs, nil
	}
	h := map[string]any{
		"source":             "message_resource",
		"address_type":       "local_file",
		"resource_ids":       resourceIDs,
		"direct_readable":    len(directPaths) > 0,
		"local_paths":        allPaths,
		"local_path_uris":    allURIs,
		"local_path_details": allDetails,
	}
	if len(families) == 1 {
		h["resource_family"] = families[0]
	} else if len(families) > 1 {
		h["resource_families"] = families
	}
	if len(directPaths) > 0 {
		h["direct_readable_local_paths"] = directPaths
		h["direct_readable_local_path_uris"] = directURIs
	}
	if len(decodedPaths) > 0 {
		h["decoded_local_paths"] = decodedPaths
		h["decoded_local_path_uris"] = decodedURIs
	}
	hints := []map[string]any{h}
	return allPaths, allURIs, hints
}

func messageXMLMediaReadHints(s *server, r wcdb.Row) []map[string]any {
	p, _ := r["message_content_parsed"].(map[string]any)
	if p == nil {
		return nil
	}
	switch rowInt64(r, "base_kind") {
	case 3:
		if h := imageXMLReadHint(p); h != nil {
			return []map[string]any{h}
		}
	case 34:
		if h := voiceXMLReadHint(s, r, p, "message_xml", ""); h != nil {
			return []map[string]any{h}
		}
	case 47:
		if h := emojiXMLReadHint(p); h != nil {
			return []map[string]any{h}
		}
	case 49:
		if rowInt64(r, "subtype") == 57 {
			return quoteXMLReadHints(s, r, p)
		}
		if rowInt64(r, "subtype") == 19 {
			return forwardXMLReadHints(s, r, p)
		}
	}
	return nil
}

func imageXMLReadHint(p map[string]any) map[string]any {
	cdn := compactMap(map[string]any{
		"mid_url":   stringMapValue(p, "cdn_mid_url"),
		"big_url":   stringMapValue(p, "cdn_big_url"),
		"thumb_url": stringMapValue(p, "cdn_thumb_url"),
	})
	h := compactMap(map[string]any{
		"source":          "message_xml",
		"address_type":    "wechat_cdn",
		"resource_family": "image",
		"direct_readable": false,
		"wechat_cdn":      cdn,
		"aeskey":          stringMapValue(p, "aeskey"),
		"md5":             stringMapValue(p, "md5"),
		"length":          p["length"],
		"hd_length":       p["hd_length"],
		"mid_width":       p["mid_width"],
		"mid_height":      p["mid_height"],
		"hd_width":        p["hd_width"],
		"hd_height":       p["hd_height"],
	})
	if len(h) <= 4 && len(cdn) == 0 {
		return nil
	}
	return h
}

type voiceBlob struct {
	createTime int64
	localID    int64
	serverID   int64
	data       []byte
	dataIndex  string
}

func voiceXMLReadHint(s *server, r wcdb.Row, p map[string]any, source, role string) map[string]any {
	h := compactMap(map[string]any{
		"source":          source,
		"address_type":    "wechat_voice",
		"resource_family": "voice",
		"direct_readable": false,
		"duration_ms":     p["duration_ms"],
		"format":          voiceFormatName(p["voice_format"]),
		"length":          p["length"],
	})
	if role != "" {
		h["message_role"] = role
	}
	if s == nil {
		if len(h) <= 4 {
			return nil
		}
		return h
	}
	talker := rowString(r, "talker")
	localID := rowInt64(r, "local_id")
	serverID := rowInt64(r, "server_id")
	createTime := rowInt64(r, "create_time")
	blob, err := s.voiceBlob(talker, localID, serverID, createTime)
	if err != nil || blob == nil || len(blob.data) == 0 {
		if err != nil {
			h["lookup_error"] = err.Error()
		}
		if len(h) <= 4 {
			return nil
		}
		return h
	}
	path, err := s.writeVoiceCache(talker, blob, p)
	if err != nil {
		h["lookup_error"] = err.Error()
		return h
	}
	h["source"] = "media_0_voiceinfo"
	h["address_type"] = "local_file"
	h["size"] = int64(len(blob.data))
	h["data_index"] = blob.dataIndex
	h["voice_create_time"] = blob.createTime
	if blob.localID != 0 {
		h["voice_local_id"] = blob.localID
	}
	if blob.serverID != 0 {
		h["voice_server_id_str"] = strconv.FormatInt(blob.serverID, 10)
	}
	h["local_paths"] = []string{path}
	s.attachLocalPathAgentFields(h, "voice", []string{path})
	if transcript := s.voiceTranscriptForAgent(path); len(transcript) > 0 {
		h["transcript"] = transcript
	}
	return h
}

func (s *server) voiceBlob(talker string, localID, serverID, createTime int64) (*voiceBlob, error) {
	if s == nil || talker == "" {
		return nil, nil
	}
	var clauses []string
	var args []any
	args = append(args, talker)
	if localID != 0 && createTime != 0 {
		clauses = append(clauses, "(v.local_id = ? AND ABS(v.create_time - ?) <= 5)")
		args = append(args, localID, createTime)
	}
	if serverID != 0 {
		clauses = append(clauses, "(v.svr_id = ? AND v.svr_id != 0)")
		args = append(args, serverID)
	}
	if createTime != 0 {
		clauses = append(clauses, "v.create_time = ?")
		args = append(args, createTime)
	}
	if localID != 0 {
		clauses = append(clauses, "v.local_id = ?")
		args = append(args, localID)
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	db, err := s.openDB("message", "media_0.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	args = append(args, serverID, createTime, createTime)
	rows, err := db.Query(fmt.Sprintf(`SELECT v.create_time, v.local_id, v.svr_id,
		v.voice_data, v.data_index
		FROM VoiceInfo v
		LEFT JOIN Name2Id n ON n.rowid = v.chat_name_id
		WHERE n.user_name = ? AND (%s)
		ORDER BY CASE WHEN v.svr_id = ? AND v.svr_id != 0 THEN 0 ELSE 1 END,
			CASE WHEN ? != 0 THEN ABS(v.create_time - ?) ELSE 0 END,
			v.create_time DESC
		LIMIT 1`, strings.Join(clauses, " OR ")), args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	data := rowBytes(rows[0], "voice_data")
	if len(data) == 0 {
		return nil, nil
	}
	return &voiceBlob{
		createTime: rowInt64(rows[0], "create_time"),
		localID:    rowInt64(rows[0], "local_id"),
		serverID:   rowInt64(rows[0], "svr_id"),
		data:       data,
		dataIndex:  rowString(rows[0], "data_index"),
	}, nil
}

func (s *server) writeVoiceCache(talker string, blob *voiceBlob, p map[string]any) (string, error) {
	if blob == nil || len(blob.data) == 0 {
		return "", fmt.Errorf("voice data is empty")
	}
	ext := voiceExtForData(blob.data)
	if ext == "" {
		ext = "silk"
	}
	base := fmt.Sprintf("voice-%s-%d-%d", talkerHash(talker), blob.localID, blob.createTime)
	if blob.serverID != 0 {
		base += "-" + strconv.FormatInt(blob.serverID, 10)
	}
	if ms, ok := integerArgValue(p["duration_ms"]); ok && ms > 0 {
		base += fmt.Sprintf("-%dms", ms)
	}
	return s.writeDecodedMediaCache(base+"."+ext, blob.data, ext)
}

func (s *server) voiceTranscriptForAgent(audioPath string) map[string]any {
	audioPath = strings.TrimSpace(audioPath)
	if audioPath == "" {
		return nil
	}
	cachePath := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".transcript.json"
	if cached := readVoiceTranscriptCache(cachePath); voiceTranscriptCacheUsable(cached) {
		return cached
	}
	asrPath, err := s.voiceAudioForASR(audioPath)
	if err != nil {
		return writeVoiceTranscriptCache(cachePath, compactMap(map[string]any{
			"cache_version": voiceTranscriptCacheVersion,
			"status":        "unavailable",
			"engine":        "local_asr",
		}))
	}
	text, engine, model, err := runLocalVoiceASR(asrPath)
	status := "ok"
	if err != nil {
		status = "unavailable"
	}
	text = cleanVoiceTranscriptText(text)
	if status == "ok" && text == "" {
		status = "no_speech"
	}
	return writeVoiceTranscriptCache(cachePath, compactMap(map[string]any{
		"cache_version": voiceTranscriptCacheVersion,
		"status":        status,
		"text":          text,
		"engine":        engine,
		"model":         model,
	}))
}

const voiceTranscriptCacheVersion = 3

func voiceTranscriptCacheUsable(cached map[string]any) bool {
	if len(cached) == 0 {
		return false
	}
	version, ok := integerArgValue(cached["cache_version"])
	return ok && version == voiceTranscriptCacheVersion
}

func readVoiceTranscriptCache(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return compactMap(out)
}

func writeVoiceTranscriptCache(path string, transcript map[string]any) map[string]any {
	transcript = compactMap(transcript)
	if len(transcript) == 0 {
		return nil
	}
	if data, err := json.MarshalIndent(transcript, "", "  "); err == nil {
		_ = os.WriteFile(path, append(data, '\n'), 0o600)
	}
	return transcript
}

func (s *server) voiceAudioForASR(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav", ".mp3", ".flac", ".ogg":
		return path, nil
	case ".silk":
		return s.decodeSILKVoiceToWAV(path)
	default:
		return "", fmt.Errorf("unsupported voice audio format for local ASR: %s", ext)
	}
}

func (s *server) decodeSILKVoiceToWAV(path string) (string, error) {
	decoder := findSILKDecoder()
	if decoder == "" {
		return "", fmt.Errorf("SILK decoder not found; set WECHAT_CLI_SILK_DECODER")
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	wavPath := base + ".wav"
	if st, err := os.Stat(wavPath); err == nil && !st.IsDir() && st.Size() > 44 {
		return wavPath, nil
	}
	pcmPath := base + ".pcm"
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, decoder, path, pcmPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(pcmPath)
		return "", fmt.Errorf("SILK decode failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	pcm, err := os.ReadFile(pcmPath)
	_ = os.Remove(pcmPath)
	if err != nil {
		return "", err
	}
	if err := writePCM16LEMonoWAV(wavPath, pcm, 24000); err != nil {
		return "", err
	}
	return wavPath, nil
}

func findSILKDecoder() string {
	if p := envFirst("WECHAT_CLI_SILK_DECODER", "WX_MCP_SILK_DECODER"); p != "" {
		return p
	}
	for _, name := range []string{"silk_v3_decoder", "silk_decoder", "silk-decoder"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func writePCM16LEMonoWAV(path string, pcm []byte, sampleRate int) error {
	if len(pcm) == 0 {
		return fmt.Errorf("pcm audio is empty")
	}
	var buf bytes.Buffer
	byteRate := uint32(sampleRate * 2)
	blockAlign := uint16(2)
	dataLen := uint32(len(pcm))
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36)+dataLen)
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, byteRate)
	_ = binary.Write(&buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataLen)
	buf.Write(pcm)
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func runLocalVoiceASR(audioPath string) (text, engine, model string, err error) {
	if cmd := envFirst("WECHAT_CLI_VOICE_TRANSCRIBE_CMD", "WX_MCP_VOICE_TRANSCRIBE_CMD"); cmd != "" {
		text, err = runConfiguredVoiceTranscriber(cmd, audioPath)
		return text, "custom", "", err
	}
	if python := findFasterWhisperPython(); python != "" {
		return runFasterWhisperVoiceASRWithPython(python, audioPath)
	}
	cli, err := exec.LookPath(firstNonEmpty(envFirst("WECHAT_CLI_WHISPER_CLI", "WX_MCP_WHISPER_CLI"), "whisper-cli"))
	if err != nil {
		return "", "local_asr", "", fmt.Errorf("whisper-cli not found")
	}
	model = findWhisperModel()
	if model == "" {
		return "", "whisper.cpp", "", fmt.Errorf("whisper model not found; set WECHAT_CLI_WHISPER_MODEL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "-m", model, "-f", audioPath, "-l", "auto", "-nt", "-np").Output()
	if err != nil {
		return "", "whisper.cpp", model, err
	}
	return string(out), "whisper.cpp", model, nil
}

const defaultFasterWhisperModel = "large-v3"

func runFasterWhisperVoiceASR(audioPath string) (text, engine, model string, err error) {
	python := findFasterWhisperPython()
	if python == "" {
		return "", "faster-whisper", defaultFasterWhisperModel, fmt.Errorf("faster-whisper python runtime not found")
	}
	return runFasterWhisperVoiceASRWithPython(python, audioPath)
}

func runFasterWhisperVoiceASRWithPython(python, audioPath string) (text, engine, model string, err error) {
	model = firstNonEmpty(envFirst("WECHAT_CLI_FASTER_WHISPER_MODEL", "WX_MCP_FASTER_WHISPER_MODEL"), defaultFasterWhisperModel)
	language := firstNonEmpty(envFirst("WECHAT_CLI_FASTER_WHISPER_LANGUAGE", "WX_MCP_FASTER_WHISPER_LANGUAGE"), envFirst("WECHAT_CLI_VOICE_LANGUAGE", "WX_MCP_VOICE_LANGUAGE"), "zh")
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, python, "-c", fasterWhisperPythonScript, audioPath, model, language).CombinedOutput()
	if err != nil {
		return "", "faster-whisper", model, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), "faster-whisper", model, nil
}

const fasterWhisperPythonScript = `
import os
import sys
from faster_whisper import WhisperModel

audio_path = sys.argv[1]
model_name = sys.argv[2]
language = sys.argv[3] if len(sys.argv) > 3 else "zh"
device = os.environ.get("WECHAT_CLI_FASTER_WHISPER_DEVICE") or os.environ.get("WX_MCP_FASTER_WHISPER_DEVICE", "cpu")
compute_type = os.environ.get("WECHAT_CLI_FASTER_WHISPER_COMPUTE_TYPE") or os.environ.get("WX_MCP_FASTER_WHISPER_COMPUTE_TYPE", "int8")

model = WhisperModel(model_name, device=device, compute_type=compute_type)
kwargs = {"beam_size": 5, "vad_filter": True}
if language and language.lower() != "auto":
    kwargs["language"] = language
segments, info = model.transcribe(audio_path, **kwargs)
print("".join(segment.text for segment in segments).strip())
`

func findFasterWhisperPython() string {
	var candidates []string
	if p := envFirst("WECHAT_CLI_FASTER_WHISPER_PYTHON", "WX_MCP_FASTER_WHISPER_PYTHON"); p != "" {
		candidates = append(candidates, p)
	}
	if home, _ := os.UserHomeDir(); home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".hermes", "venv", "bin", "python3"),
			filepath.Join(home, "code", "hermes-agent", "venv", "bin", "python3"),
		)
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if pythonHasFasterWhisper(p) {
			return p
		}
	}
	return ""
}

func pythonHasFasterWhisper(python string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, python, "-c", "import faster_whisper").Run() == nil
}

func runConfiguredVoiceTranscriber(command, audioPath string) (string, error) {
	if runtime.GOOS == "windows" {
		if out, err := runConfiguredVoiceTranscriberDirect(command, audioPath); err == nil {
			return string(out), nil
		}
	}
	if !strings.Contains(command, "{audio}") {
		command += " " + shellQuote(audioPath)
	} else {
		command = strings.ReplaceAll(command, "{audio}", shellQuote(audioPath))
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	out, err := runShellCommandOutput(ctx, command)
	return string(out), err
}

func runConfiguredVoiceTranscriberDirect(command, audioPath string) ([]byte, error) {
	args, ok := splitWindowsCommandLine(command)
	if !ok || len(args) == 0 {
		return nil, fmt.Errorf("parse voice transcriber command")
	}
	hasAudio := strings.Contains(command, "{audio}")
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "{audio}", audioPath)
	}
	if !hasAudio {
		args = append(args, audioPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	return exec.CommandContext(ctx, args[0], args[1:]...).Output()
}

func splitWindowsCommandLine(s string) ([]string, bool) {
	var args []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '"':
			if inQuote && i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i++
			} else {
				inQuote = !inQuote
			}
		case ' ', '\t', '\r', '\n':
			if inQuote {
				b.WriteByte(ch)
			} else if b.Len() > 0 {
				args = append(args, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(ch)
		}
	}
	if inQuote {
		return nil, false
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	return args, true
}

func runShellCommandOutput(ctx context.Context, command string) ([]byte, error) {
	if runtime.GOOS == "windows" {
		shell := strings.TrimSpace(os.Getenv("COMSPEC"))
		if shell == "" {
			shell = "cmd.exe"
		}
		if strings.HasPrefix(strings.TrimSpace(command), `"`) {
			command = `"` + command + `"`
		}
		return exec.CommandContext(ctx, shell, "/C", command).Output()
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command).Output()
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func findWhisperModel() string {
	if p := envFirst("WECHAT_CLI_WHISPER_MODEL", "WX_MCP_WHISPER_MODEL"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if home != "" {
		for _, pattern := range []string{
			filepath.Join(home, ".cache", "whisper-cpp", "ggml-*.bin"),
			filepath.Join(home, ".cache", "whisper", "ggml-*.bin"),
			filepath.Join(home, ".local", "share", "whisper-cpp", "ggml-*.bin"),
		} {
			matches, _ := filepath.Glob(pattern)
			candidates = append(candidates, matches...)
		}
	}
	for _, pattern := range []string{
		"/opt/homebrew/share/whisper-cpp/ggml-*.bin",
		"/usr/local/share/whisper-cpp/ggml-*.bin",
	} {
		matches, _ := filepath.Glob(pattern)
		candidates = append(candidates, matches...)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return whisperModelRank(candidates[i]) > whisperModelRank(candidates[j])
	})
	for _, p := range candidates {
		if strings.Contains(filepath.Base(p), "for-tests") {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func whisperModelRank(path string) int {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(name, "large"):
		return 6
	case strings.Contains(name, "medium"):
		return 5
	case strings.Contains(name, "small"):
		return 4
	case strings.Contains(name, "base"):
		return 3
	case strings.Contains(name, "tiny"):
		return 2
	default:
		return 1
	}
}

func voiceCommandTimeout() time.Duration {
	if raw := envFirst("WECHAT_CLI_VOICE_TIMEOUT_SECONDS", "WX_MCP_VOICE_TIMEOUT_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 180 * time.Second
}

func cleanVoiceTranscriptText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "[BLANK_AUDIO]" || line == "[ Silence ]" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func voiceExtForData(data []byte) string {
	if bytes.Contains(data[:minInt(len(data), 16)], []byte("SILK_V3")) {
		return "silk"
	}
	switch {
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")):
		return "mp3"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("RIFF")):
		return "wav"
	default:
		return ""
	}
}

func voiceFormatName(v any) string {
	n, ok := integerArgValue(v)
	if !ok {
		return ""
	}
	switch n {
	case 4:
		return "silk"
	default:
		return fmt.Sprintf("format_%d", n)
	}
}

func quoteXMLReadHints(s *server, row wcdb.Row, p map[string]any) []map[string]any {
	refer, _ := p["refermsg"].(map[string]any)
	if refer == nil {
		return nil
	}
	refType := int64(0)
	if n, ok := integerArgValue(refer["type"]); ok {
		refType = n
	}
	parsed, _ := refer["content_parsed"].(map[string]any)
	if parsed == nil {
		return nil
	}
	refCreateTime := int64(0)
	if n, ok := integerArgValue(refer["createtime"]); ok {
		refCreateTime = n
	}
	if refCreateTime == 0 {
		refCreateTime = rowInt64(row, "create_time")
	}
	var hints []map[string]any
	switch refType {
	case 3:
		if h := s.directImageReadHintByContentMD5(stringMapValue(parsed, "md5"), refCreateTime); h != nil {
			addReferMsgHintContext(h, refer)
			hints = append(hints, h)
		}
		if h := imageXMLReadHint(parsed); h != nil {
			h["source"] = "message_refermsg"
			h["message_role"] = "referenced_message"
			addReferMsgHintContext(h, refer)
			hints = append(hints, h)
		}
	case 34:
		refRow := wcdb.Row{
			"talker":      firstNonEmpty(stringMapValue(refer, "chatusr"), rowString(row, "talker")),
			"create_time": refCreateTime,
			"base_kind":   int64(34),
			"kind_name":   "voice",
		}
		if sid, err := strconv.ParseInt(stringMapValue(refer, "svrid"), 10, 64); err == nil {
			refRow["server_id"] = sid
		}
		if h := voiceXMLReadHint(s, refRow, parsed, "message_refermsg", "referenced_message"); h != nil {
			addReferMsgHintContext(h, refer)
			hints = append(hints, h)
		}
	}
	if len(hints) > 0 {
		refer["media_read_hints"] = hints
	}
	return hints
}

func forwardXMLReadHints(s *server, row wcdb.Row, p map[string]any) []map[string]any {
	if s == nil {
		return nil
	}
	var hints []map[string]any
	var walk func([]wxparse.ForwardItem, []int)
	walk = func(items []wxparse.ForwardItem, prefix []int) {
		for i, it := range items {
			path := appendForwardPath(prefix, i)
			if it.DataType == 2 {
				if h := s.directImageReadHintByContentMD5(it.FullMD5, it.SrcMsgCreateTime); h != nil {
					h["source"] = "message_forward_item"
					h["message_role"] = "forward_item"
					h["forward_path"] = forwardPathString(path)
					h["resource_family"] = "image"
					h["content_md5"] = strings.ToLower(strings.TrimSpace(it.FullMD5))
					hints = append(hints, h)
				}
			}
			if len(it.NestedItems) > 0 {
				walk(it.NestedItems, path)
			}
		}
	}
	switch items := p["forward_items"].(type) {
	case []wxparse.ForwardItem:
		walk(items, nil)
	}
	return hints
}

func (s *server) directImageReadHintByContentMD5(contentMD5 string, createTime int64) map[string]any {
	if s == nil {
		return nil
	}
	paths := s.readableImageMatchesByMD5(createTime, contentMD5)
	if len(paths) == 0 {
		return nil
	}
	res := map[string]any{
		"resource_family": "image",
		"content_md5":     strings.ToLower(strings.TrimSpace(contentMD5)),
		"local_paths":     paths,
	}
	s.attachLocalPathAgentFields(res, "image", paths)
	_, _, hints := mediaResourceReadHints([]map[string]any{res})
	if len(hints) == 0 {
		return nil
	}
	h := hints[0]
	h["source"] = "message_refermsg"
	h["message_role"] = "referenced_message"
	h["content_md5"] = strings.ToLower(strings.TrimSpace(contentMD5))
	if ids, ok := h["resource_ids"].([]any); ok && len(ids) == 0 {
		delete(h, "resource_ids")
	}
	return h
}

func addReferMsgHintContext(h map[string]any, refer map[string]any) {
	ref := compactMap(map[string]any{
		"type":         refer["type"],
		"create_time":  refer["createtime"],
		"display_name": refer["displayname"],
		"fromusr":      refer["fromusr"],
		"chatusr":      refer["chatusr"],
		"server_id":    refer["svrid"],
	})
	if len(ref) > 0 {
		h["refermsg"] = ref
	}
}

func emojiXMLReadHint(p map[string]any) map[string]any {
	cdn := compactMap(map[string]any{
		"cdn_url":     stringMapValue(p, "cdn_url"),
		"encrypt_url": stringMapValue(p, "encrypt_url"),
	})
	h := compactMap(map[string]any{
		"source":          "message_xml",
		"address_type":    "wechat_cdn",
		"resource_family": "sticker",
		"direct_readable": false,
		"wechat_cdn":      cdn,
		"aeskey":          stringMapValue(p, "aeskey"),
		"md5":             stringMapValue(p, "md5"),
		"width":           p["width"],
		"height":          p["height"],
		"type":            p["type"],
	})
	if len(h) <= 4 && len(cdn) == 0 {
		return nil
	}
	return h
}

func compactMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case int:
			if x == 0 {
				continue
			}
		case int64:
			if x == 0 {
				continue
			}
		case map[string]any:
			if len(x) == 0 {
				continue
			}
		case []string:
			if len(x) == 0 {
				continue
			}
		case []map[string]any:
			if len(x) == 0 {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		}
		out[k] = v
	}
	return out
}

func stringMapValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapAny(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	default:
		return nil
	}
}

func stringSliceAny(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func mapSliceAny(v any) []map[string]any {
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func appendUniquePathDetails(vals []map[string]any, extra ...map[string]any) []map[string]any {
	seen := map[string]bool{}
	for _, v := range vals {
		if p, ok := v["path"].(string); ok && p != "" {
			seen[p] = true
		}
	}
	for _, v := range extra {
		p, _ := v["path"].(string)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		vals = append(vals, v)
	}
	return vals
}

func appendUniqueAny(vals []any, extra any) []any {
	key := fmt.Sprint(extra)
	for _, v := range vals {
		if fmt.Sprint(v) == key {
			return vals
		}
	}
	return append(vals, extra)
}

func (s *server) localMediaPaths(talker string, createTime int64, family, md5Value string, fileNames []string) []string {
	if s == nil || s.cfg == nil || s.cfg.DBRoot == "" || createTime == 0 {
		return nil
	}
	month := time.Unix(createTime, 0).Format("2006-01")
	var candidates []string
	if md5Value != "" {
		switch family {
		case "image", "cover":
			candidates = append(candidates, s.readableImageMatchesByMD5(createTime, md5Value)...)
			base := filepath.Join(s.cfg.DBRoot, "msg", "attach", talkerHash(talker), month, "Img")
			candidates = append(candidates,
				filepath.Join(base, md5Value+".dat"),
				filepath.Join(base, md5Value+"_h.dat"),
				filepath.Join(base, md5Value+"_t.dat"),
			)
		case "video":
			base := filepath.Join(s.cfg.DBRoot, "msg", "video", month)
			candidates = append(candidates,
				filepath.Join(base, md5Value+".mp4"),
				filepath.Join(base, md5Value+"_thumb.jpg"),
			)
		}
	}
	if family == "file" {
		base := filepath.Join(s.cfg.DBRoot, "msg", "file", month)
		for _, name := range fileNames {
			if safeLeafName(name) {
				candidates = append(candidates, filepath.Join(base, name))
			}
		}
	}
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, p := range candidates {
		if p == "" || seen[p] {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}

type readableImageMatch struct {
	path    string
	timeGap int64
}

type readableImageCandidate struct {
	path    string
	modTime int64
}

func (s *server) readableImageMatchesByMD5(createTime int64, md5Value string) []string {
	if s == nil || s.cfg == nil || s.cfg.DBRoot == "" {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(md5Value))
	if !md5LikeRe.MatchString(target) {
		return nil
	}
	candidates := s.readableImageTempIndex()[target]
	if len(candidates) == 0 {
		return nil
	}
	matches := make([]readableImageMatch, 0, len(candidates))
	for _, cand := range candidates {
		gap := int64(0)
		if createTime != 0 {
			gap = cand.modTime - createTime
			if gap < 0 {
				gap = -gap
			}
		}
		matches = append(matches, readableImageMatch{path: cand.path, timeGap: gap})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].timeGap != matches[j].timeGap {
			return matches[i].timeGap < matches[j].timeGap
		}
		return matches[i].path < matches[j].path
	})
	return []string{matches[0].path}
}

func (s *server) readableImageTempIndex() map[string][]readableImageCandidate {
	root := filepath.Join(s.cfg.DBRoot, "temp")
	s.readableImageIndexMu.Lock()
	defer s.readableImageIndexMu.Unlock()
	if s.readableImageIndexRoot == root && s.readableImageIndex != nil && time.Since(s.readableImageIndexBuiltAt) < 5*time.Second {
		return s.readableImageIndex
	}
	index := map[string][]readableImageCandidate{}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		s.readableImageIndexRoot = root
		s.readableImageIndexBuiltAt = time.Now()
		s.readableImageIndex = index
		return index
	}
	const maxCandidateFiles = 2048
	const maxCandidateSize = 50 << 20
	var scanned int
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !directImageExt(ext) {
			return nil
		}
		scanned++
		if scanned > maxCandidateFiles {
			return filepath.SkipAll
		}
		st, err := d.Info()
		if err != nil || st.Size() <= 0 || st.Size() > maxCandidateSize {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sum := md5.Sum(b)
		key := hex.EncodeToString(sum[:])
		index[key] = append(index[key], readableImageCandidate{path: path, modTime: st.ModTime().Unix()})
		return nil
	})
	s.readableImageIndexRoot = root
	s.readableImageIndexBuiltAt = time.Now()
	s.readableImageIndex = index
	return index
}

func (s *server) resolveLooseChatArg(a map[string]any) (string, error) {
	raw := strings.TrimSpace(firstNonEmpty(getStr(a, "talker"), getStr(a, "chat")))
	if raw == "" || looksLikeRawChatID(raw) {
		return raw, nil
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		return "", fmt.Errorf("chat %q requires cache index for display-name resolution; run `wechat-cli cache refresh` first or pass raw talker/wxid", raw)
	}
	defer db.Close()
	return resolveTalkerForCache(db, map[string]any{"chat": raw}, true)
}

func (s *server) resolveLooseSenderArg(a map[string]any) (string, error) {
	raw := strings.TrimSpace(getStr(a, "sender"))
	if raw == "" || looksLikeRawChatID(raw) {
		return raw, nil
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		return "", fmt.Errorf("sender %q requires cache index for display-name resolution; run `wechat-cli cache refresh` first or pass raw sender wxid", raw)
	}
	defer db.Close()
	cands, err := resolveChatCandidates(db, raw, "", 1)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("sender %q not found; pass raw sender wxid", raw)
	}
	return cands[0].Username, nil
}

func mediaKindWhere(kind, alias string) (string, []any, error) {
	col := alias + ".message_local_type"
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "image":
		return col + " = ?", []any{int64(3)}, nil
	case "voice":
		return col + " = ?", []any{int64(34)}, nil
	case "video":
		return col + " = ?", []any{int64(43)}, nil
	case "sticker", "emoji":
		return col + " = ?", []any{int64(47)}, nil
	case "app":
		return "(" + col + " & 4294967295) = ?", []any{int64(49)}, nil
	case "file":
		return col + " IN (?, ?, ?)", []any{packedLocalType(49, 6), packedLocalType(49, 8), packedLocalType(49, 24)}, nil
	case "forward_chat":
		return col + " = ?", []any{packedLocalType(49, 19)}, nil
	case "miniprogram":
		return col + " IN (?, ?)", []any{packedLocalType(49, 33), packedLocalType(49, 36)}, nil
	case "channel_video":
		return col + " = ?", []any{packedLocalType(49, 51)}, nil
	case "announcement":
		return col + " = ?", []any{packedLocalType(49, 87)}, nil
	default:
		return "", nil, fmt.Errorf("unsupported media_resources kind_name/type %q", kind)
	}
}

func messageKindWhere(kind, col string) (string, []any, error) {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "text":
		return "(" + col + " & 4294967295) = ?", []any{int64(1)}, nil
	case "image":
		return "(" + col + " & 4294967295) = ?", []any{int64(3)}, nil
	case "voice":
		return "(" + col + " & 4294967295) = ?", []any{int64(34)}, nil
	case "card":
		return "(" + col + " & 4294967295) = ?", []any{int64(42)}, nil
	case "video":
		return "(" + col + " & 4294967295) = ?", []any{int64(43)}, nil
	case "sticker", "emoji":
		return "(" + col + " & 4294967295) = ?", []any{int64(47)}, nil
	case "location":
		return "(" + col + " & 4294967295) = ?", []any{int64(48)}, nil
	case "app":
		return "(" + col + " & 4294967295) = ?", []any{int64(49)}, nil
	case "voip":
		return "(" + col + " & 4294967295) = ?", []any{int64(50)}, nil
	case "system":
		return "(" + col + " & 4294967295) = ?", []any{int64(10000)}, nil
	case "music":
		return col + " = ?", []any{packedLocalType(49, 3)}, nil
	case "link":
		return col + " IN (?, ?)", []any{packedLocalType(49, 5), packedLocalType(49, 49)}, nil
	case "file":
		return col + " IN (?, ?, ?)", []any{packedLocalType(49, 6), packedLocalType(49, 8), packedLocalType(49, 24)}, nil
	case "forward_chat":
		return col + " = ?", []any{packedLocalType(49, 19)}, nil
	case "miniprogram":
		return col + " IN (?, ?)", []any{packedLocalType(49, 33), packedLocalType(49, 36)}, nil
	case "channel_video":
		return col + " = ?", []any{packedLocalType(49, 51)}, nil
	case "quote":
		return col + " = ?", []any{packedLocalType(49, 57)}, nil
	case "pat":
		return col + " = ?", []any{packedLocalType(49, 62)}, nil
	case "announcement":
		return col + " = ?", []any{packedLocalType(49, 87)}, nil
	case "transfer":
		return col + " = ?", []any{packedLocalType(49, 2000)}, nil
	case "red_packet":
		return col + " = ?", []any{packedLocalType(49, 2001)}, nil
	default:
		return "", nil, fmt.Errorf("unsupported message kind_name/type %q", kind)
	}
}

func packedLocalType(baseKind, subtype int32) int64 {
	return int64(uint32(baseKind)) | (int64(subtype) << 32)
}

func resourceDetailWhere(a map[string]any, alias string) (string, []any, error) {
	var where []string
	var args []any
	if raw, ok, err := int64Arg(a, "resource_type_raw"); err != nil {
		return "", nil, err
	} else if ok {
		where = append(where, alias+".type = ?")
		args = append(args, raw)
	}
	if fam := strings.TrimSpace(strings.ToLower(getStr(a, "resource_family"))); fam != "" {
		var code int64
		switch fam {
		case "image":
			code = 1
		case "video":
			code = 2
		case "file":
			code = 3
		case "cover":
			code = 4
		case "unknown":
			code = 0
		default:
			return "", nil, fmt.Errorf("unsupported resource_family %q", fam)
		}
		if code == 0 {
			where = append(where, "("+alias+".type & 65535) NOT IN (1, 2, 3, 4)")
		} else {
			where = append(where, "("+alias+".type & 65535) = ?")
			args = append(args, code)
		}
	}
	return strings.Join(where, " AND "), args, nil
}

func mediaResourceFamily(rawType int64, baseKind int32, kindName string) string {
	switch rawType & 0xFFFF {
	case 1:
		return "image"
	case 2:
		return "video"
	case 3:
		return "file"
	case 4:
		return "cover"
	}
	switch {
	case baseKind == 3:
		return "image"
	case baseKind == 34:
		return "voice"
	case baseKind == 43:
		return "video"
	case kindName == "file":
		return "file"
	}
	return "unknown"
}

func rowBytes(r wcdb.Row, key string) []byte {
	if v, ok := r[key]; ok {
		switch x := v.(type) {
		case []byte:
			return x
		case string:
			return []byte(x)
		}
	}
	return nil
}

var md5LikeRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func firstMD5(vals []string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if md5LikeRe.MatchString(v) {
			return strings.ToLower(v)
		}
	}
	return ""
}

func fileNameStrings(vals []string) []string {
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || md5LikeRe.MatchString(v) || !safeLeafName(v) {
			continue
		}
		out = append(out, v)
	}
	return uniqueStrings(out)
}

func safeLeafName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	if strings.Contains(name, "\x00") || strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name
}

func packedStrings(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	var walk func([]byte, int)
	walk = func(buf []byte, depth int) {
		if depth > 4 || len(buf) == 0 {
			return
		}
		for i := 0; i < len(buf); {
			key, n, ok := readProtoVarint(buf, i)
			if !ok || key == 0 {
				return
			}
			i += n
			wire := key & 0x7
			switch wire {
			case 0:
				_, n, ok := readProtoVarint(buf, i)
				if !ok {
					return
				}
				i += n
			case 1:
				if i+8 > len(buf) {
					return
				}
				i += 8
			case 2:
				ln, n, ok := readProtoVarint(buf, i)
				if !ok {
					return
				}
				i += n
				if ln > uint64(len(buf)-i) {
					return
				}
				field := buf[i : i+int(ln)]
				i += int(ln)
				if s, ok := printablePackedString(field); ok {
					out = append(out, s)
				}
				walk(field, depth+1)
			case 5:
				if i+4 > len(buf) {
					return
				}
				i += 4
			default:
				return
			}
		}
	}
	walk(data, 0)
	return uniqueStrings(out)
}

func readProtoVarint(buf []byte, off int) (uint64, int, bool) {
	var x uint64
	var shift uint
	for i := off; i < len(buf) && i < off+10; i++ {
		b := buf[i]
		x |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return x, i - off + 1, true
		}
		shift += 7
	}
	return 0, 0, false
}

func printablePackedString(buf []byte) (string, bool) {
	if len(buf) == 0 || len(buf) > 512 || !utf8.Valid(buf) {
		return "", false
	}
	s := strings.TrimSpace(string(buf))
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return "", false
		}
	}
	return s, true
}

func uniqueStrings(vals []string) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	seen := map[string]bool{}
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func appendUniqueStrings(vals []string, extra ...string) []string {
	if len(extra) == 0 {
		return vals
	}
	seen := make(map[string]bool, len(vals)+len(extra))
	for _, v := range vals {
		if v != "" {
			seen[v] = true
		}
	}
	for _, v := range extra {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		vals = append(vals, v)
	}
	return vals
}

func uniqueInt64s(vals []int64) []int64 {
	if len(vals) == 0 {
		return nil
	}
	out := make([]int64, 0, len(vals))
	seen := map[int64]bool{}
	for _, v := range vals {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func (s *server) toolGroupMembers(a map[string]any) (any, error) {
	target := getStr(a, "chatroom_id")
	if target == "" {
		target = getStr(a, "chat")
	}
	if target == "" {
		return nil, fmt.Errorf("chatroom_id or chat is required")
	}
	if !looksLikeRawChatID(target) {
		if cdb, err := s.openCacheIndex(false); err == nil {
			cp := map[string]any{"chat": target, "type_filter": "group"}
			resolved, rerr := resolveTalkerForCache(cdb, cp, true)
			cdb.Close()
			if rerr != nil {
				return nil, rerr
			}
			target = resolved
		} else if errors.Is(err, errCacheMissing) {
			return nil, fmt.Errorf("chat %q requires cache index for group-name resolution; run `wechat-cli cache refresh` first or pass raw chatroom_id", target)
		} else {
			return nil, err
		}
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT c.username, c.alias, c.remark, c.nick_name,
		COALESCE(NULLIF(c.remark, ''), NULLIF(c.nick_name, ''), c.username) AS display_name,
		CASE WHEN cr.owner = c.username THEN 1 ELSE 0 END AS is_owner,
		CASE WHEN c.local_type = 1 THEN 1 ELSE 0 END AS is_friend
		FROM chat_room cr
		JOIN chatroom_member cm ON cm.room_id = cr.id
		JOIN contact c ON c.id = cm.member_id
		WHERE cr.username = ?
		ORDER BY COALESCE(NULLIF(c.remark, ''), c.nick_name, c.username)
		LIMIT ? OFFSET ?`, target, getInt(a, "limit", 100), getInt(a, "offset", 0))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if v, ok := r["is_owner"].(int64); ok {
			r["is_owner"] = v != 0
		}
		if v, ok := r["is_friend"].(int64); ok {
			r["is_friend"] = v != 0
		}
		for _, k := range []string{"alias", "remark"} {
			if v, ok := r[k].(string); ok && v == "" {
				delete(r, k)
			}
		}
	}
	if !getBool(a, "stats") {
		return rows, nil
	}
	tableName := "Msg_" + talkerHash(target)
	shards, err := s.findMsgDBs(tableName)
	if err != nil {
		return nil, fmt.Errorf("stats=true 失败 (%s): %w", tableName, err)
	}
	defer closeMsgDBs(shards)
	counts := make(map[string]int64)
	for _, shard := range shards {
		n2i, err := loadName2Id(shard.DB)
		if err != nil {
			return nil, fmt.Errorf("stats=true 失败 (%s loadName2Id): %w", shard.Name, err)
		}
		countRows, err := shard.DB.Query(fmt.Sprintf(
			"SELECT real_sender_id, COUNT(*) AS cnt FROM %s GROUP BY real_sender_id", tableName))
		if err != nil {
			return nil, fmt.Errorf("stats=true 失败 (%s count query): %w", shard.Name, err)
		}
		for _, r := range countRows {
			id, _ := r["real_sender_id"].(int64)
			cnt, _ := r["cnt"].(int64)
			if w, ok := n2i[id]; ok {
				counts[w] += cnt
			}
		}
	}
	for _, row := range rows {
		if u, ok := row["username"].(string); ok {
			row["msg_count"] = counts[u]
		}
	}
	return rows, nil
}

func (s *server) toolSns(a map[string]any) (any, error) {
	db, err := s.openDB("sns", "sns.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	limit := getInt(a, "limit", 20)
	offset := getInt(a, "offset", 0)
	afterTS, err := parseTS(getStr(a, "after"))
	if err != nil {
		return nil, err
	}
	beforeTS, err := parseTS(getStr(a, "before"))
	if err != nil {
		return nil, err
	}

	var where []string
	var args []any
	if u := getStr(a, "user"); u != "" {
		where = append(where, "user_name = ?")
		args = append(args, u)
	}
	if kw := getStr(a, "keyword"); kw != "" {
		where = append(where, "content LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	fetchLimit := limit + offset
	if afterTS > 0 || beforeTS > 0 {
		fetchLimit *= 4
	}
	if fetchLimit > 2000 {
		fetchLimit = 2000
	}

	rows, err := db.Query(
		fmt.Sprintf("SELECT tid, user_name, content FROM SnsTimeLine %s ORDER BY tid DESC LIMIT %d", wc, fetchLimit),
		args...)
	if err != nil {
		return nil, err
	}

	var posts []*snsPost
	var tids []int64
	skip := offset
	for _, r := range rows {
		raw, _ := r["content"].(string)
		p, perr := parseSnsXML(raw)
		if perr != nil {
			// Surface parser drift instead of silently skipping. Counts toward
			// limit so the agent sees the failure in the same window it asked for.
			posts = append(posts, &snsPost{ParseError: perr.Error()})
			if len(posts) >= limit {
				break
			}
			continue
		}
		if p == nil {
			continue
		}
		if afterTS > 0 && p.CreateTime < afterTS {
			continue
		}
		if beforeTS > 0 && p.CreateTime >= beforeTS {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		tid, _ := r["tid"].(int64)
		tids = append(tids, tid)
		posts = append(posts, p)
		if len(posts) >= limit {
			break
		}
	}

	if len(posts) > 0 {
		likes, comments := loadSnsInteractions(db, tids)
		// Assign by walking posts in order, advancing tid index only on valid
		// posts. Parallel-slice indexing breaks when a parse-error post slips
		// into `posts` without a corresponding tid in `tids` — would attach
		// likes/comments to the wrong post (silent SNS data corruption).
		ti := 0
		for _, p := range posts {
			if p.ParseError != "" {
				continue
			}
			if ti >= len(tids) {
				break
			}
			tid := tids[ti]
			p.Likes = likes[tid]
			p.Comments = comments[tid]
			ti++
		}
		s.attachSnsAvatars(posts)
	}
	return posts, nil
}

func (s *server) toolSnsFeed(a map[string]any) (any, error) {
	return s.toolSns(a)
}

func (s *server) toolSnsSearch(a map[string]any) (any, error) {
	if getStr(a, "keyword") == "" {
		return nil, fmt.Errorf("keyword is required")
	}
	return s.toolSns(a)
}

func (s *server) toolSnsNotifications(a map[string]any) (any, error) {
	db, err := s.openDB("sns", "sns.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if !getBool(a, "include_read") {
		where = append(where, "is_unread != 0")
	}
	if t := getStr(a, "after"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "create_time >= ?")
		args = append(args, ts)
	}
	if t := getStr(a, "before"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "create_time < ?")
		args = append(args, ts)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	limit := getInt(a, "limit", 50)
	args = append(args, limit)
	rows, err := db.Query(fmt.Sprintf(`SELECT local_id, create_time, type, feed_id, is_unread,
		from_username, from_nickname, to_username, to_nickname, content
		FROM SnsMessage_tmp3 %s ORDER BY create_time DESC, local_id DESC LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	feedIDs := make(map[int64]bool)
	for _, r := range rows {
		if fid := rowInt64(r, "feed_id"); fid != 0 {
			feedIDs[fid] = true
		}
		typ := rowInt64(r, "type")
		if typ == 1 {
			r["notification_type"] = "like"
		} else {
			r["notification_type"] = "comment"
		}
		r["is_unread"] = rowInt64(r, "is_unread") != 0
	}
	feeds := s.loadSnsFeedPreview(db, feedIDs)
	for _, r := range rows {
		if f, ok := feeds[rowInt64(r, "feed_id")]; ok {
			r["feed_author_username"] = f.Username
			r["feed_author"] = f.Nickname
			r["feed_preview"] = f.Content
		}
	}
	return rows, nil
}

func (s *server) loadSnsFeedPreview(db *wcdb.DB, ids map[int64]bool) map[int64]*snsPost {
	out := map[int64]*snsPost{}
	if len(ids) == 0 {
		return out
	}
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for id := range ids {
		ph = append(ph, "?")
		args = append(args, id)
	}
	rows, err := db.Query(fmt.Sprintf("SELECT tid, content FROM SnsTimeLine WHERE tid IN (%s)", strings.Join(ph, ",")), args...)
	if err != nil {
		return out
	}
	for _, r := range rows {
		tid := rowInt64(r, "tid")
		p, err := parseSnsXML(rowString(r, "content"))
		if err != nil || p == nil {
			continue
		}
		out[tid] = p
	}
	return out
}

func (s *server) toolSearch(a map[string]any) (any, error) {
	kw := getStr(a, "keyword")
	if kw == "" {
		return nil, fmt.Errorf("keyword is required")
	}
	switch mode := searchMode(a); mode {
	case "fts", "like", "auto":
	default:
		return nil, fmt.Errorf("invalid search_mode=%q: must be fts / like / auto", mode)
	}
	talker, err := s.resolveLooseChatArg(a)
	if err != nil {
		return nil, err
	}
	sender := ""
	if getStr(a, "sender") != "" {
		sender, err = s.resolveLooseSenderArg(a)
		if err != nil {
			return nil, err
		}
	}
	limit := getInt(a, "limit", 20)
	like := "%" + kw + "%"

	// search_mode is kept for compatibility, but all modes use WeChat's live
	// FTS content DB. wechat-cli intentionally does not globally scan every Msg_*
	// table for substring search.
	db, err := s.openDB("message", "message_fts.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Build session_id → talker mapping from FTS name2id.
	n2iRows, err := db.Query("SELECT rowid AS rid, username FROM name2id")
	if err != nil {
		return nil, fmt.Errorf("search 失败 (name2id): %w", err)
	}
	idToTalker := make(map[int64]string)
	talkerToID := make(map[string]int64)
	for _, r := range n2iRows {
		if rid, ok := r["rid"].(int64); ok {
			if u, ok := r["username"].(string); ok {
				idToTalker[rid] = u
				talkerToID[u] = rid
			}
		}
	}
	var sessionID int64
	if talker != "" {
		var ok bool
		sessionID, ok = talkerToID[talker]
		if !ok {
			return []wcdb.Row{}, nil
		}
	}
	subWhere := []string{"c0 LIKE ?"}
	subArgs := []any{like}
	if sessionID != 0 {
		subWhere = append(subWhere, "c4 = ?")
		subArgs = append(subArgs, sessionID)
	}
	if s := getStr(a, "after"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, err
		}
		subWhere = append(subWhere, "c6 >= ?")
		subArgs = append(subArgs, ts)
	}
	if s := getStr(a, "before"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, err
		}
		subWhere = append(subWhere, "c6 < ?")
		subArgs = append(subArgs, ts)
	}
	whereSQL := strings.Join(subWhere, " AND ")
	fetchLimit := limit
	if searchNeedsPostFilter(a) {
		fetchLimit = limit * 20
		if fetchLimit < 200 {
			fetchLimit = 200
		}
		if fetchLimit > 5000 {
			fetchLimit = 5000
		}
	}

	// UNION ALL across 4 FTS content partitions then global ORDER BY.
	// Previous impl looped 0..3 and early-stopped when len(results) >= limit,
	// which could miss newer messages living in later partitions.
	// c0=text, c1=local_id, c2=sort_seq, c4=session_id, c6=create_time
	query := `SELECT * FROM (
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_0_content WHERE ` + whereSQL + `
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_1_content WHERE ` + whereSQL + `
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_2_content WHERE ` + whereSQL + `
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_3_content WHERE ` + whereSQL + `
	) ORDER BY create_time DESC LIMIT ?`
	var qargs []any
	for i := 0; i < 4; i++ {
		qargs = append(qargs, subArgs...)
	}
	qargs = append(qargs, fetchLimit)
	rows, err := db.Query(query, qargs...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		sid, _ := r["session_id"].(int64)
		r["talker"] = idToTalker[sid]
		delete(r, "session_id")
	}
	s.enrichSearchSender(rows)
	s.attachDisplayNames(rows,
		[2]string{"talker", "talker_display_name"},
		[2]string{"sender_wxid", "sender_display_name"})
	decorateMessageSearchRows(rows)
	rows = filterLiveSearchRows(rows, a, sender)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func searchMode(a map[string]any) string {
	mode := getStr(a, "search_mode")
	if mode == "" {
		return "fts"
	}
	return mode
}

func searchNeedsPostFilter(a map[string]any) bool {
	return firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type"), getStr(a, "sender")) != "" ||
		getInt(a, "base_kind", 0) != 0
}

func filterLiveSearchRows(rows []wcdb.Row, a map[string]any, sender string) []wcdb.Row {
	if !searchNeedsPostFilter(a) {
		return rows
	}
	baseKind := getInt(a, "base_kind", 0)
	kind := firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type"))
	out := rows[:0]
	for _, r := range rows {
		if sender != "" && rowString(r, "sender_wxid") != sender {
			continue
		}
		if baseKind != 0 && rowInt64(r, "base_kind") != int64(baseKind) {
			continue
		}
		if kind != "" && !messageKindNameMatches(kind, rowString(r, "kind_name"), rowInt64(r, "base_kind")) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func messageKindNameMatches(want, got string, baseKind int64) bool {
	want = strings.TrimSpace(strings.ToLower(want))
	got = strings.TrimSpace(strings.ToLower(got))
	switch want {
	case "emoji":
		want = "sticker"
	case "app":
		return baseKind == 49
	case "text":
		return baseKind == 1
	case "image":
		return baseKind == 3
	case "voice":
		return baseKind == 34
	case "card":
		return baseKind == 42
	case "video":
		return baseKind == 43
	case "sticker":
		return baseKind == 47
	case "location":
		return baseKind == 48
	case "voip":
		return baseKind == 50
	case "system":
		return baseKind == 10000
	}
	return got == want
}

// enrichSearchSender resolves sender_wxid + base_kind + kind_name for FTS
// search hits by joining each (talker, local_id) back to its Msg_<hash> shard.
// Groups rows by talker → one IN query per talker; missing rows leave the
// fields absent so caller can distinguish "not enriched" from "no value".
func (s *server) enrichSearchSender(rows []wcdb.Row) {
	byTalker := make(map[string][]int64)
	for _, r := range rows {
		t, _ := r["talker"].(string)
		lid, _ := r["local_id"].(int64)
		if t == "" || lid == 0 {
			continue
		}
		byTalker[t] = append(byTalker[t], lid)
	}
	type meta struct {
		senderWxid string
		baseKind   int32
		kindName   string
	}
	metaByKey := make(map[string]meta)
	for talker, lids := range byTalker {
		tableName := "Msg_" + talkerHash(talker)
		shards, err := s.findMsgDBs(tableName)
		if err != nil {
			continue
		}
		ph := make([]string, len(lids))
		args := make([]any, len(lids))
		for i, lid := range lids {
			ph[i] = "?"
			args[i] = lid
		}
		for _, shard := range shards {
			n2i, _ := loadName2Id(shard.DB)
			metaRows, qerr := shard.DB.Query(fmt.Sprintf(
				"SELECT local_id, real_sender_id, local_type FROM %s WHERE local_id IN (%s)",
				tableName, strings.Join(ph, ",")), args...)
			if qerr != nil {
				continue
			}
			for _, mr := range metaRows {
				lid, _ := mr["local_id"].(int64)
				rsid, _ := mr["real_sender_id"].(int64)
				lt, _ := mr["local_type"].(int64)
				bk, _, name := wxkind.Unpack(lt)
				m := meta{baseKind: bk, kindName: name}
				if w, ok := n2i[rsid]; ok {
					m.senderWxid = w
				}
				metaByKey[talker+":"+strconv.FormatInt(lid, 10)] = m
			}
		}
		closeMsgDBs(shards)
	}
	for _, r := range rows {
		t, _ := r["talker"].(string)
		lid, _ := r["local_id"].(int64)
		if m, ok := metaByKey[t+":"+strconv.FormatInt(lid, 10)]; ok {
			if m.senderWxid != "" {
				r["sender_wxid"] = m.senderWxid
			}
			r["base_kind"] = m.baseKind
			r["kind_name"] = m.kindName
		}
	}
}

func (s *server) toolSQL(a map[string]any) (any, error) {
	q := getStr(a, "query")
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	q, err := boundedReadSQL(q, getInt(a, "limit", 200))
	if err != nil {
		return nil, err
	}
	subdir := getStr(a, "subdir")
	if subdir == "" {
		subdir = "session"
	}
	file := getStr(a, "file")
	if file == "" {
		file = "session.db"
	}
	db, err := s.openDB(subdir, file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.Query(q)
}

func (s *server) toolTransfers(a map[string]any) (any, error) {
	db, err := s.openDB("general", "general.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if t := getStr(a, "after"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "begin_transfer_time >= ?")
		args = append(args, ts)
	}
	if t := getStr(a, "before"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "begin_transfer_time < ?")
		args = append(args, ts)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, getInt(a, "limit", 50))
	rows, err := db.Query(fmt.Sprintf(`SELECT transfer_id, transcation_id,
		session_name AS session_username,
		pay_payer AS payer_wxid, pay_receiver AS receiver_wxid, pay_sub_type,
		begin_transfer_time, invalid_time, last_modified_time,
		message_server_id
		FROM transferTable %s
		ORDER BY begin_transfer_time DESC
		LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	contents := s.fetchMessageContent(rows, "message_server_id", "session_username")
	for _, r := range rows {
		sid, _ := r["message_server_id"].(int64)
		c, ok := contents[sid]
		if !ok {
			continue
		}
		amount, des, memo, err := wxparse.TransferInfo(c)
		if err != nil {
			r["parse_error"] = err.Error()
			continue
		}
		if amount != "" {
			r["amount"] = amount
		}
		if des != "" {
			r["description"] = des
		}
		if memo != "" {
			r["memo"] = memo
		}
	}
	s.attachDisplayNames(rows,
		[2]string{"payer_wxid", "payer_display_name"},
		[2]string{"receiver_wxid", "receiver_display_name"},
		[2]string{"session_username", "session_display_name"})
	return rows, nil
}

func (s *server) toolRedPackets(a map[string]any) (any, error) {
	limit := getInt(a, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	afterTS, err := parseTS(getStr(a, "after"))
	if err != nil {
		return nil, err
	}
	beforeTS, err := parseTS(getStr(a, "before"))
	if err != nil {
		return nil, err
	}
	var cacheDB *wcdb.DB
	openCache := func() (*wcdb.DB, error) {
		if cacheDB != nil {
			return cacheDB, nil
		}
		db, err := s.openCacheIndex(false)
		if err != nil {
			return nil, err
		}
		cacheDB = db
		return cacheDB, nil
	}
	defer func() {
		if cacheDB != nil {
			cacheDB.Close()
		}
	}()
	talker := getStr(a, "talker")
	if talker == "" && getStr(a, "chat") != "" {
		cdb, err := openCache()
		if err != nil {
			return nil, fmt.Errorf("chat filter requires cache index; run `wechat-cli cache refresh` first: %w", err)
		}
		resolved, err := resolveTalkerForCache(cdb, a, true)
		if err != nil {
			return nil, err
		}
		talker = resolved
	}
	senderFilter := getStr(a, "sender")
	if senderFilter != "" && !looksLikeRawChatID(senderFilter) {
		cdb, err := openCache()
		if err != nil {
			return nil, fmt.Errorf("sender filter requires cache index; run `wechat-cli cache refresh` first: %w", err)
		}
		cands, err := resolveChatCandidates(cdb, senderFilter, "", 1)
		if err != nil {
			return nil, err
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("sender %q not found; call resolve_chat first to inspect candidates", senderFilter)
		}
		senderFilter = cands[0].Username
	}
	needsMessageMeta := afterTS > 0 || beforeTS > 0
	db, err := s.openDB("general", "general.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if talker != "" {
		where = append(where, "session_name = ?")
		args = append(args, talker)
	}
	if senderFilter != "" {
		where = append(where, "sender_user_name = ?")
		args = append(args, senderFilter)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	fetchLimit := limit
	if needsMessageMeta {
		fetchLimit = 50000
	}
	args = append(args, fetchLimit)
	rows, err := db.Query(fmt.Sprintf(`SELECT send_id,
			sender_user_name AS sender_wxid,
			session_name AS session_username,
			native_url, message_server_id
			FROM redEnvelopeTable %s
			ORDER BY rowid DESC
			LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	if needsMessageMeta {
		meta := s.liveMessageMeta(rows, "message_server_id", "session_username")
		filtered := rows[:0]
		for _, r := range rows {
			if m, ok := meta[messagePairKey(rowString(r, "session_username"), rowInt64(r, "message_server_id"))]; ok {
				r["create_time"] = rowInt64(m, "create_time")
				r["create_time_human"] = rowString(m, "create_time_human")
				if sw := rowString(m, "sender_wxid"); sw != "" {
					r["message_sender_wxid"] = sw
				}
				if sd := rowString(m, "sender_display_name"); sd != "" {
					r["message_sender_display_name"] = sd
				}
			}
			ct := rowInt64(r, "create_time")
			if (afterTS > 0 || beforeTS > 0) && ct == 0 {
				continue
			}
			if afterTS > 0 && ct < afterTS {
				continue
			}
			if beforeTS > 0 && ct >= beforeTS {
				continue
			}
			filtered = append(filtered, r)
		}
		sort.Slice(filtered, func(i, j int) bool {
			if rowInt64(filtered[i], "create_time") != rowInt64(filtered[j], "create_time") {
				return rowInt64(filtered[i], "create_time") > rowInt64(filtered[j], "create_time")
			}
			return rowInt64(filtered[i], "message_server_id") > rowInt64(filtered[j], "message_server_id")
		})
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
		rows = filtered
	}
	contents := s.fetchMessageContent(rows, "message_server_id", "session_username")
	for _, r := range rows {
		sid, _ := r["message_server_id"].(int64)
		c, ok := contents[sid]
		if !ok {
			continue
		}
		wishing, sceneText, err := wxparse.RedPacketInfo(c)
		if err != nil {
			r["parse_error"] = err.Error()
			continue
		}
		if wishing != "" {
			r["wishing"] = wishing
		}
		if sceneText != "" {
			r["scene_text"] = sceneText
		}
	}
	s.attachDisplayNames(rows,
		[2]string{"sender_wxid", "sender_display_name"},
		[2]string{"session_username", "session_display_name"})
	return rows, nil
}

func (s *server) toolFavorites(a map[string]any) (any, error) {
	db, err := s.openDB("favorite", "favorite.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if t := getStr(a, "after"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "update_time >= ?")
		args = append(args, ts)
	}
	if t := getStr(a, "before"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "update_time < ?")
		args = append(args, ts)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, getInt(a, "limit", 50))
	rows, err := db.Query(fmt.Sprintf(`SELECT server_id,
		type AS type_id, update_time, source_id, content,
		fromusr AS from_wxid,
		realchatname AS source_chat_username
		FROM fav_db_item %s
		ORDER BY update_time DESC
		LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		ti, _ := r["type_id"].(int64)
		r["favorite_type"] = wxkind.FavKind(ti)
		delete(r, "type_id")
		if c, ok := r["content"].(string); ok && c != "" {
			title, desc, url, perr := wxparse.FavoriteInfo(c)
			if perr != nil {
				r["parse_error"] = perr.Error()
			} else if title != "" || desc != "" || url != "" {
				if title != "" {
					r["title"] = title
				}
				if desc != "" {
					r["description"] = desc
				}
				if url != "" {
					r["url"] = url
				}
			}
		}
		for _, k := range []string{"source_chat_username"} {
			if v, ok := r[k].(string); ok && v == "" {
				delete(r, k)
			}
		}
	}
	s.attachDisplayNames(rows,
		[2]string{"from_wxid", "from_display_name"},
		[2]string{"source_chat_username", "source_chat_display_name"})
	return rows, nil
}

func (s *server) toolChatroomAnnouncements(a map[string]any) (any, error) {
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	limit := getInt(a, "limit", 20)
	var where []string
	var args []any
	if cid := getStr(a, "chatroom_id"); cid != "" {
		where = append(where, "username_ = ?")
		args = append(args, cid)
	} else {
		where = append(where, "announcement_ IS NOT NULL AND announcement_ != ''")
	}
	if t := getStr(a, "after"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "announcement_publish_time_ >= ?")
		args = append(args, ts)
	}
	if t := getStr(a, "before"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "announcement_publish_time_ < ?")
		args = append(args, ts)
	}
	args = append(args, limit)
	rows, err := db.Query(fmt.Sprintf(`SELECT username_ AS chatroom_id,
		announcement_ AS announcement,
		announcement_editor_ AS editor_wxid,
		announcement_publish_time_ AS publish_time
		FROM chat_room_info_detail
		WHERE %s
		ORDER BY announcement_publish_time_ DESC
		LIMIT ?`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	s.attachDisplayNames(rows,
		[2]string{"chatroom_id", "chatroom_display_name"},
		[2]string{"editor_wxid", "editor_display_name"})
	return rows, nil
}

func (s *server) toolSchema(a map[string]any) (any, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	subdir := getStr(a, "subdir")
	file := getStr(a, "file")
	if subdir != "" && file != "" {
		db, err := s.openDB(subdir, file)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return db.Query(`SELECT name, sql FROM sqlite_master
			WHERE type='table' AND name NOT LIKE 'sqlite_%'
			ORDER BY name`)
	}
	dbRoot := filepath.Join(s.cfg.DBRoot, "db_storage")
	entries, err := os.ReadDir(dbRoot)
	if err != nil {
		return nil, err
	}
	// Shard filenames look like <prefix>_<n>.db where <prefix> may itself
	// contain underscores (e.g. biz_message_0.db → prefix=biz_message).
	// Group by prefix so different shard families (message vs biz_message)
	// don't collapse into each other, and non-shard dbs (message_fts.db,
	// message_resource.db) stay separate.
	shardRE := regexp.MustCompile(`^(.+)_\d+\.db$`)
	type out struct {
		Subdir     string   `json:"subdir"`
		File       string   `json:"file"`
		ShardCount int      `json:"shard_count,omitempty"`
		Tables     []string `json:"tables,omitempty"`
		Error      string   `json:"error,omitempty"`
	}
	var result []out
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := e.Name()
		files, err := os.ReadDir(filepath.Join(dbRoot, sub))
		if err != nil {
			result = append(result, out{Subdir: sub, Error: err.Error()})
			continue
		}
		// Group shard families by prefix; keep non-shard dbs as individuals.
		type family struct {
			canonical string
			count     int
		}
		families := map[string]*family{}
		var singles []string
		for _, f := range files {
			name := f.Name()
			if !strings.HasSuffix(name, ".db") {
				continue
			}
			if mm := shardRE.FindStringSubmatch(name); mm != nil {
				prefix := mm[1]
				fam := families[prefix]
				if fam == nil {
					fam = &family{canonical: name}
					families[prefix] = fam
				} else if name < fam.canonical {
					fam.canonical = name
				}
				fam.count++
			} else {
				singles = append(singles, name)
			}
		}
		listFile := func(name string, shardCount int) {
			db, err := s.openDB(sub, name)
			if err != nil {
				result = append(result, out{Subdir: sub, File: name, ShardCount: shardCount, Error: err.Error()})
				return
			}
			rows, err := db.Query(`SELECT name FROM sqlite_master
				WHERE type='table' AND name NOT LIKE 'sqlite_%'
				ORDER BY name`)
			db.Close()
			if err != nil {
				result = append(result, out{Subdir: sub, File: name, ShardCount: shardCount, Error: err.Error()})
				return
			}
			tables := make([]string, 0, len(rows))
			for _, r := range rows {
				if n, ok := r["name"].(string); ok {
					tables = append(tables, n)
				}
			}
			result = append(result, out{Subdir: sub, File: name, ShardCount: shardCount, Tables: tables})
		}
		for _, fam := range families {
			listFile(fam.canonical, fam.count)
		}
		for _, name := range singles {
			listFile(name, 0)
		}
	}
	return result, nil
}

func (s *server) toolForwardHistory(a map[string]any) (any, error) {
	db, err := s.openDB("general", "general.db")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var where []string
	var args []any
	if t := getStr(a, "after"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "forward_time >= ?")
		args = append(args, ts)
	}
	if t := getStr(a, "before"); t != "" {
		ts, err := parseTS(t)
		if err != nil {
			return nil, err
		}
		where = append(where, "forward_time < ?")
		args = append(args, ts)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, getInt(a, "limit", 50))
	rows, err := db.Query(fmt.Sprintf(`SELECT username, forward_time
		FROM ForwardRecent %s
		ORDER BY forward_time DESC
		LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	s.attachDisplayNames(rows, [2]string{"username", "display_name"})
	return rows, nil
}

// ──────────────────── helpers ────────────────────

func talkerHash(talker string) string {
	h := md5.Sum([]byte(talker))
	return hex.EncodeToString(h[:])
}

func getStr(a map[string]any, k string) string {
	if v, ok := a[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(a map[string]any, k string, def int) int {
	if v, ok := a[k]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

func getBool(a map[string]any, k string) bool {
	if v, ok := a[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

func getBoolDefault(a map[string]any, k string, def bool) bool {
	if v, ok := a[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func includeMediaPathsForMessages(a map[string]any) bool {
	return getBoolDefault(a, "include_media_paths", true)
}

func includeDebugOutput(a map[string]any) bool {
	return getBoolDefault(a, "include_debug", getBoolDefault(a, "debug", false))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func int64Arg(a map[string]any, k string) (int64, bool, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return 0, false, nil
	}
	if n, ok := integerArgValue(v); ok {
		return n, true, nil
	}
	if s, ok := v.(string); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("invalid argument %q: expected integer string, got %q", k, s)
		}
		return n, true, nil
	}
	return 0, false, fmt.Errorf("invalid argument %q: expected integer, got %T", k, v)
}

func mediaServerIDArg(a map[string]any) (int64, bool, error) {
	for _, key := range []string{"server_id_str", "message_server_id_str", "server_id", "message_server_id"} {
		if n, ok, err := int64Arg(a, key); ok || err != nil {
			return n, ok, err
		}
	}
	return 0, false, nil
}

func validateToolArgs(name string, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	var schema map[string]any
	for _, td := range toolDefs {
		if td.Name == name {
			if s, ok := td.InputSchema.(map[string]any); ok {
				schema = s
			}
			break
		}
	}
	if schema == nil {
		return nil
	}
	if req, ok := schema["required"].([]string); ok {
		for _, k := range req {
			if _, exists := args[k]; !exists {
				return fmt.Errorf("missing required argument %q for tool %s", k, name)
			}
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for k, v := range args {
		p, ok := props[k].(map[string]any)
		if !ok {
			return fmt.Errorf("unknown argument %q for tool %s", k, name)
		}
		if v == nil {
			continue
		}
		want, _ := p["type"].(string)
		switch want {
		case "string":
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("invalid argument %q for tool %s: expected string, got %T", k, name, v)
			}
			if err := validateStringEnum(name, k, s, p); err != nil {
				return err
			}
		case "integer":
			n, ok := integerArgValue(v)
			if !ok {
				return fmt.Errorf("invalid argument %q for tool %s: expected integer, got %T", k, name, v)
			}
			if n < 0 {
				return fmt.Errorf("invalid argument %q for tool %s: expected non-negative integer, got %d", k, name, n)
			}
			if max, ok := maxIntegerArg(name, k); ok && n > max {
				return fmt.Errorf("invalid argument %q for tool %s: maximum is %d, got %d", k, name, max, n)
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("invalid argument %q for tool %s: expected boolean, got %T", k, name, v)
			}
		}
	}
	return nil
}

func validateStringEnum(tool, key, value string, prop map[string]any) error {
	if key == "type_filter" || key == "filter" {
		return validateTypeFilterArg(tool, key, value)
	}
	raw, ok := prop["enum"].([]string)
	if !ok || len(raw) == 0 || value == "" {
		return nil
	}
	for _, allowed := range raw {
		if value == allowed {
			return nil
		}
	}
	return fmt.Errorf("invalid argument %q for tool %s: expected one of %s, got %q", key, tool, strings.Join(raw, "/"), value)
}

var allowedChatTypes = map[string]bool{
	"all": true, "private": true, "group": true, "official_account": true,
	"folded": true, "bot": true, "corp_im": true, "stranger": true,
}

func validateTypeFilterArg(tool, key, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	for _, part := range strings.Split(value, ",") {
		n := normalizeChatType(part)
		if !allowedChatTypes[n] {
			return fmt.Errorf("invalid argument %q for tool %s: unsupported chat type %q", key, tool, strings.TrimSpace(part))
		}
	}
	return nil
}

func isIntegerArg(v any) bool {
	_, ok := integerArgValue(v)
	return ok
}

func integerArgValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		i := int64(n)
		return i, n == float64(i)
	default:
		return 0, false
	}
}

func maxIntegerArg(tool, key string) (int64, bool) {
	switch key {
	case "limit":
		switch tool {
		case "export_messages":
			return 100000, true
		case "sql":
			return 1000, true
		default:
			return 5000, true
		}
	case "offset":
		return 1000000, true
	default:
		return 0, false
	}
}

func boundedReadSQL(q string, limit int) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("query is required")
	}
	q = strings.TrimSuffix(q, ";")
	q = strings.TrimSpace(q)
	if strings.Contains(q, ";") {
		return "", fmt.Errorf("sql tool accepts one read-only statement at a time")
	}
	parts := strings.Fields(q)
	if len(parts) == 0 {
		return "", fmt.Errorf("query is required")
	}
	verb := strings.ToLower(parts[0])
	if verb == "select" || verb == "with" {
		if limit <= 0 {
			limit = 200
		}
		if limit > 1000 {
			limit = 1000
		}
		return fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", q, limit), nil
	}
	if verb == "pragma" || verb == "explain" {
		return q, nil
	}
	return "", fmt.Errorf("sql tool only accepts read-only SELECT/WITH/PRAGMA/EXPLAIN statements")
}

// parseTS accepts unix seconds or local-timezone date/datetime strings.
// Empty input returns (0, nil). Invalid non-empty input returns an error
// rather than silently falling back to 0 — that would surprise the caller
// into returning unfiltered results.
func parseTS(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("无法解析时间: %s (支持 unix秒 / 2006-01-02 / 2006-01-02T15:04:05, 本地时区)", s)
}

// ──────────────────── zstd / field decode ────────────────────

var zstdDec *zstd.Decoder

func init() {
	d, _ := zstd.NewReader(nil)
	zstdDec = d
}

func tryDecodeField(v any) any {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "KLUv/") {
			if raw, err := base64.StdEncoding.DecodeString(x); err == nil {
				if zstdDec != nil && len(raw) >= 4 && raw[0] == 0x28 && raw[1] == 0xb5 && raw[2] == 0x2f && raw[3] == 0xfd {
					if out, err := zstdDec.DecodeAll(raw, nil); err == nil {
						return string(out)
					}
				}
			}
		}
	case []byte:
		if zstdDec != nil && len(x) >= 4 && x[0] == 0x28 && x[1] == 0xb5 && x[2] == 0x2f && x[3] == 0xfd {
			if out, err := zstdDec.DecodeAll(x, nil); err == nil {
				return string(out)
			}
		}
	}
	return v
}

func decodeFields(rows []wcdb.Row, fields ...string) []wcdb.Row {
	for _, row := range rows {
		for _, f := range fields {
			if v, ok := row[f]; ok {
				row[f] = tryDecodeField(v)
			}
		}
	}
	return rows
}

func loadName2Id(db *wcdb.DB) (map[int64]string, error) {
	rows, err := db.Query("SELECT rowid AS rid, user_name FROM Name2Id")
	if err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(rows))
	for _, r := range rows {
		if id, ok := r["rid"].(int64); ok {
			if u, ok := r["user_name"].(string); ok {
				m[id] = u
			}
		}
	}
	return m, nil
}

func resolveSenders(rows []wcdb.Row, senderMap map[int64]string) []wcdb.Row {
	for _, row := range rows {
		if id, ok := row["real_sender_id"].(int64); ok {
			if wxid, ok := senderMap[id]; ok {
				row["sender_wxid"] = wxid
			}
		}
	}
	return rows
}

// ──────────────────── SNS XML parsing ────────────────────

type xmlSnsDataItem struct {
	XMLName  xml.Name      `xml:"SnsDataItem"`
	Timeline xmlTimeline   `xml:"TimelineObject"`
	Local    xmlLocalExtra `xml:"LocalExtraInfo"`
}
type xmlTimeline struct {
	ID          string     `xml:"id"`
	Username    string     `xml:"username"`
	CreateTime  string     `xml:"createTime"`
	ContentDesc string     `xml:"contentDesc"`
	Private     string     `xml:"private"`
	Location    xmlLoc     `xml:"location"`
	Content     xmlContent `xml:"ContentObject"`
}
type xmlLoc struct {
	Lat  string `xml:"latitude,attr"`
	Lon  string `xml:"longitude,attr"`
	Name string `xml:"poiName,attr"`
}
type xmlContent struct {
	Type      string       `xml:"type"`
	MediaList xmlMediaList `xml:"mediaList"`
}
type xmlMediaList struct {
	Items []xmlMedia `xml:"media"`
}
type xmlMedia struct {
	Type          string      `xml:"type"`
	SubType       string      `xml:"sub_type"`
	URL           xmlMediaURL `xml:"url"`
	Thumb         xmlMediaURL `xml:"thumb"`
	Size          xmlMSize    `xml:"size"`
	VideoMD5      string      `xml:"videomd5"`
	VideoDuration string      `xml:"videoDuration"`
}
type xmlMediaURL struct {
	Text   string `xml:",chardata"`
	MD5    string `xml:"md5,attr"`
	Key    string `xml:"key,attr"`
	Token  string `xml:"token,attr"`
	EncIdx string `xml:"enc_idx,attr"`
}
type xmlMSize struct {
	Width  string `xml:"width,attr"`
	Height string `xml:"height,attr"`
	Total  string `xml:"totalSize,attr"`
}
type xmlLocalExtra struct {
	Nickname string `xml:"nickname"`
	LikeFlag string `xml:"like_flag"`
}

type snsPost struct {
	TID        string     `json:"tid"`
	Username   string     `json:"username"`
	Nickname   string     `json:"nickname"`
	AvatarURL  string     `json:"avatar_url,omitempty"`
	CreateTime int64      `json:"create_time"`
	Content    string     `json:"content"`
	Type       int        `json:"type"`
	Private    bool       `json:"private,omitempty"`
	LikedByMe  bool       `json:"liked_by_me,omitempty"`
	Media      []snsMedia `json:"media,omitempty"`
	Location   *snsLoc    `json:"location,omitempty"`
	Likes      []snsReact `json:"likes,omitempty"`
	Comments   []snsCmt   `json:"comments,omitempty"`
	ParseError string     `json:"parse_error,omitempty"`
}
type snsMedia struct {
	Type          string `json:"type"`
	RawType       int    `json:"raw_type,omitempty"`
	SubType       string `json:"sub_type,omitempty"`
	URL           string `json:"url,omitempty"`
	Thumb         string `json:"thumb,omitempty"`
	MD5           string `json:"md5,omitempty"`
	URLKey        string `json:"url_key,omitempty"`
	URLToken      string `json:"url_token,omitempty"`
	URLEncIdx     string `json:"url_enc_idx,omitempty"`
	ThumbKey      string `json:"thumb_key,omitempty"`
	ThumbToken    string `json:"thumb_token,omitempty"`
	ThumbEncIdx   string `json:"thumb_enc_idx,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	TotalSize     int    `json:"total_size,omitempty"`
	VideoMD5      string `json:"video_md5,omitempty"`
	VideoDuration int    `json:"video_duration,omitempty"`
}
type snsLoc struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}
type snsReact struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}
type snsCmt struct {
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	Content     string `json:"content"`
	CreateTime  int64  `json:"create_time"`
	ReplyTo     string `json:"reply_to,omitempty"`
	ReplyToNick string `json:"reply_to_nick,omitempty"`
}

func parseSnsXML(raw string) (*snsPost, error) {
	var item xmlSnsDataItem
	if err := xml.Unmarshal([]byte(raw), &item); err != nil {
		return nil, err
	}
	t := item.Timeline
	createTime := parseXMLInt64(t.CreateTime)
	contentType := parseXMLInt(t.Content.Type)
	p := &snsPost{
		TID: t.ID, Username: t.Username, Nickname: item.Local.Nickname,
		CreateTime: createTime, Content: t.ContentDesc, Type: contentType,
		Private: parseXMLInt(t.Private) != 0, LikedByMe: parseXMLInt(item.Local.LikeFlag) != 0,
	}
	for _, m := range t.Content.MediaList.Items {
		rawType := parseXMLInt(m.Type)
		mt := "image"
		if rawType != 2 {
			mt = "video"
		}
		p.Media = append(p.Media, snsMedia{
			Type: mt, RawType: rawType, SubType: strings.TrimSpace(m.SubType),
			URL: strings.TrimSpace(m.URL.Text), Thumb: strings.TrimSpace(m.Thumb.Text),
			MD5:    strings.TrimSpace(m.URL.MD5),
			URLKey: strings.TrimSpace(m.URL.Key), URLToken: strings.TrimSpace(m.URL.Token), URLEncIdx: strings.TrimSpace(m.URL.EncIdx),
			ThumbKey: strings.TrimSpace(m.Thumb.Key), ThumbToken: strings.TrimSpace(m.Thumb.Token), ThumbEncIdx: strings.TrimSpace(m.Thumb.EncIdx),
			Width: parseXMLInt(m.Size.Width), Height: parseXMLInt(m.Size.Height), TotalSize: parseXMLInt(m.Size.Total),
			VideoMD5: strings.TrimSpace(m.VideoMD5), VideoDuration: parseXMLInt(m.VideoDuration),
		})
	}
	lat, _ := strconv.ParseFloat(t.Location.Lat, 64)
	lon, _ := strconv.ParseFloat(t.Location.Lon, 64)
	if lat != 0 || lon != 0 || t.Location.Name != "" {
		p.Location = &snsLoc{Name: t.Location.Name, Lat: lat, Lon: lon}
	}
	return p, nil
}

func parseXMLInt(s string) int {
	return int(parseXMLInt64(s))
}

func parseXMLInt64(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

func loadSnsInteractions(db *wcdb.DB, tids []int64) (map[int64][]snsReact, map[int64][]snsCmt) {
	if len(tids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(tids))
	args := make([]any, len(tids))
	for i, t := range tids {
		ph[i] = "?"
		args[i] = t
	}
	likes := make(map[int64][]snsReact)
	comments := make(map[int64][]snsCmt)
	rows, err := db.Query(
		fmt.Sprintf("SELECT feed_id, type, from_username, from_nickname, to_username, to_nickname, content, create_time FROM SnsMessage_tmp3 WHERE feed_id IN (%s) ORDER BY create_time", strings.Join(ph, ",")),
		args...)
	if err != nil {
		return likes, comments
	}
	for _, r := range rows {
		fid, _ := r["feed_id"].(int64)
		typ, _ := r["type"].(int64)
		fu, _ := r["from_username"].(string)
		fn, _ := r["from_nickname"].(string)
		switch typ {
		case 1:
			likes[fid] = append(likes[fid], snsReact{Username: fu, Nickname: fn})
		case 2:
			tu, _ := r["to_username"].(string)
			tn, _ := r["to_nickname"].(string)
			ct, _ := r["content"].(string)
			ts, _ := r["create_time"].(int64)
			c := snsCmt{Username: fu, Nickname: fn, Content: ct, CreateTime: ts}
			if tu != "" {
				c.ReplyTo = tu
				c.ReplyToNick = tn
			}
			comments[fid] = append(comments[fid], c)
		}
	}
	return likes, comments
}

// ──────────────────── message_content enrichment ────────────────────

type xmlMsgImg struct {
	XMLName xml.Name `xml:"msg"`
	Img     struct {
		AesKey       string `xml:"aeskey,attr"`
		Length       int64  `xml:"length,attr"`
		HdLength     int64  `xml:"hdlength,attr"`
		MD5          string `xml:"md5,attr"`
		CdnMidURL    string `xml:"cdnmidimgurl,attr"`
		CdnBigURL    string `xml:"cdnbigimgurl,attr"`
		CdnThumbURL  string `xml:"cdnthumburl,attr"`
		CdnHdHeight  int    `xml:"cdnhdheight,attr"`
		CdnHdWidth   int    `xml:"cdnhdwidth,attr"`
		CdnMidHeight int    `xml:"cdnmidheight,attr"`
		CdnMidWidth  int    `xml:"cdnmidwidth,attr"`
	} `xml:"img"`
}

type xmlMsgVoice struct {
	XMLName  xml.Name `xml:"msg"`
	VoiceMsg struct {
		VoiceLength int64  `xml:"voicelength,attr"`
		VoiceFormat int    `xml:"voiceformat,attr"`
		Length      int64  `xml:"length,attr"`
		EndFlag     int    `xml:"endflag,attr"`
		CancelFlag  int    `xml:"cancelflag,attr"`
		VoiceURL    string `xml:"voiceurl,attr"`
		AesKey      string `xml:"aeskey,attr"`
	} `xml:"voicemsg"`
}

type xmlMsgEmoji struct {
	XMLName xml.Name `xml:"msg"`
	Emoji   struct {
		AesKey     string `xml:"aeskey,attr"`
		MD5        string `xml:"md5,attr"`
		CdnURL     string `xml:"cdnurl,attr"`
		EncryptURL string `xml:"encrypturl,attr"`
		Width      int    `xml:"width,attr"`
		Height     int    `xml:"height,attr"`
		Type       int    `xml:"type,attr"`
	} `xml:"emoji"`
}

type xmlMsgCard struct {
	XMLName         xml.Name `xml:"msg"`
	BigHeadImgURL   string   `xml:"bigheadimgurl,attr"`
	SmallHeadImgURL string   `xml:"smallheadimgurl,attr"`
	Username        string   `xml:"username,attr"`
	Nickname        string   `xml:"nickname,attr"`
	Sex             int      `xml:"sex,attr"`
	Alias           string   `xml:"alias,attr"`
	Province        string   `xml:"province,attr"`
	City            string   `xml:"city,attr"`
	Signature       string   `xml:"sign,attr"`
	Scene           int      `xml:"scene,attr"`
}

type xmlMsgLocation struct {
	XMLName  xml.Name `xml:"msg"`
	Location struct {
		X       float64 `xml:"x,attr"`
		Y       float64 `xml:"y,attr"`
		Scale   int     `xml:"scale,attr"`
		Label   string  `xml:"label,attr"`
		PoiName string  `xml:"poiname,attr"`
	} `xml:"location"`
}

type xmlMsgAppmsg struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		Title             string `xml:"title"`
		Des               string `xml:"des"`
		URL               string `xml:"url"`
		LowURL            string `xml:"lowurl"`
		Type              int    `xml:"type"`
		SourceUsername    string `xml:"sourceusername"`
		SourceDisplayName string `xml:"sourcedisplayname"`
		ThumbURL          string `xml:"thumburl"`
		AppAttach         struct {
			TotalLen int64  `xml:"totallen"`
			AttachID string `xml:"attachid"`
			FileExt  string `xml:"fileext"`
			AesKey   string `xml:"aeskey"`
		} `xml:"appattach"`
		WcPayInfo struct {
			FeeDesc       string `xml:"feedesc"`
			Description   string `xml:"description"`
			PaySubType    int    `xml:"paysubtype"`
			PayMemo       string `xml:"pay_memo"`
			SenderTitle   string `xml:"sendertitle"`
			ReceiverTitle string `xml:"receivertitle"`
			SceneText     string `xml:"scenetext"`
			TemplateID    string `xml:"templateid"`
			InnerType     int    `xml:"innertype"`
			NativeURL     string `xml:"nativeurl"`
		} `xml:"wcpayinfo"`
		ReferMsg *xmlMsgReferMsg `xml:"refermsg"`
	} `xml:"appmsg"`
}

type xmlMsgReferMsg struct {
	ChatUsr     string `xml:"chatusr"`
	Type        int    `xml:"type"`
	CreateTime  int64  `xml:"createtime"`
	DisplayName string `xml:"displayname"`
	SvrID       string `xml:"svrid"`
	FromUsr     string `xml:"fromusr"`
	Content     string `xml:"content"`
}

// fetchMessageContent batch-loads message_content (zstd decoded) for a list of
// (talker, server_id) pairs. Groups by talker → routes to every matching
// Msg_<hash> shard, because WeChat can keep one conversation table in multiple
// message shard DBs over time. Returns server_id → content map; entries missing
// means lookup failed (table not found / row gone) and callers treat the
// enrichment field as absent rather than error.
func (s *server) fetchMessageContent(rows []wcdb.Row, sidCol, talkerCol string) map[int64]string {
	byTalker := make(map[string][]int64)
	for _, r := range rows {
		t, _ := r[talkerCol].(string)
		sid, _ := r[sidCol].(int64)
		if t == "" || sid == 0 {
			continue
		}
		byTalker[t] = append(byTalker[t], sid)
	}
	out := make(map[int64]string)
	for talker, sids := range byTalker {
		tableName := "Msg_" + talkerHash(talker)
		shards, err := s.findMsgDBs(tableName)
		if err != nil {
			continue
		}
		ph := make([]string, len(sids))
		args := make([]any, len(sids))
		for i, sid := range sids {
			ph[i] = "?"
			args[i] = sid
		}
		for _, shard := range shards {
			contentRows, qerr := shard.DB.Query(fmt.Sprintf(
				"SELECT server_id, message_content FROM %s WHERE server_id IN (%s)",
				tableName, strings.Join(ph, ",")), args...)
			if qerr != nil {
				continue
			}
			decoded := decodeFields(contentRows, "message_content")
			for _, cr := range decoded {
				sid, _ := cr["server_id"].(int64)
				content, _ := cr["message_content"].(string)
				out[sid] = content
			}
		}
		closeMsgDBs(shards)
	}
	return out
}

func messagePairKey(talker string, serverID int64) string {
	return talker + "\x00" + strconv.FormatInt(serverID, 10)
}

func (s *server) liveMessageMeta(rows []wcdb.Row, sidCol, talkerCol string) map[string]wcdb.Row {
	out := map[string]wcdb.Row{}
	if len(rows) == 0 {
		return out
	}
	byTalker := make(map[string][]int64)
	for _, r := range rows {
		t := rowString(r, talkerCol)
		sid := rowInt64(r, sidCol)
		if t == "" || sid == 0 {
			continue
		}
		byTalker[t] = append(byTalker[t], sid)
	}
	var allMeta []wcdb.Row
	for talker, sids := range byTalker {
		tableName := "Msg_" + talkerHash(talker)
		shards, err := s.findMsgDBs(tableName)
		if err != nil {
			continue
		}
		ph := make([]string, len(sids))
		args := make([]any, 0, len(sids))
		for i, sid := range sids {
			ph[i] = "?"
			args = append(args, sid)
		}
		for _, shard := range shards {
			n2i, _ := loadName2Id(shard.DB)
			metaRows, qerr := shard.DB.Query(fmt.Sprintf(`SELECT server_id, create_time, real_sender_id
				FROM %s WHERE server_id IN (%s)`, quoteIdent(tableName), strings.Join(ph, ",")), args...)
			if qerr != nil {
				continue
			}
			if n2i != nil {
				metaRows = resolveSenders(metaRows, n2i)
			}
			for _, mr := range metaRows {
				mr["talker"] = talker
				if ct := rowInt64(mr, "create_time"); ct != 0 {
					mr["create_time_human"] = time.Unix(ct, 0).Format("2006-01-02 15:04:05")
				}
				allMeta = append(allMeta, mr)
			}
		}
		closeMsgDBs(shards)
	}
	s.attachDisplayNames(allMeta, [2]string{"sender_wxid", "sender_display_name"})
	for _, mr := range allMeta {
		out[messagePairKey(rowString(mr, "talker"), rowInt64(mr, "server_id"))] = mr
	}
	return out
}

// stripMsgPrefix trims the "wxid_xxx:\n" sender prefix WeChat prepends to
// group message content so xml.Unmarshal sees a clean XML document.
func stripMsgPrefix(raw string) string {
	if idx := strings.Index(raw, "<"); idx > 0 {
		return raw[idx:]
	}
	return raw
}

// parseMessageContent returns a structured JSON-serializable value for supported
// (base_kind, subtype). Returns nil for unsupported kinds; for known kinds whose
// XML failed to parse, returns {"parse_error": ...} so agents can distinguish
// "no data" from "parser drifted" (e.g. WeChat schema bumped). Raw
// message_content is always retained in the row so no information is lost.
// Depth bounds recursion for nested refermsg content.
func parseMessageContent(baseKind, subtype int32, raw string, depth int) any {
	if depth <= 0 || raw == "" {
		return nil
	}
	xmlStr := stripMsgPrefix(raw)
	switch baseKind {
	case 3:
		var m xmlMsgImg
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "image"}
		}
		return map[string]any{
			"md5":           m.Img.MD5,
			"length":        m.Img.Length,
			"hd_length":     m.Img.HdLength,
			"aeskey":        m.Img.AesKey,
			"cdn_mid_url":   m.Img.CdnMidURL,
			"cdn_big_url":   m.Img.CdnBigURL,
			"cdn_thumb_url": m.Img.CdnThumbURL,
			"hd_width":      m.Img.CdnHdWidth,
			"hd_height":     m.Img.CdnHdHeight,
			"mid_width":     m.Img.CdnMidWidth,
			"mid_height":    m.Img.CdnMidHeight,
		}
	case 34:
		var m xmlMsgVoice
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "voice"}
		}
		return compactMap(map[string]any{
			"duration_ms":  m.VoiceMsg.VoiceLength,
			"voice_format": m.VoiceMsg.VoiceFormat,
			"length":       m.VoiceMsg.Length,
			"endflag":      m.VoiceMsg.EndFlag,
			"cancelflag":   m.VoiceMsg.CancelFlag,
			"voice_url":    m.VoiceMsg.VoiceURL,
			"aeskey":       m.VoiceMsg.AesKey,
		})
	case 42:
		var m xmlMsgCard
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "card"}
		}
		return map[string]any{
			"username":           m.Username,
			"nickname":           m.Nickname,
			"alias":              m.Alias,
			"small_head_img_url": m.SmallHeadImgURL,
			"big_head_img_url":   m.BigHeadImgURL,
			"province":           m.Province,
			"city":               m.City,
			"signature":          m.Signature,
			"sex":                m.Sex,
			"scene":              m.Scene,
		}
	case 47:
		var m xmlMsgEmoji
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "emoji"}
		}
		return map[string]any{
			"aeskey":      m.Emoji.AesKey,
			"md5":         m.Emoji.MD5,
			"cdn_url":     m.Emoji.CdnURL,
			"encrypt_url": m.Emoji.EncryptURL,
			"width":       m.Emoji.Width,
			"height":      m.Emoji.Height,
			"type":        m.Emoji.Type,
		}
	case 48:
		var m xmlMsgLocation
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "location"}
		}
		return map[string]any{
			"latitude":  m.Location.X,
			"longitude": m.Location.Y,
			"scale":     m.Location.Scale,
			"label":     m.Location.Label,
			"poiname":   m.Location.PoiName,
		}
	case 49:
		var m xmlMsgAppmsg
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "app"}
		}
		out := map[string]any{
			"app_subtype":         m.AppMsg.Type,
			"title":               m.AppMsg.Title,
			"des":                 m.AppMsg.Des,
			"url":                 firstNonEmpty(m.AppMsg.URL, m.AppMsg.LowURL),
			"source_username":     m.AppMsg.SourceUsername,
			"source_display_name": m.AppMsg.SourceDisplayName,
			"thumb_url":           m.AppMsg.ThumbURL,
		}
		if m.AppMsg.AppAttach.TotalLen != 0 || m.AppMsg.AppAttach.AttachID != "" || m.AppMsg.AppAttach.FileExt != "" {
			out["app_attach"] = compactMap(map[string]any{
				"total_len": m.AppMsg.AppAttach.TotalLen,
				"attach_id": m.AppMsg.AppAttach.AttachID,
				"file_ext":  m.AppMsg.AppAttach.FileExt,
			})
		}
		if m.AppMsg.WcPayInfo.FeeDesc != "" || m.AppMsg.WcPayInfo.SenderTitle != "" ||
			m.AppMsg.WcPayInfo.ReceiverTitle != "" || m.AppMsg.WcPayInfo.NativeURL != "" {
			out["wcpayinfo"] = compactMap(map[string]any{
				"feedesc":       m.AppMsg.WcPayInfo.FeeDesc,
				"description":   m.AppMsg.WcPayInfo.Description,
				"paysubtype":    m.AppMsg.WcPayInfo.PaySubType,
				"pay_memo":      m.AppMsg.WcPayInfo.PayMemo,
				"sendertitle":   m.AppMsg.WcPayInfo.SenderTitle,
				"receivertitle": m.AppMsg.WcPayInfo.ReceiverTitle,
				"scenetext":     m.AppMsg.WcPayInfo.SceneText,
				"templateid":    m.AppMsg.WcPayInfo.TemplateID,
				"innertype":     m.AppMsg.WcPayInfo.InnerType,
				"nativeurl":     m.AppMsg.WcPayInfo.NativeURL,
			})
		}
		if subtype == 19 {
			items, err := wxparse.ForwardItems(raw, depth)
			if err != nil {
				out["forward_items_parse_error"] = err.Error()
			} else if len(items) > 0 {
				out["forward_items"] = items
			}
		}
		if m.AppMsg.ReferMsg != nil {
			r := m.AppMsg.ReferMsg
			refer := map[string]any{
				"chatusr":     r.ChatUsr,
				"type":        r.Type,
				"createtime":  r.CreateTime,
				"displayname": r.DisplayName,
				"svrid":       r.SvrID,
				"fromusr":     r.FromUsr,
				"content_raw": r.Content,
			}
			if parsed := parseMessageContent(int32(r.Type), 0, r.Content, depth-1); parsed != nil {
				refer["content_parsed"] = parsed
			}
			out["refermsg"] = refer
		}
		return out
	}
	return nil
}

const messageContentParseDepth = 5

// enrichMessages augments raw message rows with packed-type decoding, a
// structured message_content_parsed sibling field, and a one-line
// content_summary suitable for agent display. Raw local_type and
// message_content are always preserved.
func enrichMessages(rows []wcdb.Row) []wcdb.Row {
	for _, row := range rows {
		lt, ok := row["local_type"].(int64)
		if !ok {
			continue
		}
		baseKind, subtype, name := wxkind.Unpack(lt)
		row["base_kind"] = baseKind
		row["subtype"] = subtype
		row["kind_name"] = name
		content, _ := row["message_content"].(string)
		if content != "" {
			if parsed := parseMessageContent(baseKind, subtype, content, messageContentParseDepth); parsed != nil {
				row["message_content_parsed"] = parsed
			}
		}
		row["content_summary"] = contentSummary(baseKind, subtype, content, row["message_content_parsed"])
		if ct, ok := row["create_time"].(int64); ok && ct > 0 {
			row["create_time_human"] = time.Unix(ct, 0).Format("2006-01-02 15:04:05")
		}
	}
	return rows
}

// senderPrefixRe matches a "wxid:\n" prefix attached to group-chat raw
// content. WeChat stores group messages as "<senderWxid>:\n<actual text>";
// this prefix is redundant once sender_wxid is exposed as its own field.
// Anchored requirement of newline after ':' avoids stripping URLs (https://).
var senderPrefixRe = regexp.MustCompile(`^\s*[a-zA-Z0-9_@-]+:\s*\r?\n\s*`)

// contentSummary returns a one-line human-readable summary for display.
// text/system → raw content (sender prefix stripped); media → bracketed
// placeholder; app → title or quoted-reply composite. Depth-bounded implicitly
// via parseMessageContent.
func contentSummary(baseKind, subtype int32, raw string, parsed any) string {
	switch baseKind {
	case 1:
		return senderPrefixRe.ReplaceAllString(raw, "")
	case 3:
		return "[图片]"
	case 34:
		if p, _ := parsed.(map[string]any); p != nil {
			if ms, ok := integerArgValue(p["duration_ms"]); ok && ms > 0 {
				return fmt.Sprintf("[语音] %.1fs", float64(ms)/1000)
			}
		}
		return "[语音]"
	case 42:
		if p, _ := parsed.(map[string]any); p != nil {
			if name := firstNonEmpty(stringMapValue(p, "nickname"), stringMapValue(p, "username")); name != "" {
				return "[名片] " + name
			}
		}
		return "[名片]"
	case 43:
		return "[视频]"
	case 47:
		return "[表情]"
	case 48:
		if p, _ := parsed.(map[string]any); p != nil {
			if label := firstNonEmpty(stringMapValue(p, "label"), stringMapValue(p, "poiname")); label != "" {
				return "[位置] " + label
			}
		}
		return "[位置]"
	case 49:
		p, _ := parsed.(map[string]any)
		if p == nil {
			return "[应用消息]"
		}
		title, _ := p["title"].(string)
		wcpay := mapAny(p["wcpayinfo"])
		switch subtype {
		case 2000:
			return firstNonEmpty(stringMapValue(p, "des"), stringMapValue(wcpay, "description"), stringMapValue(wcpay, "feedesc"), "[转账]")
		case 2001:
			return firstNonEmpty(stringMapValue(wcpay, "sendertitle"), title, "[红包]")
		}
		if subtype == 57 {
			quoted := "..."
			if r, ok := p["refermsg"].(map[string]any); ok {
				refType := int32(0)
				if t, ok := r["type"].(int); ok {
					refType = int32(t)
				} else if t, ok := integerArgValue(r["type"]); ok {
					refType = int32(t)
				}
				refRaw, _ := r["content_raw"].(string)
				refParsed := mapAny(r["content_parsed"])
				refSubtype := int32(0)
				if refType == 49 {
					if n, ok := integerArgValue(refParsed["app_subtype"]); ok {
						refSubtype = int32(n)
					}
				}
				quoted = contentSummary(refType, refSubtype, refRaw, refParsed)
			}
			if title != "" {
				return "[引用: " + quoted + "] " + title
			}
			return "[引用: " + quoted + "]"
		}
		if title != "" {
			return title
		}
		return "[应用消息]"
	case 50:
		return "[通话]"
	case 10000:
		return raw
	}
	return fmt.Sprintf("[未知类型 base_kind=%d]", baseKind)
}

// selfWxid derives V's own wxid by stripping WeChat 4.x's _<4-hex> device
// suffix from the config wxid. Returns empty if config wxid is unset.
func (s *server) selfWxid() string {
	raw := s.cfg.Wxid
	if raw == "" {
		return ""
	}
	if i := strings.LastIndex(raw, "_"); i > 0 && len(raw)-i == 5 {
		return raw[:i]
	}
	return raw
}

// findUsernamesByFuzzyName returns contact usernames whose display identity
// (nick_name / remark / alias) matches keyword case-insensitively and
// space-insensitively. Used by sessions.keyword to enable cross-db display_name
// search without regressing the user-visible interface.
func (s *server) findUsernamesByFuzzyName(kw string) []string {
	if kw == "" {
		return nil
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil
	}
	defer db.Close()
	like := "%" + kw + "%"
	likeNoSpace := "%" + strings.ReplaceAll(kw, " ", "") + "%"
	rows, err := db.Query(`SELECT username FROM contact WHERE
		nick_name LIKE ? COLLATE NOCASE
		OR REPLACE(nick_name, ' ', '') LIKE ? COLLATE NOCASE
		OR remark LIKE ? COLLATE NOCASE
		OR REPLACE(remark, ' ', '') LIKE ? COLLATE NOCASE
		OR alias LIKE ? COLLATE NOCASE`, like, likeNoSpace, like, likeNoSpace, like)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if u, ok := r["username"].(string); ok {
			out = append(out, u)
		}
	}
	return out
}

// lookupDisplayNames batch-queries contact.db for remark > nick_name > username
// preference per wxid. Returns nil on error.
func (s *server) lookupDisplayNames(names map[string]bool) map[string]string {
	if len(names) == 0 {
		return nil
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil
	}
	defer db.Close()
	ph := make([]string, 0, len(names))
	args := make([]any, 0, len(names))
	for n := range names {
		ph = append(ph, "?")
		args = append(args, n)
	}
	q := fmt.Sprintf(`SELECT username,
		COALESCE(NULLIF(remark, ''), NULLIF(nick_name, ''), username) AS dn
		FROM contact WHERE username IN (%s)`, strings.Join(ph, ","))
	cr, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	m := make(map[string]string)
	for _, r := range cr {
		u, _ := r["username"].(string)
		dn, _ := r["dn"].(string)
		if dn != "" {
			m[u] = dn
		}
	}
	return m
}

// attachDisplayNames fills one or more display_name fields on rows by looking
// up usernames from contact.db. Each pair is [sourceField, targetField].
// Missing lookups fall back to the raw username so the target field is always
// populated (never undefined in agent-side JSON).
func (s *server) attachDisplayNames(rows []wcdb.Row, pairs ...[2]string) {
	if len(rows) == 0 || len(pairs) == 0 {
		return
	}
	names := make(map[string]bool)
	for _, r := range rows {
		for _, p := range pairs {
			if v, ok := r[p[0]].(string); ok && v != "" {
				names[v] = true
			}
		}
	}
	m := s.lookupDisplayNames(names)
	if m == nil {
		m = make(map[string]string)
	}
	for _, r := range rows {
		for _, p := range pairs {
			v, _ := r[p[0]].(string)
			if v == "" {
				continue
			}
			if dn, ok := m[v]; ok {
				r[p[1]] = dn
			} else {
				r[p[1]] = v
			}
		}
	}
}

// attachSnsAvatars batch-queries contact.big_head_url and attaches AvatarURL
// to each post. Silent on errors.
func (s *server) attachSnsAvatars(posts []*snsPost) {
	if len(posts) == 0 {
		return
	}
	names := make(map[string]bool)
	for _, p := range posts {
		if p.Username != "" {
			names[p.Username] = true
		}
	}
	if len(names) == 0 {
		return
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return
	}
	defer db.Close()
	ph := make([]string, 0, len(names))
	args := make([]any, 0, len(names))
	for n := range names {
		ph = append(ph, "?")
		args = append(args, n)
	}
	rows, err := db.Query(fmt.Sprintf(
		"SELECT username, big_head_url FROM contact WHERE username IN (%s)",
		strings.Join(ph, ",")), args...)
	if err != nil {
		return
	}
	m := make(map[string]string)
	for _, r := range rows {
		u, _ := r["username"].(string)
		url, _ := r["big_head_url"].(string)
		m[u] = url
	}
	for _, p := range posts {
		if url, ok := m[p.Username]; ok {
			p.AvatarURL = url
		}
	}
}
