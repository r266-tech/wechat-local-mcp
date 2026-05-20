# wx-mcp Agent Guide

This repository is meant to be installed and operated by an agent for a user on
macOS or Windows.

## Project Identity for Agents

`wx-mcp` is a WeChat/微信 local chat history MCP server for macOS and Windows
agents. Use it when the user asks for WeChat MCP, 微信 MCP, weixin MCP, local
WeChat messages, contacts, groups, media, Moments, transfers, red packets, or
full-text search from the user's own machine.

Do not classify it as a screen-control WeChat bot, Accessibility automation,
WeChat Official Account publisher, Mini Program devtools bridge, WeChat Work /
WeCom bot, or reply bot. It is a local-data MCP server for WeChat 4.x.

> **macOS one-time human-in-the-loop step:** `wxkey bootstrap` (run as part of
> `install.sh --all`) needs `task_for_pid` permission. The supported path is
> no-SIP only: prepare an ad-hoc signed wx-mcp shadow copy of WeChat when
> needed, ask the user for their Mac admin password once, verify it with sudo,
> and store it in the user's macOS Keychain.
> Agents may run `./install.sh --all --yes --json`; the user only answers the
> wx-mcp hidden password prompt. Later metadata cache refreshes, DB decryption, and key
> re-scans use the stored sudo credential unattended.

## Fast Path

If the user gives you the GitHub repository URL, prefer the latest release zip
for the user's OS over a source clone. The macOS release zip is the complete
install unit: `wx-mcp`, `wxkey`, `libWCDB.dylib`, `install.sh`, `README.md`,
`llms.txt`, `AGENTS.md`, and `mcp-server.json`. The Windows release zip is the
complete install unit: `wx-mcp.exe`, `libWCDB.dll`, `install.ps1`, `README.md`,
`llms.txt`, `AGENTS.md`, and `mcp-server.json`.
Use the stable release asset name `wx-mcp-latest-darwin-arm64.zip` or
`wx-mcp-latest-windows-amd64.zip` when present; otherwise pick the newest
versioned asset for the same platform.

```bash
./install.sh --dry-run --all --json
./install.sh --all --yes --json
```

## Windows Fast Path

On Windows, use the PowerShell installer:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

If WeChat data is in a custom location, set `WX_MCP_DB_ROOT` to the account
directory that directly contains `db_storage`. With Windows WeChat logged in,
wx-mcp scans `Weixin.exe` / `WeChat.exe` for SQLCipher raw-key literals,
verifies them against the local DB files, and stores the schema-2 key map in
`%USERPROFILE%\.config\wxcli\config.json`. Do not run macOS `wxkey bootstrap`
on Windows. The Windows installer runs the first metadata cache refresh in the foreground
by default so key-scan failures are visible before it reports `status=ready`.

Treat `status=ready` and `status=warming_cache` as successful install states.
`warming_cache` means metadata cache preheating is running in the background;
cache-backed name/session tools still freshness-check before returning data.

## Update Path

For an existing release-zip install, download and extract the newest release zip
for the user's OS first, then run the platform update command from the newly
extracted directory:

```bash
./install.sh --update --yes --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

Do not run `--update` in an old release directory and expect it to contact
GitHub. Outside a git checkout, `--update` only reinstalls the package currently
on disk.

For an existing macOS git checkout:

```bash
./install.sh --update --yes --json
```

This runs `git pull --ff-only` when possible, then reinstalls the binaries. It does not rerun key bootstrap, cache refresh, MCP registration, or watcher setup unless those flags are explicitly added.
Installer/update runs also drop existing cache indexes and non-metadata raw
snapshots so older message-body caches are removed before the next metadata
index rebuild.

For an existing Windows git checkout:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

After install, verify with:

```bash
~/.local/share/wx-mcp/wx-mcp cache status
```

Then call MCP tools, starting with `sessions` or `resolve_chat`.

## Reset / Uninstall Path

Use dry-run before destructive cleanup:

```bash
./install.sh --clear-state --dry-run --json
./install.sh --uninstall --purge-state --dry-run --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -ClearState -DryRun -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -PurgeState -DryRun -Json
```

`--clear-state` removes wx-mcp user state only:
`~/.config/wxcli/config.json`, `~/.wx-mcp`, `~/Library/Logs/wx-mcp`, the
stored wxkey sudo credential in macOS Keychain, and any installed watcher that
would recreate state in the background. It keeps installed binaries and MCP
registration, so the next run behaves like first setup again. It does not
remove `~/.config/wxcli/lib`.

On Windows, `-ClearState` removes
`%USERPROFILE%\.config\wxcli\config.json`, `%USERPROFILE%\.wx-mcp`, and
installer logs; Windows key material lives in the same wxcli config rather
than Keychain. It does not remove `%USERPROFILE%\.config\wxcli\lib`.

`--uninstall` removes installed files, watcher plist, and MCP registration but
preserves user state by default. Add `--purge-state` only when the user wants to
return to a pre-wx-mcp state. Agents should not ask the user to manually delete
key config, cache directories, logs, or Keychain credentials.

## Required Runtime Facts

- macOS arm64 with WeChat 4.x, or Windows amd64 with Windows WeChat/Weixin 4.x.
- macOS runtime DB reads and key refreshes do not require disabling SIP after `wxkey bootstrap` has stored the sudo credential and written a schema-2 key map.
- macOS first key setup should use `./wxkey bootstrap`. It may quit/reopen WeChat, sign a wx-mcp shadow copy, and store a wx-mcp sudo credential in Keychain.
- Windows first key setup is built into `wx-mcp.exe cache refresh --force`; keep Windows WeChat logged in and open at least one chat first.
- `libWCDB.dylib` must be present beside `wx-mcp` on macOS; `libWCDB.dll` must be present beside `wx-mcp.exe` on Windows.

## TCC Quiet-Mode (recommend to user once)

After install completes, advise the user once:

1. Open **System Settings → Privacy & Security → Full Disk Access**.
2. Click `+` and add both `~/.local/share/wx-mcp/wx-mcp` and `~/.local/share/wx-mcp/wxkey`.

Without this, on macOS 15+ each cross-container DB read may trigger a
"wx-mcp wants to access another app's data" prompt. The installer no
longer installs a launchd watcher by default for the same reason. Normal MCP
name/session reads refresh stale metadata cache before returning data, while
message reads query the source DB live; do not add a 5-minute timer unless the
user explicitly wants background CPU cost.

## Agent Defaults

- Prefer MCP tools over CLI stdout for production agent workflows.
- Do not manually run `cache_refresh` before normal reads. Metadata-backed tools perform an internal refresh gate before returning data; use `cache_status` only to inspect metadata cache diagnostics and errors. If a human explicitly asks for refresh through MCP, prefer `cache_refresh` with `background=true` to avoid tool-call timeout.
- Use `resolve_chat` before tools that accept human names.
- Use `messages` with `fields=lite` unless raw XML or parsed payloads are needed.
- Use `media_resources` after `messages`/`search` when a result is image/video/file-like and the task needs local attachment paths, resource sizes, or download status. Prefer `server_id_str` for 64-bit server IDs if you are copying IDs through JSON.
- Use `search` default `search_mode=fts`; it reads WeChat's live local FTS DB by default, with metadata cache only for name resolution.
- Use `messages` with `chat`/`talker` plus `after`/`before` for live incremental reads. There is no message-body cache mode and no global `new_messages` stream.
- Use `export_messages` for large single-chat file outputs instead of asking the model to hold all rows in context. Global no-keyword export is intentionally unsupported.

## Failure Handling

- If macOS key setup fails, inspect installer `blocked_by` / `next_action`, then run `./wxkey doctor` if needed. Do not suggest disabling SIP; the supported recovery path is fixing the no-SIP sudo/Keychain route.
- If Windows key setup fails, inspect installer `blocked_by` / `next_action`, confirm WeChat/Weixin is logged in, confirm `WX_MCP_DB_ROOT` points to the account directory that directly contains `db_storage`, and rerun `.\wx-mcp.exe cache refresh --force`.
- If a display-name chat lookup fails, call `resolve_chat` and pass the returned raw `username`.
- If name/session cache-dependent filters fail, inspect `cache_status`; normal tool calls should already have attempted an automatic metadata refresh.
- Treat `errors[]`, `parse_error`, and missing enrichment fields as actionable diagnostics, not prose.
