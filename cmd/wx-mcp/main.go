package main

import (
	"bufio"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	cfg            *config.Config
	wcdbPath       string
	ok             bool
	keyRefreshMu   sync.Mutex
	keyRefreshLast map[string]time.Time
}

// findWCDB locates the platform WCDB dynamic library.
func findWCDB() (string, error) {
	var candidates []string
	if p := strings.TrimSpace(os.Getenv("WX_MCP_WCDB_LIB")); p != "" {
		candidates = append(candidates, p)
	}
	if p := strings.TrimSpace(os.Getenv("WX_MCP_WCDB_DYLIB")); p != "" {
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
		return "", fmt.Errorf("libWCDB.dylib 未找到。把它放在 wx-mcp 旁边 (./lib/libWCDB.dylib), ~/.config/wxcli/lib/, 或设置 WX_MCP_WCDB_DYLIB")
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
	fmt.Fprintf(os.Stderr, "[wx-mcp] %s — running wxkey key setup...\n", reason)
	res, stderr, err := wxkey.RunSetup()
	if err != nil {
		return fmt.Errorf("wxkey setup failed: %w\n%s\nOn macOS, run `wxkey bootstrap` once to prepare the no-SIP key cache. On Windows, keep WeChat logged in, verify WX_MCP_DB_ROOT matches the logged-in account, then retry.", err, stderr)
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
	fmt.Fprintf(os.Stderr, "[wx-mcp] wxkey setup OK — %d per-DB keys cached for wxid=%s\n",
		len(res.Keys), res.WxID)
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

func (s *server) findMsgDB(tableName string) (*wcdb.DB, error) {
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
	for _, name := range shards {
		db, err := s.openDB("message", name)
		if err != nil {
			openErrs = append(openErrs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		rows, err := db.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", tableName)
		if err == nil && len(rows) > 0 {
			return db, nil
		}
		db.Close()
	}
	if len(openErrs) > 0 {
		return nil, fmt.Errorf("table %s not found in opened message shards; %d/%d shards could not be opened, first error: %v", tableName, len(openErrs), len(shards), openErrs[0])
	}
	return nil, fmt.Errorf("table %s not found in %d message shards", tableName, len(shards))
}

// ──────────────────── main loop ────────────────────

func main() {
	if len(os.Args) > 1 {
		if maybeRunCLI(os.Args[1:]) {
			return
		}
		printCLIUsage()
		os.Exit(2)
	}
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
			"serverInfo":      map[string]any{"name": "wx-mcp", "version": "1.4.5"},
			"instructions": "Errors and partial-success signals are embedded in normal tool returns — read them, don't paper over.\n" +
				"- Per-record `error` fields (e.g. `no enc_key for salt ...`) mean that specific db is unreadable; surface that to the user, do not silently treat it as `no data`.\n" +
				"- Empty results when you expected data: report the gap to the user. Do not auto-trigger other tools to `fix` it — recovery (e.g. rerunning `wxkey setup`) is a privileged side effect that should be the user's call, not yours.\n" +
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
		"new_messages":           s.toolNewMessages,
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
	if rows, ok, err := s.cacheMessages(a); ok || err != nil {
		return rows, err
	}
	talker := getStr(a, "talker")
	if talker == "" {
		talker = getStr(a, "chat")
	}
	if talker == "" {
		return nil, fmt.Errorf("talker or chat is required")
	}
	if getStr(a, "talker") == "" && !looksLikeRawChatID(talker) {
		return nil, fmt.Errorf("chat %q requires cache index for display-name resolution; run `wx-mcp cache refresh` first or pass raw talker/wxid", talker)
	}
	if messagesHasCacheOnlyFilters(a) {
		return nil, fmt.Errorf("messages filters type/kind_name/base_kind/sender require cache index; run `wx-mcp cache refresh` first")
	}
	if aggregatorSessions[talker] {
		return nil, fmt.Errorf("%q 是订阅号合集入口 (UI 聚合 session), 本身无消息表. 真实消息在各 gh_* 公众号下, 按具体 gh_<id> 查", talker)
	}
	tableName := "Msg_" + talkerHash(talker)
	db, err := s.findMsgDB(tableName)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var where []string
	var args []any

	if s := getStr(a, "after"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, err
		}
		where = append(where, "create_time >= ?")
		args = append(args, ts)
	}
	if s := getStr(a, "before"); s != "" {
		ts, err := parseTS(s)
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
	offset := getInt(a, "offset", 0)
	kw := getStr(a, "keyword")

	// keyword 不能进 SQL WHERE — message_content 是 zstd 压缩 BLOB, LIKE
	// 在压缩字节上 match 不了任何文本. 改在 enrich 解压后 in-memory filter.
	// 拉宽 SQL 取数, offset 也在 Go 里应用 (避免 SQL offset 跳过未命中行).
	sqlLimit := limit
	sqlOffset := offset
	if kw != "" {
		sqlLimit = 5000
		sqlOffset = 0
	}
	args = append(args, sqlLimit, sqlOffset)

	rows, err := db.Query(fmt.Sprintf(`SELECT local_id, server_id, local_type, sort_seq,
		real_sender_id, create_time, status, message_content, source
		FROM %s %s
		ORDER BY sort_seq DESC
		LIMIT ? OFFSET ?`, tableName, wc), args...)
	if err != nil {
		return nil, err
	}
	if m, _ := loadName2Id(db); m != nil {
		rows = resolveSenders(rows, m)
	}
	rows = enrichMessages(decodeFields(rows, "message_content", "source"))
	s.attachDisplayNames(rows, [2]string{"sender_wxid", "sender_display_name"})
	if selfWxid := s.selfWxid(); selfWxid != "" {
		for _, r := range rows {
			sw, _ := r["sender_wxid"].(string)
			r["is_from_me"] = (sw == selfWxid)
		}
	}
	for _, r := range rows {
		delete(r, "real_sender_id")
		delete(r, "sort_seq")
		delete(r, "status")
		delete(r, "source")
		delete(r, "local_type")
	}
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
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
		rows = filtered
	}
	mode := getStr(a, "fields")
	if mode == "" {
		mode = "lite"
	}
	if mode != "lite" && mode != "full" {
		return nil, fmt.Errorf("invalid fields=%q: must be \"lite\" or \"full\"", mode)
	}
	return liteMessages(rows, mode), nil
}

func messagesHasCacheOnlyFilters(a map[string]any) bool {
	for _, k := range []string{"type", "kind_name", "sender"} {
		if getStr(a, k) != "" {
			return true
		}
	}
	return getInt(a, "base_kind", 0) != 0
}

// liteMessages strips raw XML / parsed / source / housekeeping fields when
// mode=lite. Keeps the 8 fields that matter for human-readable summarization
// (typical 100-row response: ~250KB full → ~12KB lite, ~95% reduction).
// mode=full passes through unchanged.
func liteMessages(rows []wcdb.Row, mode string) []wcdb.Row {
	if mode != "lite" {
		return rows
	}
	keep := map[string]bool{
		"talker": true, "talker_display_name": true, "chat_type": true,
		"local_id": true, "server_id": true,
		"create_time": true, "create_time_human": true,
		"sender_wxid": true, "sender_display_name": true, "is_from_me": true,
		"base_kind": true, "kind_name": true, "content_summary": true,
	}
	for _, r := range rows {
		for k := range r {
			if !keep[k] {
				delete(r, k)
			}
		}
	}
	return rows
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
	return s.buildMediaResourceOutput(rows, getBoolDefault(a, "include_local_paths", true)), nil
}

func (s *server) buildMediaResourceOutput(rows []wcdb.Row, includeLocalPaths bool) []map[string]any {
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
	return out
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
		res["local_paths"] = s.localMediaPaths(rowString(r, "talker"), rowInt64(r, "message_create_time"), family, md5Value, fileNames)
	}
	return res
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

func (s *server) resolveLooseChatArg(a map[string]any) (string, error) {
	raw := strings.TrimSpace(firstNonEmpty(getStr(a, "talker"), getStr(a, "chat")))
	if raw == "" || looksLikeRawChatID(raw) {
		return raw, nil
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		return "", fmt.Errorf("chat %q requires cache index for display-name resolution; run `wx-mcp cache refresh` first or pass raw talker/wxid", raw)
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
		return "", fmt.Errorf("sender %q requires cache index for display-name resolution; run `wx-mcp cache refresh` first or pass raw sender wxid", raw)
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
			return nil, fmt.Errorf("chat %q requires cache index for group-name resolution; run `wx-mcp cache refresh` first or pass raw chatroom_id", target)
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
	msgDB, err := s.findMsgDB(tableName)
	if err != nil {
		return nil, fmt.Errorf("stats=true 失败 (%s): %w", tableName, err)
	}
	defer msgDB.Close()
	n2i, err := loadName2Id(msgDB)
	if err != nil {
		return nil, fmt.Errorf("stats=true 失败 (loadName2Id): %w", err)
	}
	countRows, err := msgDB.Query(fmt.Sprintf(
		"SELECT real_sender_id, COUNT(*) AS cnt FROM %s GROUP BY real_sender_id", tableName))
	if err != nil {
		return nil, fmt.Errorf("stats=true 失败 (count query): %w", err)
	}
	counts := make(map[string]int64)
	for _, r := range countRows {
		id, _ := r["real_sender_id"].(int64)
		cnt, _ := r["cnt"].(int64)
		if w, ok := n2i[id]; ok {
			counts[w] = cnt
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
	if rows, ok, err := s.cacheSearch(a); ok || err != nil {
		return rows, err
	}
	kw := getStr(a, "keyword")
	if kw == "" {
		return nil, fmt.Errorf("keyword is required")
	}
	mode := searchMode(a)
	if mode == "fts" {
		return nil, fmt.Errorf("search_mode=fts requires cache index; run `wx-mcp cache refresh` first or pass search_mode=like for legacy direct search")
	}
	if searchHasCacheOnlyFilters(a) {
		return nil, fmt.Errorf("search filters chat/talker/after/before/type/kind_name/base_kind/sender require cache index; run `wx-mcp cache refresh` first")
	}
	limit := getInt(a, "limit", 20)
	like := "%" + kw + "%"

	// Use FTS content tables (85万条索引, single DB) — much faster than scanning Msg_* tables.
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
	for _, r := range n2iRows {
		if rid, ok := r["rid"].(int64); ok {
			if u, ok := r["username"].(string); ok {
				idToTalker[rid] = u
			}
		}
	}

	// UNION ALL across 4 FTS content partitions then global ORDER BY.
	// Previous impl looped 0..3 and early-stopped when len(results) >= limit,
	// which could miss newer messages living in later partitions.
	// c0=text, c1=local_id, c2=sort_seq, c4=session_id, c6=create_time
	query := `SELECT * FROM (
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_0_content WHERE c0 LIKE ?
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_1_content WHERE c0 LIKE ?
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_2_content WHERE c0 LIKE ?
		UNION ALL
		SELECT c0 AS content, c1 AS local_id, c4 AS session_id, c6 AS create_time FROM message_fts_v4_3_content WHERE c0 LIKE ?
	) ORDER BY create_time DESC LIMIT ?`
	rows, err := db.Query(query, like, like, like, like, limit)
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
	return rows, nil
}

func searchMode(a map[string]any) string {
	mode := getStr(a, "search_mode")
	if mode == "" {
		return "fts"
	}
	return mode
}

func searchHasCacheOnlyFilters(a map[string]any) bool {
	for _, k := range []string{"talker", "chat", "after", "before", "type", "kind_name", "sender"} {
		if getStr(a, k) != "" {
			return true
		}
	}
	return getInt(a, "base_kind", 0) != 0
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
		msgDB, err := s.findMsgDB(tableName)
		if err != nil {
			continue
		}
		n2i, _ := loadName2Id(msgDB)
		ph := make([]string, len(lids))
		args := make([]any, len(lids))
		for i, lid := range lids {
			ph[i] = "?"
			args[i] = lid
		}
		metaRows, qerr := msgDB.Query(fmt.Sprintf(
			"SELECT local_id, real_sender_id, local_type FROM %s WHERE local_id IN (%s)",
			tableName, strings.Join(ph, ",")), args...)
		msgDB.Close()
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
			return nil, fmt.Errorf("chat filter requires cache index; run `wx-mcp cache refresh` first: %w", err)
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
			return nil, fmt.Errorf("sender filter requires cache index; run `wx-mcp cache refresh` first: %w", err)
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
	if needsMessageMeta {
		if _, err := openCache(); err != nil {
			return nil, fmt.Errorf("red_packets time filters require cache index; run `wx-mcp cache refresh` first: %w", err)
		}
	}
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
		meta := cacheMessageMeta(cacheDB, rows, "message_server_id", "session_username")
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
		case "new_messages", "sql":
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

type xmlMsgAppmsg struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		Title    string          `xml:"title"`
		Des      string          `xml:"des"`
		URL      string          `xml:"url"`
		Type     int             `xml:"type"`
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
// (talker, server_id) pairs. Groups by talker → routes to the matching
// Msg_<hash> shard → single IN query per talker. Returns server_id → content
// map; entries missing means lookup failed (table not found / row gone) and
// caller should treat the enrichment field as absent rather than error.
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
		msgDB, err := s.findMsgDB(tableName)
		if err != nil {
			continue
		}
		ph := make([]string, len(sids))
		args := make([]any, len(sids))
		for i, sid := range sids {
			ph[i] = "?"
			args[i] = sid
		}
		contentRows, qerr := msgDB.Query(fmt.Sprintf(
			"SELECT server_id, message_content FROM %s WHERE server_id IN (%s)",
			tableName, strings.Join(ph, ",")), args...)
		msgDB.Close()
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
	return out
}

func messagePairKey(talker string, serverID int64) string {
	return talker + "\x00" + strconv.FormatInt(serverID, 10)
}

func cacheMessageMeta(db *wcdb.DB, rows []wcdb.Row, sidCol, talkerCol string) map[string]wcdb.Row {
	out := map[string]wcdb.Row{}
	if db == nil || len(rows) == 0 {
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
	for talker, sids := range byTalker {
		ph := make([]string, len(sids))
		args := make([]any, 0, len(sids)+1)
		args = append(args, talker)
		for i, sid := range sids {
			ph[i] = "?"
			args = append(args, sid)
		}
		metaRows, err := db.Query(fmt.Sprintf(`SELECT talker, server_id, create_time, create_time_human,
			sender_wxid, sender_display_name
			FROM messages_unified
			WHERE talker = ? AND server_id IN (%s)`, strings.Join(ph, ",")), args...)
		if err != nil {
			continue
		}
		for _, mr := range metaRows {
			out[messagePairKey(rowString(mr, "talker"), rowInt64(mr, "server_id"))] = mr
		}
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
	case 49:
		var m xmlMsgAppmsg
		if err := xml.Unmarshal([]byte(xmlStr), &m); err != nil {
			return map[string]any{"parse_error": err.Error(), "kind": "app"}
		}
		out := map[string]any{
			"app_subtype": m.AppMsg.Type,
			"title":       m.AppMsg.Title,
			"des":         m.AppMsg.Des,
			"url":         m.AppMsg.URL,
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
			if parsed := parseMessageContent(baseKind, subtype, content, 3); parsed != nil {
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
		return "[语音]"
	case 43:
		return "[视频]"
	case 47:
		return "[表情]"
	case 49:
		p, _ := parsed.(map[string]any)
		if p == nil {
			return "[应用消息]"
		}
		title, _ := p["title"].(string)
		if subtype == 57 {
			quoted := "..."
			if r, ok := p["refermsg"].(map[string]any); ok {
				refType := int32(0)
				if t, ok := r["type"].(int); ok {
					refType = int32(t)
				}
				refRaw, _ := r["content_raw"].(string)
				quoted = contentSummary(refType, 0, refRaw, r["content_parsed"])
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
