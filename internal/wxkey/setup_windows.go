//go:build windows

package wxkey

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/r266-tech/wechat-local-mcp/internal/config"
	"github.com/r266-tech/wechat-local-mcp/internal/wcdb"
)

const (
	processVMRead           = 0x0010
	processQueryInformation = 0x0400

	memCommit    = 0x1000
	pageNoAccess = 0x01
	pageGuard    = 0x100

	th32csSnapProcess = 0x00000002
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess         = kernel32.NewProc("OpenProcess")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procVirtualQueryEx      = kernel32.NewProc("VirtualQueryEx")
	procReadProcessMemory   = kernel32.NewProc("ReadProcessMemory")
	procCreateToolhelp32    = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW     = kernel32.NewProc("Process32FirstW")
	procProcess32NextW      = kernel32.NewProc("Process32NextW")
	procGetCurrentProcessID = kernel32.NewProc("GetCurrentProcessId")
)

type windowsMemoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

type windowsProcessEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

type windowsSourceDB struct {
	rel  string
	path string
	salt string
}

type windowsProcess struct {
	pid uint32
	exe string
}

type windowsSetupStats struct {
	SourceDBs        int      `json:"source_dbs"`
	TargetSalts      int      `json:"target_salts"`
	ScannedProcesses int      `json:"scanned_processes"`
	ScannedPIDs      []uint32 `json:"scanned_pids"`
	MatchedSalts     int      `json:"matched_salts"`
	VerifiedDBs      int      `json:"verified_dbs"`
}

func runSetup() (*SetupResult, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	if cfg.DBRoot == "" {
		root, wxid, err := config.AutoDetectDBRoot()
		if err != nil {
			return nil, "", fmt.Errorf("detect Windows WeChat DB root: %w", err)
		}
		cfg.DBRoot = root
		if cfg.Wxid == "" {
			cfg.Wxid = wxid
		}
	}
	if cfg.Wxid == "" {
		cfg.Wxid = wxidFromAccountDir(cfg.DBRoot)
	}
	if cfg.Keys == nil {
		cfg.Keys = map[string]string{}
	}

	dbs, salts, err := windowsListSourceDBs(cfg.DBRoot)
	if err != nil {
		return nil, "", err
	}
	if len(dbs) == 0 {
		return nil, "", fmt.Errorf("no .db files found under %s", filepath.Join(cfg.DBRoot, "db_storage"))
	}

	lib, err := windowsFindWCDB()
	if err != nil {
		return nil, "", err
	}
	if err := wcdb.Bootstrap(lib); err != nil {
		return nil, "", err
	}

	procs, err := windowsTargetProcesses()
	if err != nil {
		return nil, "", err
	}
	if len(procs) == 0 {
		return nil, "", fmt.Errorf("no running Weixin.exe/WeChat.exe process found; log in to Windows WeChat first or set WECHAT_CLI_WECHAT_PID")
	}

	found := map[string]string{}
	stats := windowsSetupStats{SourceDBs: len(dbs), TargetSalts: len(salts)}
	var firstHitPID uint32
	for _, p := range procs {
		if len(found) == len(salts) {
			break
		}
		stats.ScannedProcesses++
		stats.ScannedPIDs = append(stats.ScannedPIDs, p.pid)
		before := len(found)
		if err := windowsScanProcess(p.pid, salts, found); err != nil {
			continue
		}
		if firstHitPID == 0 && len(found) > before {
			firstHitPID = p.pid
		}
	}

	var results []ResultEntry
	verified := map[string]string{}
	for _, db := range dbs {
		key, ok := found[db.salt]
		if !ok {
			continue
		}
		if !windowsVerifyDBKey(db.path, key, db.salt) {
			continue
		}
		verified[db.salt] = key
		results = append(results, ResultEntry{
			DBRel:    filepath.ToSlash(db.rel),
			DBPath:   db.path,
			SaltHex:  db.salt,
			KeyHex:   key,
			VerifyAs: "windows-process-raw-key",
		})
	}
	if len(verified) == 0 {
		return nil, "", fmt.Errorf("no usable Windows WeChat raw keys found after scanning %d process(es); ensure WECHAT_CLI_DB_ROOT matches the logged-in account", stats.ScannedProcesses)
	}

	for salt, key := range verified {
		cfg.Keys[salt] = key
	}
	cfg.SchemaVersion = 2
	cfg.KeyPID = int(firstHitPID)
	cfg.KeyEpoch = time.Now().Unix()
	if err := config.Save(cfg); err != nil {
		return nil, "", err
	}
	stats.MatchedSalts = len(found)
	stats.VerifiedDBs = len(results)
	statsJSON, _ := json.Marshal(stats)
	cfgPath, _ := config.Path()
	res := &SetupResult{
		PID:        int(firstHitPID),
		Root:       cfg.DBRoot,
		WxID:       cfg.Wxid,
		ConfigPath: cfgPath,
		Stats:      statsJSON,
		Results:    results,
		Keys:       verified,
	}
	return res, fmt.Sprintf("Windows key scan OK: verified %d/%d db files from %d process(es)\n", stats.VerifiedDBs, stats.SourceDBs, stats.ScannedProcesses), nil
}

func windowsFindWCDB() (string, error) {
	names := []string{"libWCDB.dll", "WCDB.dll", "e_sqlcipher.dll"}
	var candidates []string
	for _, env := range []string{"WECHAT_CLI_WCDB_LIB", "WECHAT_CLI_WCDB_DYLIB", "WX_MCP_WCDB_LIB", "WX_MCP_WCDB_DYLIB"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			candidates = append(candidates, p)
		}
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			for _, name := range names {
				candidates = append(candidates, filepath.Join(dir, name), filepath.Join(dir, "lib", name), filepath.Join(dir, "..", "lib", name))
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(cwd, name), filepath.Join(cwd, "lib", name))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(home, ".config", "wxcli", "lib", name))
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("WCDB/SQLCipher DLL not found for Windows key verification")
}

func windowsListSourceDBs(root string) ([]windowsSourceDB, map[string]bool, error) {
	base := filepath.Join(root, "db_storage")
	var out []windowsSourceDB
	salts := map[string]bool{}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".db" || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			return nil
		}
		salt, err := windowsReadSaltHex(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		out = append(out, windowsSourceDB{rel: rel, path: path, salt: salt})
		salts[salt] = true
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, salts, err
}

func windowsReadSaltHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b := make([]byte, 16)
	if _, err := f.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func windowsVerifyDBKey(path, keyHex, saltHex string) bool {
	db, err := wcdb.OpenWithEncKey(path, keyHex, saltHex)
	if err != nil {
		return false
	}
	defer db.Close()
	rows, err := db.Query("SELECT count(*) AS c FROM sqlite_master")
	return err == nil && len(rows) > 0
}

func windowsTargetProcesses() ([]windowsProcess, error) {
	if raw := firstEnv("WECHAT_CLI_WECHAT_PID", "WX_MCP_WECHAT_PID"); raw != "" {
		var out []windowsProcess
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if part == "" {
				continue
			}
			pid, err := strconv.ParseUint(part, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse WECHAT_CLI_WECHAT_PID=%q: %w", raw, err)
			}
			out = append(out, windowsProcess{pid: uint32(pid), exe: "env"})
		}
		return out, nil
	}
	names := map[string]bool{"weixin.exe": true, "wechat.exe": true}
	if raw := firstEnv("WECHAT_CLI_WECHAT_PROCESS", "WX_MCP_WECHAT_PROCESS"); raw != "" {
		names = map[string]bool{}
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			if !strings.HasSuffix(part, ".exe") {
				part += ".exe"
			}
			names[part] = true
		}
	}
	all, err := windowsEnumerateProcesses()
	if err != nil {
		return nil, err
	}
	var out []windowsProcess
	currentPID := windowsCurrentPID()
	for _, p := range all {
		if p.pid == currentPID {
			continue
		}
		if names[strings.ToLower(p.exe)] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].exe != out[j].exe {
			return out[i].exe < out[j].exe
		}
		return out[i].pid < out[j].pid
	})
	return out, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func windowsEnumerateProcesses() ([]windowsProcess, error) {
	snap, _, err := procCreateToolhelp32.Call(th32csSnapProcess, 0)
	if snap == uintptr(syscall.InvalidHandle) || snap == 0 {
		return nil, err
	}
	defer procCloseHandle.Call(snap)
	var entry windowsProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, err := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r == 0 {
		return nil, err
	}
	var out []windowsProcess
	for {
		out = append(out, windowsProcess{
			pid: entry.ProcessID,
			exe: syscall.UTF16ToString(entry.ExeFile[:]),
		})
		entry.Size = uint32(unsafe.Sizeof(entry))
		r, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if r == 0 {
			break
		}
	}
	return out, nil
}

func windowsCurrentPID() uint32 {
	r, _, _ := procGetCurrentProcessID.Call()
	return uint32(r)
}

func windowsScanProcess(pid uint32, targetSalts map[string]bool, found map[string]string) error {
	h, _, err := procOpenProcess.Call(processVMRead|processQueryInformation, 0, uintptr(pid))
	if h == 0 {
		return err
	}
	defer procCloseHandle.Call(h)
	const maxUserAddress = uintptr(0x00007fffffffffff)
	for addr := uintptr(0); addr < maxUserAddress; {
		if len(found) == len(targetSalts) {
			return nil
		}
		var m windowsMemoryBasicInformation
		r, _, _ := procVirtualQueryEx.Call(h, addr, uintptr(unsafe.Pointer(&m)), unsafe.Sizeof(m))
		if r == 0 {
			addr += 0x10000
			continue
		}
		next := m.BaseAddress + m.RegionSize
		if next <= addr {
			return nil
		}
		if windowsReadableRegion(m) {
			windowsScanRegion(h, m.BaseAddress, m.RegionSize, targetSalts, found)
		}
		addr = next
	}
	return nil
}

func windowsReadableRegion(m windowsMemoryBasicInformation) bool {
	return m.State == memCommit && m.RegionSize > 0 && m.Protect&pageNoAccess == 0 && m.Protect&pageGuard == 0
}

func windowsScanRegion(process uintptr, base, size uintptr, targetSalts map[string]bool, found map[string]string) {
	const chunkSize = 4 << 20
	var overlap []byte
	for off := uintptr(0); off < size; {
		if len(found) == len(targetSalts) {
			return
		}
		n := chunkSize
		if remain := size - off; remain < uintptr(n) {
			n = int(remain)
		}
		buf := make([]byte, n)
		var got uintptr
		r, _, _ := procReadProcessMemory.Call(process, base+off, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&got)))
		if r != 0 && got > 0 {
			data := append(append([]byte{}, overlap...), buf[:got]...)
			scanRawKeyLiterals(data, targetSalts, found)
			if len(data) > 128 {
				overlap = append(overlap[:0], data[len(data)-128:]...)
			} else {
				overlap = append(overlap[:0], data...)
			}
		}
		off += uintptr(n)
	}
}

func scanRawKeyLiterals(data []byte, targetSalts map[string]bool, found map[string]string) int {
	var hits int
	for i := 0; i+99 <= len(data); i++ {
		if data[i] != 'x' || data[i+1] != '\'' || data[i+98] != '\'' {
			continue
		}
		hexBytes := data[i+2 : i+98]
		if !asciiHex(hexBytes) {
			continue
		}
		salt := strings.ToLower(string(hexBytes[64:96]))
		if !targetSalts[salt] {
			continue
		}
		key := strings.ToLower(string(hexBytes[:64]))
		if _, err := hex.DecodeString(key); err != nil {
			continue
		}
		found[salt] = key
		hits++
	}
	return hits
}

func asciiHex(b []byte) bool {
	for _, c := range b {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func wxidFromAccountDir(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if idx := strings.LastIndex(name, "_"); idx > 0 {
		return name[:idx]
	}
	return name
}
