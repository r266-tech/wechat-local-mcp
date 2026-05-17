# wx-mcp Agent Guide

This repository is meant to be installed and operated by an agent for a user.

> **One-time human-in-the-loop step:** `wxkey bootstrap` (run as part of
> `install.sh --all`) needs `task_for_pid` permission. The supported path is
> no-SIP only: prepare an ad-hoc signed wx-mcp shadow copy of WeChat when
> needed, ask the user for their Mac admin password once, verify it with sudo,
> and store it in the user's macOS Keychain.
> Agents may run `./install.sh --all --yes --json`; the user only answers the
> wx-mcp hidden password prompt. Later cache refreshes, DB decryption, and key
> re-scans use the stored sudo credential unattended.

## Fast Path

If the user gives you the GitHub repository URL, prefer the latest release zip
over a source clone. The release zip is the complete install unit: `wx-mcp`,
`wxkey`, `libWCDB.dylib`, `install.sh`, docs, and manifest.
Use the stable release asset name `wx-mcp-latest-darwin-arm64.zip` when it is
present; otherwise pick the newest versioned `wx-mcp-v*-darwin-arm64.zip`.

```bash
./install.sh --dry-run --all --json
./install.sh --all --yes --json
```

Treat `status=ready` and `status=warming_cache` as successful install states.
`warming_cache` means cache preheating is running in the background; cache-backed
tools still freshness-check before returning data.

## Update Path

For an existing git checkout:

```bash
./install.sh --update --yes --json
```

This runs `git pull --ff-only` when possible, then reinstalls the binaries. It does not rerun key bootstrap, cache refresh, MCP registration, or watcher setup unless those flags are explicitly added.

After install, verify with:

```bash
~/.local/share/wx-mcp/wx-mcp cache status
```

Then call MCP tools, starting with `sessions` or `resolve_chat`.

## Required Runtime Facts

- macOS arm64 and WeChat 4.x.
- Runtime DB reads and key refreshes do not require disabling SIP after `wxkey bootstrap` has stored the sudo credential and written a schema-2 key map.
- First key setup should use `./wxkey bootstrap`. It may quit/reopen WeChat, sign a wx-mcp shadow copy, and store a wx-mcp sudo credential in Keychain.
- `libWCDB.dylib` must be present beside `wx-mcp`, in `~/.config/wxcli/lib/`, or via `WX_MCP_WCDB_DYLIB`.

## TCC Quiet-Mode (recommend to user once)

After install completes, advise the user once:

1. Open **System Settings → Privacy & Security → Full Disk Access**.
2. Click `+` and add both `~/.local/share/wx-mcp/wx-mcp` and `~/.local/share/wx-mcp/wxkey`.

Without this, on macOS 15+ each cross-container DB read may trigger a
"wx-mcp wants to access another app's data" prompt. The installer no
longer installs a launchd watcher by default for the same reason. Normal MCP
reads refresh stale cache before returning data, so do not add a 5-minute timer
unless the user explicitly wants background CPU cost.

## Agent Defaults

- Prefer MCP tools over CLI stdout for production agent workflows.
- Do not manually run `cache_refresh` before normal reads. Cache-backed tools perform a freshness gate and auto-refresh before returning data; use `cache_status` only to inspect freshness and errors. If a human explicitly asks for refresh through MCP, prefer `cache_refresh` with `background=true` to avoid tool-call timeout.
- Use `resolve_chat` before tools that accept human names.
- Use `messages` with `fields=lite` unless raw XML or parsed payloads are needed.
- Use `media_resources` after `messages`/`search` when a result is image/video/file-like and the task needs local attachment paths, resource sizes, or download status. Prefer `server_id_str` for 64-bit server IDs if you are copying IDs through JSON.
- Use `search` default `search_mode=fts`; use `search_mode=auto` only when substring recall matters more than latency.
- Use `new_messages.next_cursor` exactly as returned. Cursor format is stable across cache rebuilds.
- Use `export_messages` for large file outputs instead of asking the model to hold all rows in context.

## Failure Handling

- If key setup fails, inspect installer `blocked_by` / `next_action`, then run `./wxkey doctor` if needed. Do not suggest disabling SIP; the supported recovery path is fixing the no-SIP sudo/Keychain route.
- If a display-name chat lookup fails, call `resolve_chat` and pass the returned raw `username`.
- If cache-dependent filters fail, inspect `cache_status`; normal tool calls should already have attempted an automatic refresh.
- Treat `errors[]`, `parse_error`, missing enrichment fields, and cache `message_errors` as actionable diagnostics, not prose.
