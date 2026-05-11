# wx-mcp Agent Guide

This repository is meant to be installed and operated by an agent for a user.

## Fast Path

```bash
./install.sh --dry-run --all --json
./install.sh --all --yes --json
```

After install, verify with:

```bash
~/.local/share/wx-mcp/wx-mcp cache status
```

Then call MCP tools, starting with `sessions` or `resolve_chat`.

## Required Runtime Facts

- macOS arm64 and WeChat 4.x.
- Runtime DB reads do not require disabling SIP after `~/.config/wxcli/config.json` has a usable key.
- First key setup should use `./wxkey bootstrap`. It may quit, ad-hoc resign, and reopen WeChat.
- `libWCDB.dylib` must be present beside `wx-mcp`, in `~/.config/wxcli/lib/`, or via `WX_MCP_WCDB_DYLIB`.

## Agent Defaults

- Prefer MCP tools over CLI stdout for production agent workflows.
- Run `cache_refresh` after first setup; use `cache_status` to inspect freshness and errors.
- Use `resolve_chat` before tools that accept human names.
- Use `messages` with `fields=lite` unless raw XML or parsed payloads are needed.
- Use `media_resources` after `messages`/`search` when a result is image/video/file-like and the task needs local attachment paths, resource sizes, or download status. Prefer `server_id_str` for 64-bit server IDs if you are copying IDs through JSON.
- Use `search` default `search_mode=fts`; use `search_mode=auto` only when substring recall matters more than latency.
- Use `new_messages.next_cursor` exactly as returned. Cursor format is stable across cache rebuilds.
- Use `export_messages` for large file outputs instead of asking the model to hold all rows in context.

## Failure Handling

- If key setup fails, run `./wxkey doctor`.
- If a display-name chat lookup fails, call `resolve_chat` and pass the returned raw `username`.
- If cache-dependent filters fail, run `wx-mcp cache refresh`.
- Treat `errors[]`, `parse_error`, missing enrichment fields, and cache `message_errors` as actionable diagnostics, not prose.
