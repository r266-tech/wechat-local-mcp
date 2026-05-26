package wcdb

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	SQLITE_OK             = 0
	SQLITE_ROW            = 100
	SQLITE_DONE           = 101
	SQLITE_OPEN_READONLY  = 0x00000001
	SQLITE_OPEN_READWRITE = 0x00000002
	SQLITE_OPEN_CREATE    = 0x00000004
	SQLITE_BUSY           = 5
	SQLITE_LOCKED         = 6

	COL_INT   = 1
	COL_FLOAT = 2
	COL_TEXT  = 3
	COL_BLOB  = 4
	COL_NULL  = 5
)

var (
	mu     sync.Mutex
	loaded bool

	sqlite3_open_v2        func(filename string, ppDb *uintptr, flags int32, vfs *byte) int32
	sqlite3_close_v2       func(db uintptr) int32
	sqlite3_key_v2         func(db uintptr, zDbName string, pKey unsafe.Pointer, nKey int32) int32
	sqlite3_exec           func(db uintptr, sql string, cb uintptr, arg uintptr, errmsg *uintptr) int32
	sqlite3_prepare_v2     func(db uintptr, sql string, nByte int32, stmt *uintptr, tail *uintptr) int32
	sqlite3_step           func(stmt uintptr) int32
	sqlite3_finalize       func(stmt uintptr) int32
	sqlite3_column_count   func(stmt uintptr) int32
	sqlite3_column_name    func(stmt uintptr, i int32) uintptr
	sqlite3_column_text    func(stmt uintptr, i int32) uintptr
	sqlite3_column_int64   func(stmt uintptr, i int32) int64
	sqlite3_column_bytes   func(stmt uintptr, i int32) int32
	sqlite3_column_blob    func(stmt uintptr, i int32) uintptr
	sqlite3_column_type    func(stmt uintptr, i int32) int32
	sqlite3_bind_text      func(stmt uintptr, i int32, s string, n int32, destructor uintptr) int32
	sqlite3_bind_blob      func(stmt uintptr, i int32, p unsafe.Pointer, n int32, destructor uintptr) int32
	sqlite3_bind_int64     func(stmt uintptr, i int32, v int64) int32
	sqlite3_bind_null      func(stmt uintptr, i int32) int32
	sqlite3_reset          func(stmt uintptr) int32
	sqlite3_clear_bindings func(stmt uintptr) int32
	sqlite3_errmsg         func(db uintptr) uintptr
	sqlite3_backup_init    func(dst uintptr, dstName string, src uintptr, srcName string) uintptr
	sqlite3_backup_step    func(backup uintptr, pages int32) int32
	sqlite3_backup_finish  func(backup uintptr) int32
)

// Bootstrap loads the WCDB dylib from the given absolute path.
func Bootstrap(dylibPath string) error {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return nil
	}
	h, err := loadLibrary(dylibPath)
	if err != nil {
		return fmt.Errorf("dlopen %s: %w", dylibPath, err)
	}
	for _, reg := range []struct {
		fn   any
		name string
	}{
		{&sqlite3_open_v2, "sqlite3_open_v2"},
		{&sqlite3_close_v2, "sqlite3_close_v2"},
		{&sqlite3_key_v2, "sqlite3_key_v2"},
		{&sqlite3_exec, "sqlite3_exec"},
		{&sqlite3_prepare_v2, "sqlite3_prepare_v2"},
		{&sqlite3_step, "sqlite3_step"},
		{&sqlite3_finalize, "sqlite3_finalize"},
		{&sqlite3_column_count, "sqlite3_column_count"},
		{&sqlite3_column_name, "sqlite3_column_name"},
		{&sqlite3_column_text, "sqlite3_column_text"},
		{&sqlite3_column_int64, "sqlite3_column_int64"},
		{&sqlite3_column_bytes, "sqlite3_column_bytes"},
		{&sqlite3_column_blob, "sqlite3_column_blob"},
		{&sqlite3_column_type, "sqlite3_column_type"},
		{&sqlite3_bind_text, "sqlite3_bind_text"},
		{&sqlite3_bind_blob, "sqlite3_bind_blob"},
		{&sqlite3_bind_int64, "sqlite3_bind_int64"},
		{&sqlite3_bind_null, "sqlite3_bind_null"},
		{&sqlite3_reset, "sqlite3_reset"},
		{&sqlite3_clear_bindings, "sqlite3_clear_bindings"},
		{&sqlite3_errmsg, "sqlite3_errmsg"},
		{&sqlite3_backup_init, "sqlite3_backup_init"},
		{&sqlite3_backup_step, "sqlite3_backup_step"},
		{&sqlite3_backup_finish, "sqlite3_backup_finish"},
	} {
		purego.RegisterLibFunc(reg.fn, h, reg.name)
	}
	loaded = true
	return nil
}

type DB struct {
	handle uintptr
	path   string
}

// OpenWithEncKey opens a WCDB-encrypted SQLite file using the raw-key path:
// encKeyHex is the 64-hex post-PBKDF2 enc_key (as harvested by `wxkey scan`)
// and saltHex is the 32-hex SQLCipher salt taken from the file's first 16
// bytes. Skips PBKDF2 entirely; opening cost drops to a couple of HMACs.
func OpenWithEncKey(dbPath, encKeyHex, saltHex string) (*DB, error) {
	if len(encKeyHex) != 64 {
		return nil, fmt.Errorf("OpenWithEncKey: enc_key must be 64 hex (got %d)", len(encKeyHex))
	}
	if len(saltHex) != 32 {
		return nil, fmt.Errorf("OpenWithEncKey: salt must be 32 hex (got %d)", len(saltHex))
	}
	// Build "x'<64hex><32hex>'" — SQLCipher's raw-key SQL literal form.
	blob := []byte("x'" + encKeyHex + saltHex + "'")
	return openWithKeyBlob(dbPath, blob, SQLITE_OPEN_READONLY)
}

func OpenWithEncKeyWritable(dbPath, encKeyHex, saltHex string) (*DB, error) {
	if len(encKeyHex) != 64 {
		return nil, fmt.Errorf("OpenWithEncKeyWritable: enc_key must be 64 hex (got %d)", len(encKeyHex))
	}
	if len(saltHex) != 32 {
		return nil, fmt.Errorf("OpenWithEncKeyWritable: salt must be 32 hex (got %d)", len(saltHex))
	}
	blob := []byte("x'" + encKeyHex + saltHex + "'")
	return openWithKeyBlob(dbPath, blob, SQLITE_OPEN_READWRITE)
}

// OpenWithKeyMap opens dbPath after reading the SQLCipher salt from the file
// header (first 16 bytes) and looking up the matching enc_key in keys
// (salt-hex -> enc_key-hex). wechat-cli intentionally supports only this schema-2
// raw-key path; missing salts must be fixed by refreshing wxkey's key map.
func OpenWithKeyMap(dbPath string, keys map[string]string) (*DB, error) {
	salt, err := readDBSalt(dbPath)
	if err != nil {
		return nil, err
	}
	saltHex := hex.EncodeToString(salt)
	if encKeyHex, ok := keys[saltHex]; ok {
		return OpenWithEncKey(dbPath, encKeyHex, saltHex)
	}
	return nil, fmt.Errorf("no enc_key for salt %s in %s — refresh wxkey's schema-2 key map after WeChat touches this DB", saltHex, dbPath)
}

func OpenWithKeyMapWritable(dbPath string, keys map[string]string) (*DB, error) {
	salt, err := readDBSalt(dbPath)
	if err != nil {
		return nil, err
	}
	saltHex := hex.EncodeToString(salt)
	if encKeyHex, ok := keys[saltHex]; ok {
		return OpenWithEncKeyWritable(dbPath, encKeyHex, saltHex)
	}
	return nil, fmt.Errorf("no enc_key for salt %s in %s — refresh wxkey's schema-2 key map after WeChat touches this DB", saltHex, dbPath)
}

// OpenPlain opens an unencrypted SQLite database with the bundled WCDB SQLite
// symbols. The caller must Bootstrap first. writable=false opens readonly.
func OpenPlain(dbPath string, writable bool) (*DB, error) {
	flags := int32(SQLITE_OPEN_READONLY)
	if writable {
		flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE
	}
	var h uintptr
	if rc := sqlite3_open_v2(dbPath, &h, flags, nil); rc != SQLITE_OK {
		return nil, fmt.Errorf("sqlite3_open_v2(%s) rc=%d: %s", dbPath, rc, errmsg(h))
	}
	db := &DB{handle: h, path: dbPath}
	_ = db.Exec("PRAGMA busy_timeout=5000")
	return db, nil
}

func openWithKeyBlob(dbPath string, blob []byte, flags int32) (*DB, error) {
	var h uintptr
	if rc := sqlite3_open_v2(dbPath, &h, flags, nil); rc != SQLITE_OK {
		return nil, fmt.Errorf("sqlite3_open_v2(%s) rc=%d: %s", dbPath, rc, errmsg(h))
	}
	if rc := sqlite3_key_v2(h, "main", unsafe.Pointer(&blob[0]), int32(len(blob))); rc != SQLITE_OK {
		sqlite3_close_v2(h)
		return nil, fmt.Errorf("sqlite3_key_v2 rc=%d", rc)
	}
	db := &DB{handle: h, path: dbPath}
	_ = db.Exec("PRAGMA busy_timeout=5000")
	return db, nil
}

// BackupTo writes a plaintext SQLite snapshot of the opened database. Source
// may be encrypted; destination is opened without sqlite3_key_v2, so the backup
// lands as a normal unencrypted SQLite file. The final rename is atomic.
func (d *DB) BackupTo(dstPath string) error {
	if d == nil || d.handle == 0 {
		return fmt.Errorf("backup from closed db")
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	tmp := dstPath + ".tmp"
	_ = os.Remove(tmp)
	dst, err := OpenPlain(tmp, true)
	if err != nil {
		return err
	}
	closeDst := true
	defer func() {
		if closeDst {
			dst.Close()
			_ = os.Remove(tmp)
		}
	}()

	b := sqlite3_backup_init(dst.handle, "main", d.handle, "main")
	if b == 0 {
		msg := errmsg(dst.handle)
		dst.Close()
		closeDst = false
		_ = os.Remove(tmp)
		if strings.Contains(msg, "encrypted") {
			return d.exportPlaintextTo(tmp, dstPath)
		}
		return fmt.Errorf("sqlite3_backup_init: %s", msg)
	}
	for {
		rc := sqlite3_backup_step(b, -1)
		if rc == SQLITE_DONE {
			break
		}
		if rc == SQLITE_OK {
			continue
		}
		if rc == SQLITE_BUSY || rc == SQLITE_LOCKED {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		_ = sqlite3_backup_finish(b)
		return fmt.Errorf("sqlite3_backup_step rc=%d: src=%s dst=%s", rc, errmsg(d.handle), errmsg(dst.handle))
	}
	if rc := sqlite3_backup_finish(b); rc != SQLITE_OK {
		return fmt.Errorf("sqlite3_backup_finish rc=%d: %s", rc, errmsg(dst.handle))
	}
	dst.Close()
	closeDst = false
	_ = os.Remove(dstPath)
	_ = os.Remove(dstPath + "-wal")
	_ = os.Remove(dstPath + "-shm")
	return os.Rename(tmp, dstPath)
}

func (d *DB) exportPlaintextTo(tmp, dstPath string) error {
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	sql := fmt.Sprintf(
		"ATTACH DATABASE %s AS plaintext KEY ''; SELECT sqlcipher_export('plaintext'); DETACH DATABASE plaintext;",
		sqlString(tmp),
	)
	if err := d.Exec(sql); err != nil {
		_ = d.Exec("DETACH DATABASE plaintext")
		return d.copyPlaintextTo(tmp, dstPath, fmt.Errorf("sqlcipher_export plaintext: %w", err))
	}
	_ = os.Remove(dstPath)
	_ = os.Remove(dstPath + "-wal")
	_ = os.Remove(dstPath + "-shm")
	return os.Rename(tmp, dstPath)
}

func (d *DB) copyPlaintextTo(tmp, dstPath string, cause error) error {
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	dst, err := OpenPlain(tmp, true)
	if err != nil {
		return fmt.Errorf("%v; logical copy open dst: %w", cause, err)
	}
	defer func() {
		dst.Close()
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	schema, err := d.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 2 ELSE 3 END, name`)
	if err != nil {
		return fmt.Errorf("%v; logical copy schema: %w", cause, err)
	}
	var post []string
	if err = dst.Exec("BEGIN"); err != nil {
		return fmt.Errorf("%v; logical copy begin: %w", cause, err)
	}
	for _, r := range schema {
		typ, _ := r["type"].(string)
		name, _ := r["name"].(string)
		sql, _ := r["sql"].(string)
		if typ != "table" {
			post = append(post, sql)
			continue
		}
		if err = dst.Exec(sql); err != nil {
			if isSkippableSchemaError(sql, err) {
				continue
			}
			_ = dst.Exec("ROLLBACK")
			return fmt.Errorf("%v; create table %s: %w", cause, name, err)
		}
		if err = copyTableRows(d, dst, name); err != nil {
			_ = dst.Exec("ROLLBACK")
			return fmt.Errorf("%v; copy table %s: %w", cause, name, err)
		}
	}
	for _, sql := range post {
		// Indexes/triggers/views are acceleration or metadata for our use case.
		// Some virtual-table companion indexes cannot be recreated verbatim; the
		// row snapshot is still useful, so ignore post-schema failures.
		_ = dst.Exec(sql)
	}
	if err = dst.Exec("COMMIT"); err != nil {
		_ = dst.Exec("ROLLBACK")
		return fmt.Errorf("%v; logical copy commit: %w", cause, err)
	}
	dst.Close()
	_ = os.Remove(dstPath)
	_ = os.Remove(dstPath + "-wal")
	_ = os.Remove(dstPath + "-shm")
	if err = os.Rename(tmp, dstPath); err != nil {
		return fmt.Errorf("%v; logical copy rename: %w", cause, err)
	}
	return nil
}

func isSkippableSchemaError(sql string, err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such tokenizer") ||
		(strings.Contains(strings.ToUpper(sql), "CREATE VIRTUAL TABLE") &&
			(strings.Contains(msg, "no such module") || strings.Contains(msg, "no such tokenizer")))
}

func copyTableRows(src, dst *DB, table string) error {
	cols, err := tableColumns(src, table)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteIdent(c)
		placeholders[i] = "?"
	}
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table), strings.Join(quotedCols, ","), strings.Join(placeholders, ","))
	for offset := 0; ; offset += 500 {
		rows, err := src.Query(fmt.Sprintf("SELECT * FROM %s LIMIT ? OFFSET ?", quoteIdent(table)), 500, offset)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			args := make([]any, len(cols))
			for i, c := range cols {
				args[i] = row[c]
			}
			if err := dst.ExecArgs(insertSQL, args...); err != nil {
				return err
			}
		}
	}
}

func tableColumns(db *DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + sqlString(table) + ")")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if name, ok := r["name"].(string); ok && name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// readDBSalt reads the first 16 bytes of dbPath — these are the SQLCipher
// salt and uniquely identify which enc_key opens the file.
func readDBSalt(dbPath string) ([]byte, error) {
	f, err := os.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer f.Close()
	salt := make([]byte, 16)
	n, err := f.Read(salt)
	if err != nil || n != 16 {
		return nil, fmt.Errorf("read salt %s: %w (n=%d)", dbPath, err, n)
	}
	return salt, nil
}

func (d *DB) Close() {
	if d == nil || d.handle == 0 {
		return
	}
	sqlite3_close_v2(d.handle)
	d.handle = 0
}

func (d *DB) Exec(sql string) error {
	var errPtr uintptr
	if rc := sqlite3_exec(d.handle, sql, 0, 0, &errPtr); rc != SQLITE_OK {
		return fmt.Errorf("exec rc=%d: %s", rc, readCString(errPtr))
	}
	return nil
}

func (d *DB) ExecArgs(sql string, args ...any) error {
	var stmt uintptr
	if rc := sqlite3_prepare_v2(d.handle, sql, -1, &stmt, nil); rc != SQLITE_OK {
		return fmt.Errorf("prepare rc=%d: %s (sql=%s)", rc, errmsg(d.handle), sql)
	}
	defer sqlite3_finalize(stmt)
	if err := bindArgs(stmt, args); err != nil {
		return err
	}
	rc := sqlite3_step(stmt)
	if rc != SQLITE_DONE && rc != SQLITE_ROW {
		return fmt.Errorf("step rc=%d: %s", rc, errmsg(d.handle))
	}
	return nil
}

type Stmt struct {
	db     *DB
	handle uintptr
	sql    string
}

func (d *DB) Prepare(sql string) (*Stmt, error) {
	var stmt uintptr
	if rc := sqlite3_prepare_v2(d.handle, sql, -1, &stmt, nil); rc != SQLITE_OK {
		return nil, fmt.Errorf("prepare rc=%d: %s (sql=%s)", rc, errmsg(d.handle), sql)
	}
	return &Stmt{db: d, handle: stmt, sql: sql}, nil
}

func (s *Stmt) Exec(args ...any) error {
	if s == nil || s.handle == 0 {
		return fmt.Errorf("exec on closed statement")
	}
	if rc := sqlite3_reset(s.handle); rc != SQLITE_OK {
		return fmt.Errorf("reset rc=%d: %s", rc, errmsg(s.db.handle))
	}
	if rc := sqlite3_clear_bindings(s.handle); rc != SQLITE_OK {
		return fmt.Errorf("clear bindings rc=%d: %s", rc, errmsg(s.db.handle))
	}
	if err := bindArgs(s.handle, args); err != nil {
		return err
	}
	rc := sqlite3_step(s.handle)
	if rc != SQLITE_DONE && rc != SQLITE_ROW {
		return fmt.Errorf("step rc=%d: %s (sql=%s)", rc, errmsg(s.db.handle), s.sql)
	}
	return nil
}

func (s *Stmt) Close() {
	if s == nil || s.handle == 0 {
		return
	}
	sqlite3_finalize(s.handle)
	s.handle = 0
}

type Row map[string]any

func (d *DB) Query(sql string, args ...any) ([]Row, error) {
	var stmt uintptr
	if rc := sqlite3_prepare_v2(d.handle, sql, -1, &stmt, nil); rc != SQLITE_OK {
		return nil, fmt.Errorf("prepare rc=%d: %s (sql=%s)", rc, errmsg(d.handle), sql)
	}
	defer sqlite3_finalize(stmt)

	if err := bindArgs(stmt, args); err != nil {
		return nil, err
	}

	ncol := sqlite3_column_count(stmt)
	names := make([]string, ncol)
	for i := int32(0); i < ncol; i++ {
		names[i] = readCString(sqlite3_column_name(stmt, i))
	}

	rows := []Row{}
	for {
		rc := sqlite3_step(stmt)
		if rc == SQLITE_DONE {
			break
		}
		if rc != SQLITE_ROW {
			return nil, fmt.Errorf("step rc=%d: %s", rc, errmsg(d.handle))
		}
		row := make(Row, ncol)
		for i := int32(0); i < ncol; i++ {
			row[names[i]] = readColumn(stmt, i)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func bindArgs(stmt uintptr, args []any) error {
	for i, a := range args {
		idx := int32(i + 1)
		switch v := a.(type) {
		case nil:
			sqlite3_bind_null(stmt, idx)
		case string:
			sqlite3_bind_text(stmt, idx, v, int32(len(v)), ^uintptr(0))
		case []byte:
			if len(v) == 0 {
				sqlite3_bind_blob(stmt, idx, unsafe.Pointer(nil), 0, ^uintptr(0))
			} else {
				sqlite3_bind_blob(stmt, idx, unsafe.Pointer(&v[0]), int32(len(v)), ^uintptr(0))
			}
		case int:
			sqlite3_bind_int64(stmt, idx, int64(v))
		case int32:
			sqlite3_bind_int64(stmt, idx, int64(v))
		case int64:
			sqlite3_bind_int64(stmt, idx, v)
		case bool:
			if v {
				sqlite3_bind_int64(stmt, idx, 1)
			} else {
				sqlite3_bind_int64(stmt, idx, 0)
			}
		default:
			return fmt.Errorf("unsupported bind type %T at arg %d", a, i)
		}
	}
	return nil
}

func readColumn(stmt uintptr, i int32) any {
	switch sqlite3_column_type(stmt, i) {
	case COL_INT:
		return sqlite3_column_int64(stmt, i)
	case COL_TEXT:
		return readCString(sqlite3_column_text(stmt, i))
	case COL_BLOB:
		n := sqlite3_column_bytes(stmt, i)
		if n == 0 {
			return []byte{}
		}
		p := sqlite3_column_blob(stmt, i)
		b := make([]byte, n)
		copy(b, unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
		return b
	case COL_NULL:
		return nil
	}
	return nil
}

func readCString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for {
		b := *(*byte)(unsafe.Pointer(p + uintptr(n)))
		if b == 0 {
			break
		}
		n++
		if n > 10_000_000 {
			break
		}
	}
	if n == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
}

func errmsg(db uintptr) string {
	if db == 0 {
		return ""
	}
	return readCString(sqlite3_errmsg(db))
}
