package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r266-tech/wx-mcp/internal/config"
	"github.com/r266-tech/wx-mcp/internal/wcdb"
	"github.com/r266-tech/wx-mcp/internal/wxkind"
)

var errCacheMissing = errors.New("cache index missing; run cache_refresh first")

type cachePaths struct {
	RootDir   string `json:"root_dir"`
	RawDir    string `json:"raw_dir"`
	IndexPath string `json:"index_path"`
}

type sourceDBInfo struct {
	RelPath  string
	Subdir   string
	File     string
	Source   string
	Snapshot string
	DBMTime  int64
	WALMTime int64
	SaltHex  string
}

type cacheFileMeta struct {
	RelPath    string `json:"rel_path"`
	SourcePath string `json:"source_path"`
	Snapshot   string `json:"snapshot_path"`
	DBMTime    int64  `json:"db_mtime"`
	WALMTime   int64  `json:"wal_mtime"`
	SaltHex    string `json:"salt_hex,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	CopiedAt   int64  `json:"copied_at,omitempty"`
	Reused     bool   `json:"reused,omitempty"`
}

func (s *server) activeConfigNoSetup() (*config.Config, error) {
	if s.cfg != nil {
		return s.cfg, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.DBRoot == "" {
		if root, wxid, err := config.AutoDetectDBRoot(); err == nil {
			cfg.DBRoot = root
			if cfg.Wxid == "" {
				cfg.Wxid = wxid
			}
		}
	}
	return cfg, nil
}

func cachePathsFor(cfg *config.Config) (cachePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return cachePaths{}, err
	}
	id := cfg.Wxid
	if id == "" {
		sum := md5.Sum([]byte(cfg.DBRoot))
		id = "root-" + hex.EncodeToString(sum[:8])
	}
	id = safeCacheID(id)
	root := filepath.Join(home, ".wx-mcp", "cache", id)
	return cachePaths{
		RootDir:   root,
		RawDir:    filepath.Join(root, "raw"),
		IndexPath: filepath.Join(root, "index.sqlite"),
	}, nil
}

func (s *server) cachePaths() (cachePaths, error) {
	if err := s.ensure(); err != nil {
		return cachePaths{}, err
	}
	return cachePathsFor(s.cfg)
}

var cacheIDRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func safeCacheID(s string) string {
	s = strings.Trim(cacheIDRe.ReplaceAllString(s, "_"), "._-")
	if s == "" {
		return "default"
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func (s *server) openCacheIndex(writable bool) (*wcdb.DB, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if err := wcdb.Bootstrap(s.wcdbPath); err != nil {
		return nil, err
	}
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	if !writable {
		if _, err := os.Stat(paths.IndexPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errCacheMissing
			}
			return nil, err
		}
	}
	return wcdb.OpenPlain(paths.IndexPath, writable)
}

func (s *server) toolCacheStatus(a map[string]any) (any, error) {
	cfg, err := s.activeConfigNoSetup()
	if err != nil {
		return nil, err
	}
	paths, err := cachePathsFor(cfg)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"cache_root": paths.RootDir,
		"raw_dir":    paths.RawDir,
		"index_path": paths.IndexPath,
		"exists":     fileExists(paths.IndexPath),
	}
	if cfg.DBRoot != "" {
		if dbs, err := listSourceDBs(cfg, paths); err == nil {
			out["source_db_count"] = len(dbs)
		}
	}
	if !fileExists(paths.IndexPath) {
		return out, nil
	}
	wcdbPath, err := findWCDB()
	if err != nil {
		out["index_error"] = err.Error()
		return out, nil
	}
	if err := wcdb.Bootstrap(wcdbPath); err != nil {
		out["index_error"] = err.Error()
		return out, nil
	}
	db, err := wcdb.OpenPlain(paths.IndexPath, false)
	if err != nil {
		out["index_error"] = err.Error()
		return out, nil
	}
	defer db.Close()
	out["tables"] = map[string]int64{
		"cache_files":      countTable(db, "cache_files"),
		"contacts_unified": countTable(db, "contacts_unified"),
		"sessions_unified": countTable(db, "sessions_unified"),
		"messages_unified": countTable(db, "messages_unified"),
		"message_fts":      countTable(db, "message_fts"),
		"stats_talkers":    countTable(db, "stats_talkers"),
		"stats_senders":    countTable(db, "stats_senders"),
		"stats_kind":       countTable(db, "stats_kind"),
		"stats_daily":      countTable(db, "stats_daily"),
	}
	if rows, err := db.Query("SELECT key, value FROM cache_meta ORDER BY key"); err == nil {
		meta := map[string]string{}
		for _, r := range rows {
			meta[rowString(r, "key")] = rowString(r, "value")
		}
		out["meta"] = meta
	}
	return out, nil
}

func (s *server) toolCacheRefresh(a map[string]any) (any, error) {
	unlock, acquired, lockPath, err := acquireCacheRefreshLock()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return map[string]any{
			"skipped": true,
			"reason":  "cache refresh already running",
			"lock":    lockPath,
		}, nil
	}
	defer unlock()
	return s.refreshCache(getBool(a, "force"))
}

func (s *server) toolCacheRebuild(a map[string]any) (any, error) {
	unlock, acquired, lockPath, err := acquireCacheRefreshLock()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("cache refresh already running (lock: %s)", lockPath)
	}
	defer unlock()
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(paths.RootDir); err != nil {
		return nil, err
	}
	return s.refreshCache(true)
}

func acquireCacheRefreshLock() (func(), bool, string, error) {
	if os.Getenv("WX_MCP_CACHE_LOCK_HELD") != "" {
		return func() {}, true, "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, "", err
	}
	stateDir := filepath.Join(home, ".wx-mcp")
	lockDir := filepath.Join(stateDir, "cache-refresh.lock")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, false, lockDir, err
	}
	if err := os.Mkdir(lockDir, 0o700); err == nil {
		return func() { _ = os.Remove(lockDir) }, true, lockDir, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, false, lockDir, err
	}
	if st, err := os.Stat(lockDir); err == nil && time.Since(st.ModTime()) > 2*time.Hour {
		_ = os.Remove(lockDir)
		if err := os.Mkdir(lockDir, 0o700); err == nil {
			return func() { _ = os.Remove(lockDir) }, true, lockDir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, false, lockDir, err
		}
	}
	return nil, false, lockDir, nil
}

func (s *server) refreshCache(force bool) (any, error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	if err := wcdb.Bootstrap(s.wcdbPath); err != nil {
		return nil, err
	}
	paths, err := s.cachePaths()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.RawDir, 0o700); err != nil {
		return nil, err
	}
	sources, err := listSourceDBs(s.cfg, paths)
	if err != nil {
		return nil, err
	}
	prev := loadCacheFileMeta(paths.IndexPath)
	metas := make([]cacheFileMeta, 0, len(sources))
	stats := map[string]int64{"source_dbs": int64(len(sources))}
	var errorsOut []map[string]string

	for _, src := range sources {
		meta := cacheFileMeta{
			RelPath:    src.RelPath,
			SourcePath: src.Source,
			Snapshot:   src.Snapshot,
			DBMTime:    src.DBMTime,
			WALMTime:   src.WALMTime,
			SaltHex:    src.SaltHex,
			Status:     "ok",
		}
		if strings.HasSuffix(src.File, "_fts.db") {
			meta.Status = "skipped"
			meta.Error = "source FTS DB uses WeChat custom tokenizer; wx-mcp builds its own message_fts in index.sqlite"
			stats["skipped_fts"]++
			metas = append(metas, meta)
			continue
		}
		if old, ok := prev[src.RelPath]; ok && !force &&
			old.DBMTime == src.DBMTime && old.WALMTime == src.WALMTime &&
			old.SaltHex == src.SaltHex && fileExists(src.Snapshot) {
			meta.CopiedAt = old.CopiedAt
			meta.Reused = true
			stats["reused"]++
			metas = append(metas, meta)
			continue
		}
		db, err := s.openDBWritable(src.Subdir, src.File)
		if err != nil {
			meta.Status = "error"
			meta.Error = err.Error()
			stats["snapshot_errors"]++
			errorsOut = append(errorsOut, map[string]string{"rel_path": src.RelPath, "error": err.Error()})
			metas = append(metas, meta)
			continue
		}
		if err := db.BackupTo(src.Snapshot); err != nil {
			meta.Status = "error"
			meta.Error = err.Error()
			stats["snapshot_errors"]++
			errorsOut = append(errorsOut, map[string]string{"rel_path": src.RelPath, "error": err.Error()})
			db.Close()
			metas = append(metas, meta)
			continue
		}
		db.Close()
		meta.CopiedAt = time.Now().Unix()
		stats["snapshotted"]++
		metas = append(metas, meta)
	}

	indexStats, err := s.buildCacheIndex(paths, metas)
	if err != nil {
		return nil, err
	}
	for k, v := range indexStats {
		stats[k] = v
	}
	return map[string]any{
		"cache_root": paths.RootDir,
		"index_path": paths.IndexPath,
		"force":      force,
		"stats":      stats,
		"errors":     errorsOut,
	}, nil
}

func (s *server) openDBWritable(subdir, file string) (*wcdb.DB, error) {
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
		return wcdb.OpenWithKeyMapWritable(resolvedDB, s.cfg.Keys, s.cfg.Key)
	}
	return wcdb.OpenWritable(resolvedDB, s.cfg.Key)
}

func listSourceDBs(cfg *config.Config, paths cachePaths) ([]sourceDBInfo, error) {
	if cfg.DBRoot == "" {
		return nil, fmt.Errorf("db_root is empty")
	}
	dbStorage := filepath.Join(cfg.DBRoot, "db_storage")
	var out []sourceDBInfo
	err := filepath.WalkDir(dbStorage, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".db" || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			return nil
		}
		rel, err := filepath.Rel(dbStorage, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		subdir, file := filepath.Split(rel)
		subdir = strings.TrimSuffix(subdir, "/")
		out = append(out, sourceDBInfo{
			RelPath:  rel,
			Subdir:   subdir,
			File:     file,
			Source:   path,
			Snapshot: filepath.Join(paths.RawDir, filepath.FromSlash(rel)),
			DBMTime:  fileMTimeNanos(path),
			WALMTime: fileMTimeNanos(path + "-wal"),
			SaltHex:  readSaltHex(path),
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, err
}

func loadCacheFileMeta(indexPath string) map[string]cacheFileMeta {
	out := map[string]cacheFileMeta{}
	if !fileExists(indexPath) {
		return out
	}
	db, err := wcdb.OpenPlain(indexPath, false)
	if err != nil {
		return out
	}
	defer db.Close()
	rows, err := db.Query(`SELECT rel_path, source_path, snapshot_path, db_mtime, wal_mtime, salt_hex, status, error, copied_at FROM cache_files`)
	if err != nil {
		return out
	}
	for _, r := range rows {
		m := cacheFileMeta{
			RelPath:    rowString(r, "rel_path"),
			SourcePath: rowString(r, "source_path"),
			Snapshot:   rowString(r, "snapshot_path"),
			DBMTime:    rowInt64(r, "db_mtime"),
			WALMTime:   rowInt64(r, "wal_mtime"),
			SaltHex:    rowString(r, "salt_hex"),
			Status:     rowString(r, "status"),
			Error:      rowString(r, "error"),
			CopiedAt:   rowInt64(r, "copied_at"),
		}
		out[m.RelPath] = m
	}
	return out
}

func (s *server) buildCacheIndex(paths cachePaths, files []cacheFileMeta) (map[string]int64, error) {
	tmp := paths.IndexPath + ".tmp"
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	db, err := wcdb.OpenPlain(tmp, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := createIndexSchema(db); err != nil {
		return nil, err
	}
	if err := db.Exec("BEGIN"); err != nil {
		return nil, err
	}
	stats := map[string]int64{}
	for _, f := range files {
		_ = db.ExecArgs(`INSERT INTO cache_files
			(rel_path, source_path, snapshot_path, db_mtime, wal_mtime, salt_hex, status, error, copied_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.RelPath, f.SourcePath, f.Snapshot, f.DBMTime, f.WALMTime, f.SaltHex, f.Status, f.Error, f.CopiedAt)
	}
	display, contactCount := buildIndexContacts(db, snapshotFor(files, "contact/contact.db"))
	stats["contacts"] = contactCount
	tableToTalker, sessionCount := buildIndexSessions(db, snapshotFor(files, "session/session.db"), display)
	stats["sessions"] = sessionCount
	for u := range display {
		tableToTalker["Msg_"+talkerHash(u)] = u
	}
	msgCount, msgErrs := s.buildIndexMessages(db, files, display, tableToTalker)
	stats["message_rows_seen"] = msgCount
	stats["messages"] = countTable(db, "messages_unified")
	stats["message_errors"] = int64(len(msgErrs))
	ftsReady := buildIndexFTS(db)
	if ftsReady {
		stats["fts_ready"] = 1
	}
	buildIndexStats(db)
	_ = setCacheMeta(db, "refreshed_at", strconv.FormatInt(time.Now().Unix(), 10))
	_ = setCacheMeta(db, "fts_ready", strconv.FormatBool(ftsReady))
	if len(msgErrs) > 0 {
		b, _ := json.Marshal(msgErrs)
		_ = setCacheMeta(db, "message_errors", string(b))
	}
	if err := db.Exec("COMMIT"); err != nil {
		_ = db.Exec("ROLLBACK")
		return nil, err
	}
	db.Close()
	_ = os.Remove(paths.IndexPath)
	_ = os.Remove(paths.IndexPath + "-wal")
	_ = os.Remove(paths.IndexPath + "-shm")
	if err := os.Rename(tmp, paths.IndexPath); err != nil {
		return nil, err
	}
	return stats, nil
}

func createIndexSchema(db *wcdb.DB) error {
	return db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
		CREATE TABLE cache_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE cache_files (
			rel_path TEXT PRIMARY KEY,
			source_path TEXT,
			snapshot_path TEXT,
			db_mtime INTEGER,
			wal_mtime INTEGER,
			salt_hex TEXT,
			status TEXT,
			error TEXT,
			copied_at INTEGER
		);
		CREATE TABLE contacts_unified (
			username TEXT PRIMARY KEY,
			display_name TEXT,
			nick_name TEXT,
			remark TEXT,
			alias TEXT,
			description TEXT,
			type TEXT,
			is_verified INTEGER
		);
		CREATE TABLE sessions_unified (
			username TEXT PRIMARY KEY,
			display_name TEXT,
			unread_count INTEGER,
			summary TEXT,
			last_timestamp INTEGER,
			sort_timestamp INTEGER,
			last_sender_wxid TEXT,
			last_sender_display_name TEXT,
			last_msg_type INTEGER,
			last_msg_sub_type INTEGER,
			last_msg_kind_name TEXT
		);
		CREATE TABLE messages_unified (
			talker TEXT,
			talker_display_name TEXT,
			local_id INTEGER,
			server_id INTEGER,
			create_time INTEGER,
			create_time_human TEXT,
			sort_seq INTEGER,
			sender_wxid TEXT,
			sender_display_name TEXT,
			is_from_me INTEGER,
			base_kind INTEGER,
			subtype INTEGER,
			kind_name TEXT,
			content_summary TEXT,
			message_content TEXT,
			parsed_json TEXT,
			source_db TEXT,
			source_table TEXT,
			PRIMARY KEY (talker, local_id)
		);
		CREATE INDEX idx_messages_talker_sort ON messages_unified(talker, sort_seq DESC);
		CREATE INDEX idx_messages_talker_time ON messages_unified(talker, create_time DESC);
		CREATE INDEX idx_messages_talker_kind ON messages_unified(talker, kind_name, create_time DESC);
		CREATE INDEX idx_messages_talker_sender ON messages_unified(talker, sender_wxid, create_time DESC);
		CREATE INDEX idx_messages_create_time ON messages_unified(create_time DESC);
		CREATE INDEX idx_messages_sender ON messages_unified(sender_wxid);
		CREATE INDEX idx_messages_kind ON messages_unified(kind_name);
		CREATE INDEX idx_sessions_sort ON sessions_unified(sort_timestamp DESC);
	`)
}

func buildIndexContacts(idx *wcdb.DB, path string) (map[string]string, int64) {
	display := map[string]string{}
	if path == "" || !fileExists(path) {
		return display, 0
	}
	db, err := wcdb.OpenPlain(path, false)
	if err != nil {
		return display, 0
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username, alias, remark, nick_name,
		COALESCE(NULLIF(remark, ''), NULLIF(nick_name, ''), username) AS display_name,
		description, verify_flag FROM contact`)
	if err != nil {
		return display, 0
	}
	var n int64
	for _, r := range rows {
		u := rowString(r, "username")
		if u == "" {
			continue
		}
		dn := rowString(r, "display_name")
		display[u] = dn
		_ = idx.ExecArgs(`INSERT OR REPLACE INTO contacts_unified
			(username, display_name, nick_name, remark, alias, description, type, is_verified)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			u, dn, rowString(r, "nick_name"), rowString(r, "remark"), rowString(r, "alias"),
			rowString(r, "description"), wxkind.ClassifyUsername(u), rowInt64(r, "verify_flag") != 0)
		n++
	}
	return display, n
}

func buildIndexSessions(idx *wcdb.DB, path string, display map[string]string) (map[string]string, int64) {
	tableToTalker := map[string]string{}
	if path == "" || !fileExists(path) {
		return tableToTalker, 0
	}
	db, err := wcdb.OpenPlain(path, false)
	if err != nil {
		return tableToTalker, 0
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username, unread_count, summary,
		last_timestamp, sort_timestamp,
		last_msg_sender AS last_sender_wxid, last_sender_display_name,
		last_msg_type, last_msg_sub_type
		FROM SessionTable
		WHERE COALESCE(is_hidden, 0) = 0`)
	if err != nil {
		return tableToTalker, 0
	}
	var n int64
	for _, r := range rows {
		u := rowString(r, "username")
		if u == "" {
			continue
		}
		tableToTalker["Msg_"+talkerHash(u)] = u
		dn := displayOrRaw(display, u)
		sender := stripAggSenderPrefix(rowString(r, "last_sender_wxid"))
		bk := rowInt64(r, "last_msg_type")
		st := rowInt64(r, "last_msg_sub_type")
		kind := wxkind.Resolve(int32(bk), int32(st))
		_ = idx.ExecArgs(`INSERT OR REPLACE INTO sessions_unified
			(username, display_name, unread_count, summary, last_timestamp, sort_timestamp,
			 last_sender_wxid, last_sender_display_name, last_msg_type, last_msg_sub_type, last_msg_kind_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u, dn, rowInt64(r, "unread_count"), rowString(r, "summary"), rowInt64(r, "last_timestamp"),
			rowInt64(r, "sort_timestamp"), emptyToNil(sender), emptyToNil(rowString(r, "last_sender_display_name")),
			bk, st, kind)
		n++
	}
	return tableToTalker, n
}

func (s *server) buildIndexMessages(idx *wcdb.DB, files []cacheFileMeta, display map[string]string, tableToTalker map[string]string) (int64, []map[string]string) {
	var total int64
	var errs []map[string]string
	self := s.selfWxid()
	for _, f := range files {
		if f.Status != "ok" || !strings.HasPrefix(f.RelPath, "message/") || !fileExists(f.Snapshot) {
			continue
		}
		db, err := wcdb.OpenPlain(f.Snapshot, false)
		if err != nil {
			errs = append(errs, map[string]string{"rel_path": f.RelPath, "error": err.Error()})
			continue
		}
		n2i, _ := loadName2Id(db)
		tables, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'Msg_%'")
		if err != nil {
			errs = append(errs, map[string]string{"rel_path": f.RelPath, "error": err.Error()})
			db.Close()
			continue
		}
		for _, tr := range tables {
			table := rowString(tr, "name")
			talker := tableToTalker[table]
			if talker == "" || !validMsgTable(table) {
				continue
			}
			for offset := 0; ; offset += 1000 {
				rows, err := db.Query(fmt.Sprintf(`SELECT local_id, server_id, local_type, sort_seq,
					real_sender_id, create_time, status, message_content, source
					FROM %s ORDER BY sort_seq DESC LIMIT ? OFFSET ?`, quoteIdent(table)), 1000, offset)
				if err != nil {
					errs = append(errs, map[string]string{"rel_path": f.RelPath, "table": table, "error": err.Error()})
					break
				}
				if len(rows) == 0 {
					break
				}
				if n2i != nil {
					rows = resolveSenders(rows, n2i)
				}
				rows = enrichMessages(decodeFields(rows, "message_content", "source"))
				for _, r := range rows {
					sender := rowString(r, "sender_wxid")
					senderDN := displayOrRaw(display, sender)
					parsedJSON := ""
					if p, ok := r["message_content_parsed"]; ok && p != nil {
						if b, err := json.Marshal(p); err == nil {
							parsedJSON = string(b)
						}
					}
					isFromMe := self != "" && sender == self
					if err := idx.ExecArgs(`INSERT OR REPLACE INTO messages_unified
						(talker, talker_display_name, local_id, server_id, create_time, create_time_human,
						 sort_seq, sender_wxid, sender_display_name, is_from_me, base_kind, subtype,
						 kind_name, content_summary, message_content, parsed_json, source_db, source_table)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						talker, displayOrRaw(display, talker), rowInt64(r, "local_id"), rowInt64(r, "server_id"),
						rowInt64(r, "create_time"), rowString(r, "create_time_human"), rowInt64(r, "sort_seq"),
						emptyToNil(sender), emptyToNil(senderDN), isFromMe, rowInt64(r, "base_kind"),
						rowInt64(r, "subtype"), rowString(r, "kind_name"), rowString(r, "content_summary"),
						rowString(r, "message_content"), emptyToNil(parsedJSON), f.RelPath, table); err != nil {
						errs = append(errs, map[string]string{"rel_path": f.RelPath, "table": table, "error": err.Error()})
					} else {
						total++
					}
				}
			}
		}
		db.Close()
	}
	return total, errs
}

func buildIndexFTS(db *wcdb.DB) bool {
	if err := db.Exec(`CREATE VIRTUAL TABLE message_fts USING fts5(content_summary, message_content, talker, sender_wxid)`); err != nil {
		return false
	}
	return db.Exec(`INSERT INTO message_fts(rowid, content_summary, message_content, talker, sender_wxid)
		SELECT rowid, COALESCE(content_summary, ''), COALESCE(message_content, ''), COALESCE(talker, ''), COALESCE(sender_wxid, '')
		FROM messages_unified`) == nil
}

func buildIndexStats(db *wcdb.DB) {
	_ = db.Exec(`
		CREATE TABLE stats_talkers AS
			SELECT talker, talker_display_name, COUNT(*) AS msg_count, MAX(create_time) AS last_time
			FROM messages_unified GROUP BY talker;
		CREATE INDEX idx_stats_talkers_count ON stats_talkers(msg_count DESC);
		CREATE TABLE stats_senders AS
			SELECT sender_wxid, sender_display_name, COUNT(*) AS msg_count, MAX(create_time) AS last_time
			FROM messages_unified
			WHERE sender_wxid IS NOT NULL AND sender_wxid != ''
			GROUP BY sender_wxid;
		CREATE INDEX idx_stats_senders_count ON stats_senders(msg_count DESC);
		CREATE TABLE stats_kind AS
			SELECT kind_name, base_kind, COUNT(*) AS msg_count
			FROM messages_unified GROUP BY kind_name, base_kind;
		CREATE INDEX idx_stats_kind_count ON stats_kind(msg_count DESC);
		CREATE TABLE stats_daily AS
			SELECT strftime('%Y-%m-%d', create_time, 'unixepoch', 'localtime') AS day, COUNT(*) AS msg_count
			FROM messages_unified GROUP BY day;
		CREATE INDEX idx_stats_daily_day ON stats_daily(day DESC);
	`)
}

func setCacheMeta(db *wcdb.DB, key, value string) error {
	return db.ExecArgs("INSERT OR REPLACE INTO cache_meta(key, value) VALUES (?, ?)", key, value)
}

func snapshotFor(files []cacheFileMeta, rel string) string {
	for _, f := range files {
		if f.RelPath == rel && f.Status == "ok" && fileExists(f.Snapshot) {
			return f.Snapshot
		}
	}
	return ""
}

func (s *server) cacheSessions(a map[string]any) ([]wcdb.Row, bool, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		if errors.Is(err, errCacheMissing) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer db.Close()
	var where []string
	var args []any
	if kw := getStr(a, "keyword"); kw != "" {
		where = append(where, "(s.username LIKE ? COLLATE NOCASE OR s.summary LIKE ? COLLATE NOCASE OR s.display_name LIKE ? COLLATE NOCASE)")
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	limit := getInt(a, "limit", 50)
	fetchLimit := limit
	if getStr(a, "type_filter") != "" && getStr(a, "type_filter") != "all" {
		fetchLimit = 2000
	}
	args = append(args, fetchLimit)
	rows, err := db.Query(fmt.Sprintf(`SELECT s.username, s.display_name, s.unread_count, s.summary, s.last_timestamp, s.sort_timestamp,
		s.last_sender_wxid, s.last_sender_display_name, s.last_msg_type, s.last_msg_sub_type, s.last_msg_kind_name,
		c.type AS contact_type, c.is_verified
		FROM sessions_unified s LEFT JOIN contacts_unified c ON c.username = s.username
		%s ORDER BY s.sort_timestamp DESC LIMIT ?`, wc), args...)
	if err != nil {
		return nil, false, err
	}
	rows = decorateSessionRows(rows, getStr(a, "type_filter"))
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, true, nil
}

func (s *server) cacheMessages(a map[string]any) ([]wcdb.Row, bool, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		if errors.Is(err, errCacheMissing) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer db.Close()
	talker, err := resolveTalkerForCache(db, a, true)
	if err != nil {
		return nil, true, err
	}
	if aggregatorSessions[talker] {
		return nil, true, fmt.Errorf("%q 是订阅号合集入口 (UI 聚合 session), 本身无消息表. 真实消息在各 gh_* 公众号下, 按具体 gh_<id> 查", talker)
	}
	a["talker"] = talker
	rows, err := queryCacheMessages(db, a, "sort_seq DESC")
	if err != nil {
		return nil, false, err
	}
	normalizeCacheMessages(rows)
	mode := getStr(a, "fields")
	if mode == "" {
		mode = "lite"
	}
	if mode != "lite" && mode != "full" {
		return nil, true, fmt.Errorf("invalid fields=%q: must be \"lite\" or \"full\"", mode)
	}
	return liteMessages(rows, mode), true, nil
}

func (s *server) cacheSearch(a map[string]any) ([]wcdb.Row, bool, error) {
	kw := getStr(a, "keyword")
	if kw == "" {
		return nil, true, fmt.Errorf("keyword is required")
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		if errors.Is(err, errCacheMissing) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer db.Close()
	talker, err := resolveTalkerForCache(db, a, false)
	if err != nil {
		return nil, true, err
	}
	if talker != "" {
		a["talker"] = talker
	}
	limit := getInt(a, "limit", 20)
	searchFilters := map[string]any{}
	for k, v := range a {
		searchFilters[k] = v
	}
	delete(searchFilters, "keyword")
	where, args, err := cacheMessageWhere(db, searchFilters, "m")
	if err != nil {
		return nil, true, err
	}
	where = append([]string{"message_fts MATCH ?"}, where...)
	args = append([]any{ftsPhrase(kw)}, args...)
	args = append(args, limit)
	rows, err := db.Query(fmt.Sprintf(`SELECT m.local_id, m.talker, m.talker_display_name, m.create_time,
		m.sender_wxid, m.sender_display_name, m.base_kind, m.kind_name,
		COALESCE(NULLIF(m.content_summary, ''), m.message_content) AS content,
		c.type AS talker_contact_type, c.is_verified AS talker_is_verified
		FROM message_fts f JOIN messages_unified m ON m.rowid = f.rowid
		LEFT JOIN contacts_unified c ON c.username = m.talker
		WHERE %s ORDER BY m.create_time DESC LIMIT ?`, strings.Join(where, " AND ")), args...)
	if err != nil || len(rows) == 0 {
		like := "%" + kw + "%"
		where, args, err = cacheMessageWhere(db, searchFilters, "")
		if err != nil {
			return nil, true, err
		}
		where = append([]string{"(content_summary LIKE ? OR message_content LIKE ?)"}, where...)
		args = append([]any{like, like}, args...)
		args = append(args, limit)
		rows, err = db.Query(fmt.Sprintf(`SELECT m.local_id, m.talker, m.talker_display_name, m.create_time,
			sender_wxid, sender_display_name, base_kind, kind_name,
			COALESCE(NULLIF(content_summary, ''), message_content) AS content,
			c.type AS talker_contact_type, c.is_verified AS talker_is_verified
			FROM messages_unified m LEFT JOIN contacts_unified c ON c.username = m.talker
			WHERE %s ORDER BY create_time DESC LIMIT ?`, strings.Join(where, " AND ")), args...)
	}
	if err != nil {
		return nil, false, err
	}
	decorateMessageRows(rows)
	for _, r := range rows {
		if c := rowString(r, "content"); c != "" {
			r["content"] = senderPrefixRe.ReplaceAllString(c, "")
		}
	}
	return rows, true, nil
}

func (s *server) toolUnread(a map[string]any) (any, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		if errors.Is(err, errCacheMissing) {
			raw, err := s.toolSessions(map[string]any{"limit": float64(getInt(a, "limit", 1000))})
			if err != nil {
				return nil, err
			}
			rows, ok := raw.([]wcdb.Row)
			if !ok {
				return nil, fmt.Errorf("sessions returned unexpected type %T", raw)
			}
			var out []wcdb.Row
			for _, r := range rows {
				if rowInt64(r, "unread_count") > 0 {
					out = append(out, r)
				}
			}
			return out, nil
		}
		return nil, err
	}
	defer db.Close()
	limit := getInt(a, "limit", 50)
	fetchLimit := limit
	if getStr(a, "type_filter") != "" || getStr(a, "filter") != "" {
		fetchLimit = 2000
	}
	tf := getStr(a, "type_filter")
	if tf == "" {
		tf = getStr(a, "filter")
	}
	rows, err := db.Query(`SELECT s.username, s.display_name, s.unread_count, s.summary, s.last_timestamp, s.sort_timestamp,
		s.last_sender_wxid, s.last_sender_display_name, s.last_msg_type, s.last_msg_sub_type, s.last_msg_kind_name,
		c.type AS contact_type, c.is_verified
		FROM sessions_unified s LEFT JOIN contacts_unified c ON c.username = s.username
		WHERE s.unread_count > 0 ORDER BY s.sort_timestamp DESC LIMIT ?`, fetchLimit)
	if err != nil {
		return nil, err
	}
	rows = decorateSessionRows(rows, tf)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *server) toolNewMessages(a map[string]any) (any, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	talker, err := resolveTalkerForCache(db, a, false)
	if err != nil {
		return nil, err
	}
	if talker != "" {
		a["talker"] = talker
	}
	limit := getInt(a, "limit", 100)
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var where []string
	var args []any
	if talker := getStr(a, "talker"); talker != "" {
		where = append(where, "talker = ?")
		args = append(args, talker)
	}
	ts, rowid, err := parseCacheCursor(getStr(a, "cursor"))
	if err != nil {
		return nil, err
	}
	if ts == 0 {
		ts, err = parseTS(getStr(a, "after"))
		if err != nil {
			return nil, err
		}
	}
	if ts > 0 {
		where = append(where, "(m.create_time > ? OR (m.create_time = ? AND m.rowid > ?))")
		args = append(args, ts, ts, rowid)
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	rows, err := db.Query(fmt.Sprintf(`SELECT m.rowid AS cache_rowid, m.talker, m.talker_display_name, m.local_id, m.server_id,
		m.create_time, m.create_time_human, m.sender_wxid, m.sender_display_name, m.is_from_me, m.base_kind, m.kind_name,
		m.content_summary, m.message_content, m.parsed_json,
		c.type AS talker_contact_type, c.is_verified AS talker_is_verified
		FROM messages_unified m LEFT JOIN contacts_unified c ON c.username = m.talker
		%s ORDER BY m.create_time ASC, m.rowid ASC LIMIT ?`, wc), args...)
	if err != nil {
		return nil, err
	}
	normalizeCacheMessages(rows)
	next := ""
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		next = fmt.Sprintf("%d:%d", rowInt64(last, "create_time"), rowInt64(last, "cache_rowid"))
	}
	return map[string]any{"messages": liteMessages(rows, getFieldsMode(a)), "next_cursor": next}, nil
}

func (s *server) toolStats(a map[string]any) (any, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	talker, err := resolveTalkerForCache(db, a, false)
	if err != nil {
		return nil, err
	}
	limit := getInt(a, "limit", 10)
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	out := map[string]any{}
	if talker != "" {
		a["talker"] = talker
		where, args, err := cacheMessageWhere(db, a, "")
		if err != nil {
			return nil, err
		}
		wc := ""
		if len(where) > 0 {
			wc = "WHERE " + strings.Join(where, " AND ")
		}
		out["scope"] = map[string]any{"talker": talker}
		out["messages"] = countWhere(db, "messages_unified", wc, args...)
		argsLimit := append(append([]any{}, args...), limit)
		out["by_sender"], _ = db.Query(fmt.Sprintf(`SELECT sender_wxid, sender_display_name, COUNT(*) AS msg_count, MAX(create_time) AS last_time
			FROM messages_unified %s GROUP BY sender_wxid, sender_display_name ORDER BY msg_count DESC LIMIT ?`, wc), argsLimit...)
		out["by_kind"], _ = db.Query(fmt.Sprintf(`SELECT kind_name, base_kind, COUNT(*) AS msg_count
			FROM messages_unified %s GROUP BY kind_name, base_kind ORDER BY msg_count DESC LIMIT ?`, wc), argsLimit...)
		out["daily"], _ = db.Query(fmt.Sprintf(`SELECT strftime('%%Y-%%m-%%d', create_time, 'unixepoch', 'localtime') AS day, COUNT(*) AS msg_count
			FROM messages_unified %s GROUP BY day ORDER BY day DESC LIMIT ?`, wc), argsLimit...)
		out["hourly"], _ = db.Query(fmt.Sprintf(`SELECT strftime('%%H', create_time, 'unixepoch', 'localtime') AS hour, COUNT(*) AS msg_count
			FROM messages_unified %s GROUP BY hour ORDER BY hour`, wc), args...)
		return out, nil
	}
	out = map[string]any{
		"messages": countTable(db, "messages_unified"),
		"sessions": countTable(db, "sessions_unified"),
		"contacts": countTable(db, "contacts_unified"),
	}
	if tableExists(db, "stats_talkers") {
		out["top_talkers"], _ = db.Query(`SELECT talker, talker_display_name, msg_count, last_time FROM stats_talkers ORDER BY msg_count DESC LIMIT ?`, limit)
		out["top_senders"], _ = db.Query(`SELECT sender_wxid, sender_display_name, msg_count, last_time FROM stats_senders ORDER BY msg_count DESC LIMIT ?`, limit)
		out["by_kind"], _ = db.Query(`SELECT kind_name, base_kind, msg_count FROM stats_kind ORDER BY msg_count DESC LIMIT ?`, limit)
		out["daily"], _ = db.Query(`SELECT day, msg_count FROM stats_daily ORDER BY day DESC LIMIT ?`, limit)
		return out, nil
	}
	out["top_talkers"], _ = db.Query(`SELECT talker, talker_display_name, COUNT(*) AS msg_count, MAX(create_time) AS last_time
		FROM messages_unified GROUP BY talker ORDER BY msg_count DESC LIMIT ?`, limit)
	out["top_senders"], _ = db.Query(`SELECT sender_wxid, sender_display_name, COUNT(*) AS msg_count, MAX(create_time) AS last_time
		FROM messages_unified WHERE sender_wxid IS NOT NULL AND sender_wxid != ''
		GROUP BY sender_wxid ORDER BY msg_count DESC LIMIT ?`, limit)
	out["by_kind"], _ = db.Query(`SELECT kind_name, base_kind, COUNT(*) AS msg_count
		FROM messages_unified GROUP BY kind_name, base_kind ORDER BY msg_count DESC LIMIT ?`, limit)
	out["daily"], _ = db.Query(`SELECT strftime('%Y-%m-%d', create_time, 'unixepoch', 'localtime') AS day, COUNT(*) AS msg_count
		FROM messages_unified GROUP BY day ORDER BY day DESC LIMIT ?`, limit)
	return out, nil
}

func (s *server) toolExportMessages(a map[string]any) (any, error) {
	path := getStr(a, "path")
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	format := getStr(a, "format")
	if format == "" {
		format = "jsonl"
	}
	db, err := s.openCacheIndex(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	talker, err := resolveTalkerForCache(db, a, false)
	if err != nil {
		return nil, err
	}
	if talker != "" {
		a["talker"] = talker
	}
	if a == nil {
		a = map[string]any{}
	}
	if _, ok := a["limit"]; !ok {
		a["limit"] = float64(10000)
	}
	rows, err := queryCacheMessages(db, a, "create_time ASC, rowid ASC")
	if err != nil {
		return nil, err
	}
	normalizeCacheMessages(rows)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	var content string
	switch format {
	case "jsonl":
		var b strings.Builder
		for _, r := range rows {
			j, _ := json.Marshal(r)
			b.Write(j)
			b.WriteByte('\n')
		}
		content = b.String()
	case "markdown":
		content = renderMessagesMarkdown(rows)
	case "html":
		content = renderMessagesHTML(rows)
	default:
		return nil, fmt.Errorf("invalid format=%q: jsonl / markdown / html", format)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "format": format, "count": len(rows)}, nil
}

func queryCacheMessages(db *wcdb.DB, a map[string]any, order string) ([]wcdb.Row, error) {
	talker, err := resolveTalkerForCache(db, a, false)
	if err != nil {
		return nil, err
	}
	if talker != "" {
		a["talker"] = talker
	}
	where, args, err := cacheMessageWhere(db, a, "")
	if err != nil {
		return nil, err
	}
	wc := ""
	if len(where) > 0 {
		wc = "WHERE " + strings.Join(where, " AND ")
	}
	limit := getInt(a, "limit", 50)
	offset := getInt(a, "offset", 0)
	args = append(args, limit, offset)
	return db.Query(fmt.Sprintf(`SELECT m.talker, m.talker_display_name, m.local_id, m.server_id, m.create_time,
		m.create_time_human, m.sender_wxid, m.sender_display_name, m.is_from_me, m.base_kind, m.subtype, m.kind_name,
		m.content_summary, m.message_content, m.parsed_json,
		c.type AS talker_contact_type, c.is_verified AS talker_is_verified
		FROM messages_unified m LEFT JOIN contacts_unified c ON c.username = m.talker
		%s ORDER BY %s LIMIT ? OFFSET ?`, wc, order), args...)
}

func cacheMessageWhere(db *wcdb.DB, a map[string]any, alias string) ([]string, []any, error) {
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	var where []string
	var args []any
	if talker := getStr(a, "talker"); talker != "" {
		where = append(where, col("talker")+" = ?")
		args = append(args, talker)
	}
	if s := getStr(a, "after"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, nil, err
		}
		where = append(where, col("create_time")+" >= ?")
		args = append(args, ts)
	}
	if s := getStr(a, "before"); s != "" {
		ts, err := parseTS(s)
		if err != nil {
			return nil, nil, err
		}
		where = append(where, col("create_time")+" < ?")
		args = append(args, ts)
	}
	if kw := getStr(a, "keyword"); kw != "" {
		where = append(where, "("+col("content_summary")+" LIKE ? OR "+col("message_content")+" LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	if kind := getStr(a, "kind_name"); kind != "" {
		where = append(where, col("kind_name")+" = ?")
		args = append(args, kind)
	} else if kind := getStr(a, "type"); kind != "" {
		where = append(where, col("kind_name")+" = ?")
		args = append(args, kind)
	}
	if baseKind := getInt(a, "base_kind", 0); baseKind != 0 {
		where = append(where, col("base_kind")+" = ?")
		args = append(args, baseKind)
	}
	if sender := getStr(a, "sender"); sender != "" {
		if !looksLikeRawChatID(sender) {
			if cands, err := resolveChatCandidates(db, sender, "", 1); err == nil && len(cands) > 0 {
				sender = cands[0].Username
			}
		}
		where = append(where, col("sender_wxid")+" = ?")
		args = append(args, sender)
	}
	return where, args, nil
}

func normalizeCacheMessages(rows []wcdb.Row) {
	decorateMessageRows(rows)
	for _, r := range rows {
		r["is_from_me"] = rowInt64(r, "is_from_me") != 0
		if pj := rowString(r, "parsed_json"); pj != "" {
			var parsed any
			if json.Unmarshal([]byte(pj), &parsed) == nil {
				r["message_content_parsed"] = parsed
			}
		}
		delete(r, "parsed_json")
	}
}

func getFieldsMode(a map[string]any) string {
	mode := getStr(a, "fields")
	if mode == "" {
		return "lite"
	}
	return mode
}

func parseCacheCursor(cursor string) (int64, int64, error) {
	if cursor == "" {
		return 0, 0, nil
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid cursor %q: want create_time:cache_rowid", cursor)
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	rowid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return ts, rowid, nil
}

func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func renderMessagesMarkdown(rows []wcdb.Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("### ")
		b.WriteString(rowString(r, "create_time_human"))
		b.WriteString(" ")
		b.WriteString(rowString(r, "talker_display_name"))
		if sender := rowString(r, "sender_display_name"); sender != "" {
			b.WriteString(" / ")
			b.WriteString(sender)
		}
		b.WriteString("\n\n")
		b.WriteString(rowString(r, "content_summary"))
		b.WriteString("\n\n")
	}
	return b.String()
}

func renderMessagesHTML(rows []wcdb.Row) string {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>wx-mcp export</title><body>")
	for _, r := range rows {
		b.WriteString("<section><h3>")
		b.WriteString(html.EscapeString(rowString(r, "create_time_human")))
		b.WriteString(" ")
		b.WriteString(html.EscapeString(rowString(r, "talker_display_name")))
		if sender := rowString(r, "sender_display_name"); sender != "" {
			b.WriteString(" / ")
			b.WriteString(html.EscapeString(sender))
		}
		b.WriteString("</h3><p>")
		b.WriteString(html.EscapeString(rowString(r, "content_summary")))
		b.WriteString("</p></section>")
	}
	b.WriteString("</body>")
	return b.String()
}

func countTable(db *wcdb.DB, table string) int64 {
	if !validIdent(table) {
		return 0
	}
	rows, err := db.Query("SELECT COUNT(*) AS n FROM " + quoteIdent(table))
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rowInt64(rows[0], "n")
}

func countWhere(db *wcdb.DB, table, whereClause string, args ...any) int64 {
	if !validIdent(table) {
		return 0
	}
	rows, err := db.Query("SELECT COUNT(*) AS n FROM "+quoteIdent(table)+" "+whereClause, args...)
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rowInt64(rows[0], "n")
}

func tableExists(db *wcdb.DB, table string) bool {
	if !validIdent(table) {
		return false
	}
	rows, err := db.Query("SELECT 1 AS ok FROM sqlite_master WHERE type='table' AND name=? LIMIT 1", table)
	return err == nil && len(rows) > 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileMTimeNanos(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
}

func readSaltHex(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 16)
	if n, err := f.Read(buf); err != nil || n != 16 {
		return ""
	}
	if string(buf) == "SQLite format 3\x00" {
		return ""
	}
	return hex.EncodeToString(buf)
}

func displayOrRaw(display map[string]string, username string) string {
	if username == "" {
		return ""
	}
	if dn := display[username]; dn != "" {
		return dn
	}
	return username
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rowString(r wcdb.Row, key string) string {
	if v, ok := r[key]; ok {
		switch x := v.(type) {
		case string:
			return x
		case []byte:
			return string(x)
		}
	}
	return ""
}

func rowInt64(r wcdb.Row, key string) int64 {
	if v, ok := r[key]; ok {
		switch x := v.(type) {
		case int64:
			return x
		case int:
			return int64(x)
		case int32:
			return int64(x)
		case bool:
			if x {
				return 1
			}
			return 0
		}
	}
	return 0
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var msgTableRe = regexp.MustCompile(`^Msg_[0-9a-f]{32}$`)

func validIdent(s string) bool {
	return identRe.MatchString(s)
}

func validMsgTable(s string) bool {
	return msgTableRe.MatchString(s)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
