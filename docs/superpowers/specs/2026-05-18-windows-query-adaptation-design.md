# wx-mcp Windows Query Adaptation Design

Historical note: this document predates the project rename to `wechat-cli`;
old `wx-mcp` names are retained when describing original design constraints.

Date: 2026-05-18
Status: ready for implementation review
Decision: implement option A, the Windows query/runtime edition.

Update after implementation: Windows now also includes an automatic key adapter.
When the matching Windows WeChat account is logged in, `wx-mcp` scans the
running `Weixin.exe` / `WeChat.exe` process for SQLCipher raw-key literals,
verifies them against the configured `db_storage` databases, and writes the
verified schema-2 key map to `config.json`. The original query/runtime-only
scope below is retained as historical design context.

## Goal

Make `wx-mcp` usable on Windows with a loadable Windows WCDB dynamic library and
either an existing schema-2 key map or a logged-in Windows WeChat process that
can be scanned for verified raw keys.

The first Windows release must run the MCP server, CLI tools, cache refresh, and
cache-backed queries on Windows. It also includes an in-process key adapter that
does not print key material and only persists keys after DB verification.

## Non-goals

- Do not implement Windows privilege escalation or credential storage in this
  work; key extraction is limited to scanning same-user logged-in WeChat
  processes.
- Do not change the macOS no-SIP `wxkey bootstrap` flow.
- Do not weaken DB path containment or readonly protections.
- Do not remove the release-zip macOS installer path.

## Current Findings

The query server is mostly Go and can be made cross-platform, but these areas are
currently macOS-specific:

- `internal/config/config.go` hardcodes the macOS WeChat container path.
- `cmd/wx-mcp/main.go` only resolves `libWCDB.dylib`.
- `cmd/wx-mcp/cache.go` starts background refresh through `/bin/sh`, writes logs
  under `~/Library/Logs`, and uses Unix-only process attributes.
- `install.sh` and `scripts/package.sh` only produce Darwin arm64 releases.
- The bundled `wxkey` behavior is macOS-specific and is treated as an external
  companion CLI by `wx-mcp`.

The main speed bottlenecks are:

- `refreshCache` snapshots source DBs sequentially.
- `buildIndexMessages` uses `LIMIT/OFFSET`, which becomes slower as message
  tables grow.
- Message rows are inserted one row at a time through repeated SQL preparation.
- Indexes and FTS are built in the same broad transaction as row import, limiting
  tuning options.

## User-Facing Contract

On Windows, setup succeeds when all of these are true:

- A Windows build of `wx-mcp.exe` is installed.
- A compatible WCDB dynamic library is available as `libWCDB.dll`, `WCDB.dll`, or
  an explicit `WX_MCP_WCDB_LIB` / `WX_MCP_WCDB_DYLIB` path.
- `config.json` contains `db_root`; schema-version-2 `keys` may already exist or
  may be written by the Windows key adapter.
- `db_root` points to a WeChat account directory containing `db_storage`.

If keys are missing and scanning cannot verify any key, Windows returns a
precise error:

`no usable Windows WeChat raw keys found after scanning ...; ensure WX_MCP_DB_ROOT matches the logged-in account`

## Configuration And Paths

Implementation will add platform-aware path helpers while preserving existing
config file compatibility.

Files:

- `internal/config/config.go`
- `internal/config/wechat_base_darwin.go`
- `internal/config/wechat_base_windows.go`
- `internal/config/wechat_base_other.go`

Behavior:

- Keep `~/.config/wxcli/config.json` as the cross-platform default, because this
  preserves compatibility with existing configs and scripts.
- Add `WX_MCP_CONFIG` to point at an explicit config file.
- Add `WX_MCP_DB_ROOT` to bypass autodetection and set the account root.
- On Windows, `DefaultWeChatBase` checks likely WeChat Files roots:
  - `%USERPROFILE%\Documents\WeChat Files`
  - `%USERPROFILE%\Documents\WeChat Files\xwechat_files`
  - `%USERPROFILE%\WeChat Files`
  - `%USERPROFILE%\WeChat Files\xwechat_files`
  - `%APPDATA%\Tencent\WeChat\WeChat Files`
  - `%APPDATA%\Tencent\WeChat\WeChat Files\xwechat_files`
  - `%USERPROFILE%\AppData\Roaming\Tencent\WeChat\WeChat Files`
  - `%USERPROFILE%\AppData\Roaming\Tencent\WeChat\WeChat Files\xwechat_files`
- `AutoDetectDBRoot` keeps the existing safety rule: if multiple account
  directories have `db_storage`, refuse to guess and ask for `WX_MCP_DB_ROOT`.

## WCDB Library Resolution

Implementation will replace `findWCDB()` with a platform-aware resolver.

Files:

- `cmd/wx-mcp/main.go`
- possibly `cmd/wx-mcp/platform_paths.go`

Behavior:

- Keep `WX_MCP_WCDB_DYLIB` for backward compatibility.
- Add `WX_MCP_WCDB_LIB` as the platform-neutral override.
- On Darwin, search existing `.dylib` locations unchanged.
- On Windows, search:
  - beside `wx-mcp.exe`: `libWCDB.dll`, `WCDB.dll`
  - `.\lib\libWCDB.dll`, `.\lib\WCDB.dll`
  - `%USERPROFILE%\.config\wxcli\lib\libWCDB.dll`
  - `%USERPROFILE%\.config\wxcli\lib\WCDB.dll`
- Error messages must name the platform-specific filenames.

## Key Handling

Implementation will split key refresh behavior by platform.

Files:

- `internal/wxkey/wxkey.go`
- `internal/wxkey/wxkey_darwin.go`
- `internal/wxkey/wxkey_windows.go`
- `cmd/wx-mcp/main.go`

Behavior:

- Darwin keeps the current `wxkey setup --quiet` fallback.
- Windows attempts automatic same-user setup by scanning `Weixin.exe` /
  `WeChat.exe` for SQLCipher raw-key literals, then verifies each candidate key
  against local DB files before writing schema-2 keys.
- Existing schema-2 keys continue to work identically on both platforms.

## Background Cache Refresh

Implementation will replace shell-specific background refresh with a Go-native
process spawn.

Files:

- `cmd/wx-mcp/cache.go`
- `cmd/wx-mcp/background_darwin.go`
- `cmd/wx-mcp/background_windows.go`
- `cmd/wx-mcp/background_other.go`

Behavior:

- Use `exec.Command(exe, "cache", "refresh", ...)` directly.
- Pass `WX_MCP_CACHE_LOCK_HELD=<lockPath>` to the child.
- Put logs in a platform state/log directory:
  - Darwin: `~/Library/Logs/wx-mcp`
  - Windows: `%LOCALAPPDATA%\wx-mcp\logs`, falling back to `~\.wx-mcp\logs`
  - Other: `~/.wx-mcp/logs`
- Preserve the current lock directory semantics and stale lock cleanup.
- On Windows, detach with Windows-compatible `SysProcAttr` only if available;
  otherwise start and release the child without Unix process attributes.

## Windows Installer

Add a PowerShell installer instead of forcing `install.sh` onto Windows.

Files:

- `install.ps1`
- `mcp-server.json`
- `README.md`
- `AGENTS.md`

Command:

```powershell
.\install.ps1 -All -Yes -Json
```

Behavior:

- Copy `wx-mcp.exe` and WCDB DLL into `%LOCALAPPDATA%\wx-mcp` by default.
- Accept `WX_MCP_INSTALL_DIR` override.
- Register supported MCP clients when their CLIs exist:
  - `codex mcp add wx-mcp -- <install-dir>\wx-mcp.exe`
  - `claude mcp add -s user wx-mcp <install-dir>\wx-mcp.exe`
- Run `wx-mcp.exe cache refresh --force` in the foreground when `-All` or
  `-Refresh` is provided, so the installer only reports `ready` after Windows
  key setup and cache build have actually succeeded. `-BackgroundRefresh` is an
  explicit opt-in for fire-and-forget preheating.
- Emit one JSON object with `status`, `actions`, `warnings`, `errors`,
  `blocked_by`, `next_action`, and `log`.
- If keys or DLL are missing, return `status=blocked` with actionable
  `next_action`, not a stack trace.

## Packaging

Add Windows package support while keeping Darwin packaging intact.

Files:

- `scripts/package.sh`
- `scripts/package-windows.ps1`
- `mcp-server.json`

Outputs:

- `wx-mcp-vX.Y.Z-windows-amd64.zip`
- `wx-mcp-latest-windows-amd64.zip`

Package contents:

- `wx-mcp.exe`
- `libWCDB.dll` or `WCDB.dll`
- `install.ps1`
- `README.md`
- `AGENTS.md`
- `LICENSE`
- `SECURITY.md`
- `THIRD_PARTY_NOTICES.md`
- `mcp-server.json`

The manifest should advertise both platforms once the Windows path is usable:

- `darwin-arm64`
- `windows-amd64`

## Speed Optimization Plan

### 1. Snapshot DBs concurrently

Change `refreshCache` so DB snapshot work runs with a small worker pool.

Default worker count:

- `min(4, runtime.NumCPU())`
- override with `WX_MCP_CACHE_WORKERS`

Keep deterministic output by sorting `metas` by `RelPath` after workers finish.
Do not parallelize writes to `index.sqlite`; only source DB snapshotting is
parallelized.

### 2. Replace OFFSET pagination

Change message import from:

```sql
ORDER BY sort_seq DESC LIMIT ? OFFSET ?
```

to seek pagination:

```sql
WHERE sort_seq < ?
ORDER BY sort_seq DESC
LIMIT ?
```

Fallback to `local_id` when `sort_seq` is absent or unusable. This avoids
increasing scan cost on later pages.

### 3. Add prepared insert support

Extend `internal/wcdb` with a small prepared statement wrapper:

- `Prepare(sql string) (*Stmt, error)`
- `Stmt.Exec(args ...any) error`
- `Stmt.Close() error`

Use it for hot import paths:

- `contacts_unified`
- `sessions_unified`
- `messages_unified`
- `cache_files`

### 4. Build heavy indexes after import

Create core tables first, import rows, then create message indexes and FTS.
Keep the final `index.sqlite` replacement atomic.

Suggested PRAGMAs for the temporary index build:

```sql
PRAGMA journal_mode=OFF;
PRAGMA synchronous=OFF;
PRAGMA temp_store=MEMORY;
PRAGMA locking_mode=EXCLUSIVE;
```

After atomic rename, normal reads can still open the completed index readonly.

### 5. Optional FTS deferral

Keep FTS enabled by default. Add `WX_MCP_CACHE_SKIP_FTS=1` for very large first
builds. When skipped, search returns a clear `fts_ready=false` diagnostic and
can use existing LIKE fallback only when explicitly requested.

## Implementation Order

1. Add platform-aware config path and WeChat root detection.
2. Add platform-aware WCDB library resolution.
3. Split key refresh so Windows uses the in-process key adapter and fails
   clearly when no verified keys are found.
4. Replace background refresh shell script with Go-native spawn.
5. Add `install.ps1` and update manifest/docs for Windows.
6. Implement concurrent snapshot worker pool.
7. Implement seek pagination and prepared inserts.
8. Move heavy index creation after import and add cache tuning PRAGMAs.
9. Add tests for Windows path detection, library candidate lists, missing-key
   errors, background spawn argument construction, and cache pagination helpers.
10. Run `go test ./...` on Windows.

## Acceptance Criteria

Windows runtime:

- `go test ./...` passes on Windows.
- With `WX_MCP_DB_ROOT`, `WX_MCP_WCDB_LIB`, and ready schema-2 keys, these work:
  - `wx-mcp.exe cache status`
  - `wx-mcp.exe cache refresh`
  - `wx-mcp.exe sessions --limit 5`
  - `wx-mcp.exe search --keyword test --limit 5`
- With missing keys and a logged-in matching WeChat process, Windows verifies and
  writes schema-2 keys; if scan fails, it returns an actionable mismatch/login
  error.
- `cache refresh --background` starts without `/bin/sh`.
- `install.ps1 -All -Yes -Json` emits valid JSON in success and blocked
  states.

Performance:

- Full cache refresh should be measurably faster on multi-core machines when
  more than one source DB changed.
- Message import should not show OFFSET-style degradation on later pages.
- Index build remains atomic: failed refresh never replaces a good
  `index.sqlite`.

macOS regression:

- Existing `install.sh --all --yes --json` behavior remains unchanged.
- Existing macOS `wxkey setup` fallback remains unchanged.
- Existing cache freshness checks and lock semantics remain unchanged.

## Risks

- A compatible Windows WCDB DLL may not export exactly the same symbols as the
  bundled Darwin dylib. The resolver can find the DLL, but symbol registration
  still has to fail loud if exports differ.
- Windows WeChat DB layout may differ from macOS in media paths or account root
  shape. The first implementation should gate uncertain media path logic behind
  tests and return empty `local_paths` rather than guessing unsafe paths.
- Parallel snapshotting may increase disk pressure. The worker count must stay
  conservative and configurable.
- FTS availability depends on the WCDB SQLite build. Existing code already
  records `fts_ready`; Windows must preserve that behavior.

## Self-review

- No placeholder requirements remain.
- Scope covers Windows query/runtime mode plus same-user automatic key scanning
  for logged-in `Weixin.exe` / `WeChat.exe` processes.
- The implementation order is file-backed and can be executed directly.
- Acceptance criteria cover success, blocked states, performance, and macOS
  regression.
