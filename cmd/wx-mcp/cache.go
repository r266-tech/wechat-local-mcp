package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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

	"github.com/r266-tech/wechat-local-mcp/internal/config"
	"github.com/r266-tech/wechat-local-mcp/internal/wcdb"
	"github.com/r266-tech/wechat-local-mcp/internal/wxkind"
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
	home, err := wxMCPHomeDir()
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
		if err := s.ensureCacheFresh(paths); err != nil {
			return nil, err
		}
		if _, err := os.Stat(paths.IndexPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errCacheMissing
			}
			return nil, err
		}
	}
	return wcdb.OpenPlain(paths.IndexPath, writable)
}

func (s *server) ensureCacheFresh(paths cachePaths) error {
	if envBool("WX_MCP_DISABLE_AUTO_REFRESH") {
		return nil
	}
	fresh, _, err := s.cacheFreshness(s.cfg, paths)
	if err != nil {
		return err
	}
	if fresh {
		return nil
	}
	unlock, acquired, lockPath, err := acquireCacheRefreshLock()
	if err != nil {
		return err
	}
	if acquired {
		defer unlock()
		if _, err := s.refreshCache(false); err != nil {
			return err
		}
		fresh, reason, err := s.cacheFreshness(s.cfg, paths)
		if err != nil {
			return err
		}
		if !fresh {
			if cacheDriftedAfterRefresh(reason) {
				return nil
			}
			return fmt.Errorf("cache refresh completed but cache is still not usable: %s", reason)
		}
		return nil
	}

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		fresh, _, err := s.cacheFreshness(s.cfg, paths)
		if err != nil {
			return err
		}
		if fresh {
			return nil
		}
	}
	return fmt.Errorf("cache refresh already running and cache is still stale after waiting (lock: %s)", lockPath)
}

func cacheDriftedAfterRefresh(reason string) bool {
	return strings.HasPrefix(reason, "changed source db: ")
}

func metadataStatusReason(reason string) string {
	if rel, ok := strings.CutPrefix(reason, "changed source db: "); ok {
		if isMetadataCacheSource(rel) {
			return "metadata source changed since last snapshot: " + rel
		}
	}
	if rel, ok := strings.CutPrefix(reason, "new source db: "); ok {
		if isMetadataCacheSource(rel) {
			return "new metadata source detected: " + rel
		}
	}
	if rel, ok := strings.CutPrefix(reason, "snapshot missing: "); ok {
		if isMetadataCacheSource(rel) {
			return "metadata snapshot missing: " + rel
		}
	}
	if rel, ok := strings.CutPrefix(reason, "critical snapshot error: "); ok {
		if isMetadataCacheSource(rel) {
			return "metadata snapshot error: " + rel
		}
	}
	return reason
}

func (s *server) cacheFreshness(cfg *config.Config, paths cachePaths) (bool, string, error) {
	if !fileExists(paths.IndexPath) {
		return false, "cache index missing", nil
	}
	sources, err := listSourceDBs(cfg, paths)
	if err != nil {
		return false, "", err
	}
	prev := loadCacheFileMeta(paths.IndexPath)
	if len(prev) == 0 {
		return false, "cache metadata missing", nil
	}
	if legacyMessageCachePresent(paths, prev) {
		return false, "legacy message cache present; rebuild metadata cache", nil
	}
	for _, src := range sources {
		critical := isCriticalCacheSource(src.RelPath)
		old, ok := prev[src.RelPath]
		if !ok {
			if critical {
				return false, "new source db: " + src.RelPath, nil
			}
			continue
		}
		if old.DBMTime != src.DBMTime || old.WALMTime != src.WALMTime || old.SaltHex != src.SaltHex {
			if critical {
				return false, "changed source db: " + src.RelPath, nil
			}
			continue
		}
		if old.Status == "ok" && !fileExists(src.Snapshot) {
			if critical {
				return false, "snapshot missing: " + src.RelPath, nil
			}
			continue
		}
		if old.Status == "error" && critical {
			return false, "critical snapshot error: " + src.RelPath, nil
		}
	}
	return true, "", nil
}

func isCriticalCacheSource(rel string) bool {
	return isMetadataCacheSource(rel)
}

func isMetadataCacheSource(rel string) bool {
	return rel == "contact/contact.db" || rel == "session/session.db"
}

func shouldSnapshotCacheSource(rel string) bool {
	return isMetadataCacheSource(rel)
}

func legacyMessageCachePresent(paths cachePaths, prev map[string]cacheFileMeta) bool {
	if cacheMetaValue(paths.IndexPath, "message_cache_enabled") == "true" {
		return true
	}
	for _, old := range prev {
		if !isMetadataCacheSource(old.RelPath) && old.Status == "ok" {
			return true
		}
	}
	for _, table := range []string{"messages_unified", "message_fts", "stats_talkers", "stats_senders", "stats_kind", "stats_daily"} {
		if plainIndexTableExists(paths.IndexPath, table) {
			return true
		}
	}
	return rawDirHasNonMetadataSnapshots(paths.RawDir)
}

func plainIndexTableExists(indexPath, table string) bool {
	if !validIdent(table) || !fileExists(indexPath) {
		return false
	}
	db, err := wcdb.OpenPlain(indexPath, false)
	if err != nil {
		return false
	}
	defer db.Close()
	return tableExists(db, table)
}

func rawDirHasNonMetadataSnapshots(rawDir string) bool {
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch e.Name() {
		case "contact", "session":
			continue
		default:
			return true
		}
	}
	return false
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
	}
	if rows, err := db.Query("SELECT key, value FROM cache_meta ORDER BY key"); err == nil {
		meta := map[string]string{}
		for _, r := range rows {
			key := rowString(r, "key")
			if key == "message_cache_enabled" || key == "fts_ready" || key == "message_errors" {
				continue
			}
			meta[key] = rowString(r, "value")
		}
		out["meta"] = meta
	}
	if cfg.DBRoot != "" {
		if fresh, reason, err := s.cacheFreshness(cfg, paths); err == nil {
			if !fresh && reason != "" {
				out["metadata_stale_reason"] = metadataStatusReason(reason)
			}
		}
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
	if getBool(a, "background") {
		force := getBool(a, "force")
		logPath, err := spawnBackgroundCacheRefresh(force, lockPath)
		if err != nil {
			unlock()
			return nil, err
		}
		return map[string]any{
			"started":    true,
			"background": true,
			"force":      force,
			"lock":       lockPath,
			"log":        logPath,
		}, nil
	}
	defer unlock()
	return s.refreshCache(getBool(a, "force"))
}

func spawnBackgroundCacheRefresh(force bool, lockPath string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	logDir, err := cacheLogDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	logPath := filepath.Join(logDir, "cache-refresh.background.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()

	args := []string{"cache", "refresh"}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "WX_MCP_CACHE_LOCK_HELD="+lockPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if err := cmd.Process.Release(); err != nil {
		return "", err
	}
	return logPath, nil
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
	if held := os.Getenv("WX_MCP_CACHE_LOCK_HELD"); held != "" {
		if held == "1" {
			return func() {}, true, "", nil
		}
		return func() { _ = os.Remove(held) }, true, held, nil
	}
	home, err := wxMCPHomeDir()
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
	removeNonMetadataCacheSnapshots(paths.RawDir)
	sources, err := listSourceDBs(s.cfg, paths)
	if err != nil {
		return nil, err
	}
	prev := loadCacheFileMeta(paths.IndexPath)
	metas := make([]cacheFileMeta, 0, len(sources))
	stats := map[string]int64{"source_dbs": int64(len(sources))}
	var errorsOut []map[string]string

	for _, res := range s.snapshotSources(sources, prev, force) {
		metas = append(metas, res.meta)
		if res.statKey != "" {
			stats[res.statKey]++
		}
		if res.errorOut != nil {
			errorsOut = append(errorsOut, res.errorOut)
		}
	}

	indexStats, indexErrors, err := s.buildCacheIndex(paths, metas)
	if err != nil {
		return nil, err
	}
	errorsOut = append(errorsOut, indexErrors...)
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

type snapshotResult struct {
	meta     cacheFileMeta
	statKey  string
	errorOut map[string]string
}

func (s *server) snapshotSources(sources []sourceDBInfo, prev map[string]cacheFileMeta, force bool) []snapshotResult {
	if len(sources) == 0 {
		return nil
	}
	workers := cacheWorkerCount(len(sources))
	jobs := make(chan sourceDBInfo)
	results := make(chan snapshotResult, len(sources))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for src := range jobs {
				results <- s.snapshotSource(src, prev, force)
			}
		}()
	}
	for _, src := range sources {
		jobs <- src
	}
	close(jobs)
	wg.Wait()
	close(results)
	out := make([]snapshotResult, 0, len(sources))
	for res := range results {
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].meta.RelPath < out[j].meta.RelPath })
	return out
}

func cacheWorkerCount(total int) int {
	if total <= 1 {
		return 1
	}
	if raw := strings.TrimSpace(os.Getenv("WX_MCP_CACHE_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n < 1 {
				return 1
			}
			if n > total {
				return total
			}
			return n
		}
	}
	n := runtime.NumCPU()
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	if n > total {
		n = total
	}
	return n
}

func wxMCPHomeDir() (string, error) {
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}

func (s *server) snapshotSource(src sourceDBInfo, prev map[string]cacheFileMeta, force bool) snapshotResult {
	meta := cacheFileMeta{
		RelPath:    src.RelPath,
		SourcePath: src.Source,
		Snapshot:   src.Snapshot,
		DBMTime:    src.DBMTime,
		WALMTime:   src.WALMTime,
		SaltHex:    src.SaltHex,
		Status:     "ok",
	}
	if !shouldSnapshotCacheSource(src.RelPath) {
		removeCacheSnapshot(src.Snapshot)
		meta.Status = "skipped"
		meta.Error = "source is read live; metadata cache stores contacts and sessions only by default"
		return snapshotResult{meta: meta, statKey: "skipped_live_source"}
	}
	if strings.HasSuffix(src.File, "_fts.db") {
		meta.Status = "skipped"
		meta.Error = "message_fts.db is read live; metadata cache stores contacts and sessions only"
		return snapshotResult{meta: meta, statKey: "skipped_fts"}
	}
	if old, ok := prev[src.RelPath]; ok && !force &&
		old.DBMTime == src.DBMTime && old.WALMTime == src.WALMTime &&
		old.SaltHex == src.SaltHex && fileExists(src.Snapshot) {
		meta.CopiedAt = old.CopiedAt
		meta.Reused = true
		return snapshotResult{meta: meta, statKey: "reused"}
	}
	db, err := s.openDBWritable(src.Subdir, src.File, isCriticalCacheSource(src.RelPath))
	if err != nil {
		meta.Status = "error"
		meta.Error = err.Error()
		return snapshotResult{
			meta:     meta,
			statKey:  "snapshot_errors",
			errorOut: map[string]string{"rel_path": src.RelPath, "error": err.Error()},
		}
	}
	if err := db.BackupTo(src.Snapshot); err != nil {
		meta.Status = "error"
		meta.Error = err.Error()
		db.Close()
		return snapshotResult{
			meta:     meta,
			statKey:  "snapshot_errors",
			errorOut: map[string]string{"rel_path": src.RelPath, "error": err.Error()},
		}
	}
	db.Close()
	meta.DBMTime = fileMTimeNanos(src.Source)
	meta.WALMTime = fileMTimeNanos(src.Source + "-wal")
	meta.SaltHex = readSaltHex(src.Source)
	meta.CopiedAt = time.Now().Unix()
	return snapshotResult{meta: meta, statKey: "snapshotted"}
}

func (s *server) openDBWritable(subdir, file string, allowKeyRefresh bool) (*wcdb.DB, error) {
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
		db, err := wcdb.OpenWithKeyMapWritable(resolvedDB, s.cfg.Keys)
		if err == nil || !isMissingEncKeyErr(err) || !allowKeyRefresh {
			return db, err
		}
		if setupErr := s.refreshKeysFromWxkey(err.Error()); setupErr != nil {
			return nil, setupErr
		}
		return wcdb.OpenWithKeyMapWritable(resolvedDB, s.cfg.Keys)
	}
	if !allowKeyRefresh {
		return nil, fmt.Errorf("no schema-2 DB keys cached")
	}
	if err := s.refreshKeysFromWxkey("no schema-2 DB keys cached"); err != nil {
		return nil, err
	}
	return wcdb.OpenWithKeyMapWritable(resolvedDB, s.cfg.Keys)
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

func cacheMetaValue(indexPath, key string) string {
	if !fileExists(indexPath) {
		return ""
	}
	db, err := wcdb.OpenPlain(indexPath, false)
	if err != nil {
		return ""
	}
	defer db.Close()
	rows, err := db.Query("SELECT value FROM cache_meta WHERE key=? LIMIT 1", key)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rowString(rows[0], "value")
}

func (s *server) buildCacheIndex(paths cachePaths, files []cacheFileMeta) (map[string]int64, []map[string]string, error) {
	tmp := paths.IndexPath + ".tmp"
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	db, err := wcdb.OpenPlain(tmp, true)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	if err := createIndexSchema(db); err != nil {
		return nil, nil, err
	}
	if err := db.Exec("BEGIN"); err != nil {
		return nil, nil, err
	}
	stats := map[string]int64{}
	cacheFileInsert, err := db.Prepare(`INSERT INTO cache_files
		(rel_path, source_path, snapshot_path, db_mtime, wal_mtime, salt_hex, status, error, copied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = db.Exec("ROLLBACK")
		return nil, nil, err
	}
	for _, f := range files {
		if err := cacheFileInsert.Exec(f.RelPath, f.SourcePath, f.Snapshot, f.DBMTime, f.WALMTime, f.SaltHex, f.Status, f.Error, f.CopiedAt); err != nil {
			cacheFileInsert.Close()
			_ = db.Exec("ROLLBACK")
			return nil, nil, err
		}
	}
	cacheFileInsert.Close()
	display, contactCount := buildIndexContacts(db, snapshotFor(files, "contact/contact.db"))
	stats["contacts"] = contactCount
	sessionCount := buildIndexSessions(db, snapshotFor(files, "session/session.db"), display)
	stats["sessions"] = sessionCount
	createIndexSessionIndexes(db)
	_ = setCacheMeta(db, "refreshed_at", strconv.FormatInt(time.Now().Unix(), 10))
	if err := db.Exec("COMMIT"); err != nil {
		_ = db.Exec("ROLLBACK")
		return nil, nil, err
	}
	db.Close()
	_ = os.Remove(paths.IndexPath)
	_ = os.Remove(paths.IndexPath + "-wal")
	_ = os.Remove(paths.IndexPath + "-shm")
	if err := os.Rename(tmp, paths.IndexPath); err != nil {
		return nil, nil, err
	}
	return stats, nil, nil
}

func createIndexSchema(db *wcdb.DB) error {
	return db.Exec(`
		PRAGMA journal_mode=OFF;
		PRAGMA synchronous=OFF;
		PRAGMA temp_store=MEMORY;
		PRAGMA locking_mode=EXCLUSIVE;
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
	`)
}

func createIndexSessionIndexes(db *wcdb.DB) {
	_ = db.Exec(`
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
	insert, err := idx.Prepare(`INSERT OR REPLACE INTO contacts_unified
		(username, display_name, nick_name, remark, alias, description, type, is_verified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return display, 0
	}
	defer insert.Close()
	var n int64
	for _, r := range rows {
		u := rowString(r, "username")
		if u == "" {
			continue
		}
		dn := rowString(r, "display_name")
		display[u] = dn
		_ = insert.Exec(
			u, dn, rowString(r, "nick_name"), rowString(r, "remark"), rowString(r, "alias"),
			rowString(r, "description"), wxkind.ClassifyUsername(u), rowInt64(r, "verify_flag") != 0)
		n++
	}
	return display, n
}

func buildIndexSessions(idx *wcdb.DB, path string, display map[string]string) int64 {
	if path == "" || !fileExists(path) {
		return 0
	}
	db, err := wcdb.OpenPlain(path, false)
	if err != nil {
		return 0
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username, unread_count, summary,
		last_timestamp, sort_timestamp,
		last_msg_sender AS last_sender_wxid, last_sender_display_name,
		last_msg_type, last_msg_sub_type
		FROM SessionTable
		WHERE COALESCE(is_hidden, 0) = 0`)
	if err != nil {
		return 0
	}
	insert, err := idx.Prepare(`INSERT OR REPLACE INTO sessions_unified
		(username, display_name, unread_count, summary, last_timestamp, sort_timestamp,
		 last_sender_wxid, last_sender_display_name, last_msg_type, last_msg_sub_type, last_msg_kind_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0
	}
	defer insert.Close()
	var n int64
	for _, r := range rows {
		u := rowString(r, "username")
		if u == "" {
			continue
		}
		dn := displayOrRaw(display, u)
		sender := stripAggSenderPrefix(rowString(r, "last_sender_wxid"))
		bk := rowInt64(r, "last_msg_type")
		st := rowInt64(r, "last_msg_sub_type")
		kind := wxkind.Resolve(int32(bk), int32(st))
		_ = insert.Exec(
			u, dn, rowInt64(r, "unread_count"), rowString(r, "summary"), rowInt64(r, "last_timestamp"),
			rowInt64(r, "sort_timestamp"), emptyToNil(sender), emptyToNil(rowString(r, "last_sender_display_name")),
			bk, st, kind)
		n++
	}
	return n
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

func decorateMessageSearchRows(rows []wcdb.Row) {
	decorateMessageRows(rows)
	for _, r := range rows {
		if c := rowString(r, "content"); c != "" {
			r["content"] = senderPrefixRe.ReplaceAllString(c, "")
		}
	}
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

func (s *server) toolStats(a map[string]any) (any, error) {
	db, err := s.openCacheIndex(false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return map[string]any{
		"sessions": countTable(db, "sessions_unified"),
		"contacts": countTable(db, "contacts_unified"),
		"note":     "wx-mcp only caches metadata; use messages/search for live chat-history reads",
	}, nil
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
	if firstNonEmpty(getStr(a, "talker"), getStr(a, "chat")) == "" {
		return nil, fmt.Errorf("export_messages requires chat or talker; wx-mcp does not keep a global message cache")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	count, err := s.writeLiveExportMessages(a, path, format)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "format": format, "count": count, "live": true}, nil
}

func (s *server) writeLiveExportMessages(a map[string]any, path, format string) (int, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 256*1024)
	totalLimit := getInt(a, "limit", 10000)
	if totalLimit <= 0 {
		totalLimit = 10000
	}
	batchSize := 1000
	if totalLimit < batchSize {
		batchSize = totalLimit
	}
	baseOffset := getInt(a, "offset", 0)
	written := 0
	switch format {
	case "jsonl":
	case "markdown":
	case "html":
		_, _ = w.WriteString("<!doctype html><meta charset=\"utf-8\"><title>wx-mcp export</title><body>")
	default:
		return 0, fmt.Errorf("invalid format=%q: jsonl / markdown / html", format)
	}
	for written < totalLimit {
		batchArgs := map[string]any{}
		for k, v := range a {
			batchArgs[k] = v
		}
		remaining := totalLimit - written
		limit := batchSize
		if remaining < limit {
			limit = remaining
		}
		batchArgs["limit"] = limit
		batchArgs["offset"] = baseOffset + written
		rows, err := s.queryLiveMessages(batchArgs, "create_time ASC, local_id ASC")
		if err != nil {
			return written, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			switch format {
			case "jsonl":
				j, _ := json.Marshal(r)
				if _, err := w.Write(j); err != nil {
					return written, err
				}
				if err := w.WriteByte('\n'); err != nil {
					return written, err
				}
			case "markdown":
				if _, err := w.WriteString(renderMessageMarkdown(r)); err != nil {
					return written, err
				}
			case "html":
				if _, err := w.WriteString(renderMessageHTMLSection(r)); err != nil {
					return written, err
				}
			}
		}
		written += len(rows)
		if len(rows) < limit {
			break
		}
	}
	if format == "html" {
		if _, err := w.WriteString("</body>"); err != nil {
			return written, err
		}
	}
	if err := w.Flush(); err != nil {
		return written, err
	}
	return written, nil
}

func fieldsMode(a map[string]any) (string, error) {
	mode := getStr(a, "fields")
	if mode == "" {
		return "lite", nil
	}
	if mode != "lite" && mode != "full" {
		return "", fmt.Errorf("invalid fields=%q: must be \"lite\" or \"full\"", mode)
	}
	return mode, nil
}

func getFieldsMode(a map[string]any) string {
	mode, err := fieldsMode(a)
	if err != nil {
		return "lite"
	}
	return mode
}

func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func renderMessagesMarkdown(rows []wcdb.Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(renderMessageMarkdown(r))
	}
	return b.String()
}

func renderMessageMarkdown(r wcdb.Row) string {
	var b strings.Builder
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
	return b.String()
}

func renderMessagesHTML(rows []wcdb.Row) string {
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>wx-mcp export</title><body>")
	for _, r := range rows {
		b.WriteString(renderMessageHTMLSection(r))
	}
	b.WriteString("</body>")
	return b.String()
}

func renderMessageHTMLSection(r wcdb.Row) string {
	var b strings.Builder
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

func removeCacheSnapshot(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

func removeNonMetadataCacheSnapshots(rawDir string) {
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		switch e.Name() {
		case "contact", "session":
			continue
		default:
			_ = os.RemoveAll(filepath.Join(rawDir, e.Name()))
		}
	}
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
