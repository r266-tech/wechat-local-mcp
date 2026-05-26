# wx-mcp Agent Guide

This repository is meant to be installed and operated by an agent for a user on
macOS or Windows.

## Project Identity for Agents

`wx-mcp` is a WeChat/微信 local chat history CLI for macOS and Windows agents.
Use it when the user asks for local WeChat messages, contacts, groups, media,
Moments, transfers, red packets, unread chats, full-text search, or WeChat MCP
compatibility from the user's own machine.

Do not classify it as a screen-control WeChat bot, Accessibility automation,
WeChat Official Account publisher, Mini Program devtools bridge, WeChat Work /
WeCom bot, or reply bot. It is a local-data CLI for WeChat 4.x. The MCP stdio
adapter exists only as explicit compatibility via `wx-mcp serve-mcp`.

> **macOS one-time human-in-the-loop step:** `wxkey bootstrap` (run as part of
> `install.sh --all`) needs `task_for_pid` permission. The supported path is
> no-SIP only: prepare an ad-hoc signed wx-mcp shadow copy of WeChat when
> needed, ask the user for their Mac admin password once, verify it with sudo,
> and store it in the user's macOS Keychain.
> Agents may run `./install.sh --all --yes --json`; the user only answers the
> wx-mcp hidden password prompt. Later metadata cache refreshes, DB decryption,
> and key re-scans use the stored sudo credential unattended.

## Fast Path

If the user gives you the GitHub repository URL, prefer the release bootstrap
or latest release zip for the user's OS over a source clone. The macOS release
zip is the complete install unit: `wx-mcp`, `wxkey`, `libWCDB.dylib`,
`install.sh`, `README.md`, `llms.txt`, `AGENTS.md`, `mcp-server.json`, and
`scripts/install-release.sh`. The Windows release zip is the complete install
unit: `wx-mcp.exe`, `libWCDB.dll`, `install.ps1`, `README.md`, `llms.txt`,
`AGENTS.md`, `mcp-server.json`, and `scripts/install-release.ps1`.

Use the stable release asset name `wx-mcp-latest-darwin-arm64.zip` or
`wx-mcp-latest-windows-amd64.zip` when present; otherwise pick the newest
versioned asset for the same platform.

Human-friendly one-line macOS install:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.sh | zsh
```

Agent JSON macOS install:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.sh | env WX_MCP_INSTALL_JSON=1 zsh
```

Release zip macOS install:

```bash
./install.sh --dry-run --all --json
./install.sh --all --yes --json
```

Default install is CLI-only. It does not register MCP and does not install a
watcher. If a user explicitly needs MCP compatibility, install with `--mcp`;
the registered command must run `wx-mcp serve-mcp`.

## Windows Fast Path

On Windows, use the PowerShell installer:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.ps1 | iex"
powershell -NoProfile -ExecutionPolicy Bypass -Command "[Environment]::SetEnvironmentVariable('WX_MCP_INSTALL_JSON','1','Process'); irm https://raw.githubusercontent.com/r266-tech/wechat-local-mcp/main/scripts/install-release.ps1 | iex"
```

Release zip Windows install:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

If WeChat data is in a custom location, set `WX_MCP_DB_ROOT` to the account
directory that directly contains `db_storage`. With Windows WeChat logged in,
wx-mcp scans `Weixin.exe` / `WeChat.exe` for SQLCipher raw-key literals,
verifies them against the local DB files, and stores the schema-2 key map in
`%USERPROFILE%\.config\wxcli\config.json`. Do not run macOS `wxkey bootstrap`
on Windows. The Windows installer runs the first metadata cache refresh in the
foreground by default so key-scan failures are visible before it reports
`status=ready`.

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

This runs `git pull --ff-only` when possible, then reinstalls the binaries. It
does not rerun key bootstrap, cache refresh, MCP registration, or watcher setup
unless those flags are explicitly added. Installer/update runs also drop
existing cache indexes and non-metadata raw snapshots so older message-body
caches are removed before the next metadata index rebuild.

For an existing Windows git checkout:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

After install, verify with:

```bash
~/.local/share/wx-mcp/wx-mcp sessions
```

or on Windows:

```powershell
& "$env:LOCALAPPDATA\wx-mcp\wx-mcp.exe" sessions
```

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
`~/.config/wxcli/config.json`, `~/.wx-mcp`, `~/Library/Logs/wx-mcp`, the stored
wxkey sudo credential in macOS Keychain, and any installed watcher that would
recreate state in the background. It keeps installed binaries and optional MCP
entries, so the next run behaves like first setup again. It does not remove
`~/.config/wxcli/lib`.

On Windows, `-ClearState` removes
`%USERPROFILE%\.config\wxcli\config.json`, `%USERPROFILE%\.wx-mcp`, and
installer logs; Windows key material lives in the same wxcli config rather than
Keychain. It does not remove `%USERPROFILE%\.config\wxcli\lib`.

`--uninstall` removes installed files, watcher plist, and legacy MCP
registrations by default but preserves user state. Add `--purge-state` only
when the user wants to return to a pre-wx-mcp state. Agents should not ask the
user to manually delete key config, cache directories, logs, or Keychain
credentials.

## Required Runtime Facts

- macOS arm64 with WeChat 4.x, or Windows amd64 with Windows WeChat/Weixin 4.x.
- macOS runtime DB reads and key refreshes do not require disabling SIP after
  `wxkey bootstrap` has stored the sudo credential and written a schema-2 key map.
- macOS first key setup should use `./wxkey bootstrap`. It may quit/reopen
  WeChat, sign a wx-mcp shadow copy, and store a wx-mcp sudo credential in
  Keychain.
- Windows first key setup is built into `wx-mcp.exe cache refresh --force`;
  keep Windows WeChat logged in and open at least one chat first.
- `libWCDB.dylib` must be present beside `wx-mcp` on macOS; `libWCDB.dll` must
  be present beside `wx-mcp.exe` on Windows.

## TCC Quiet-Mode (recommend to user once)

After install completes, advise the user once:

1. Open **System Settings -> Privacy & Security -> Full Disk Access**.
2. Click `+` and add both `~/.local/share/wx-mcp/wx-mcp` and
   `~/.local/share/wx-mcp/wxkey`.

Without this, on macOS 15+ each cross-container DB read may trigger a
"wx-mcp wants to access another app's data" prompt. The installer no longer
installs a launchd watcher by default for the same reason. Normal CLI
name/session reads refresh stale metadata cache before returning data, while
message reads query the source DB live; do not add a 5-minute timer unless the
user explicitly wants background CPU cost.

## Agent Defaults

- Prefer CLI commands over MCP tools for production agent workflows.
- Use `wx-mcp --help`, `wx-mcp tools`, `wx-mcp call <tool> --key value`, and
  `wx-mcp call-json <tool> '{...}'` as the stable interface.
- Do not ask the user to run `wxkey doctor`, `wxkey setup`, `cache status`, or
  installer diagnostics manually. The agent runs CLI diagnostics and setup
  retries; the user only performs OS or WeChat GUI actions that an agent cannot
  do, such as entering the one-time admin password or opening a specific missing
  chat/page in WeChat.
- Do not manually run `cache refresh` before normal reads. Metadata-backed
  commands perform an internal refresh gate before returning data; use
  `cache status` only to inspect metadata cache diagnostics and errors.
- Use `resolve-chat` before commands that accept human names when ambiguity matters.
- Use `timeline` as the normal chat-reading entrypoint for
  "show/summarize recent chat". It resolves `chat`, reads live messages,
  defaults to `order=desc` plus `display_order=asc`, and returns `query` /
  `freshness` / `messages`. Use `query.has_more` and `query.next_offset` to page
  through a whole chat.
- Treat each `messages[]` row as the agent-ready source of truth:
  `id(local_id/server_id_str/talker)`, `time`, `create_time`, `time_iso`,
  `sender`, `sender_wxid`, `is_from_me`, `kind`, `text`, display-ready
  `images` / `videos` / `files` / `link` / `music` / `miniprogram` /
  `forward_chat` / `quote` / `transfer` / `red_packet` / `location` /
  `voice.transcript` / `solitaire`, and concise `warnings`.
- Use `history --view agent` only when you need lower-level filters or ordering
  but still want the same low-noise `query` / `freshness` / `messages` envelope.
  Do not use `fields=full` for normal user-facing chat reading.
- Default output follows the WeChat UI principle: if a human sees information in
  WeChat and it affects understanding, return it by default; implementation
  materials such as protocol codes, CDN/aeskey, raw XML, unreadable `.dat`, raw
  SILK, and duplicate candidate paths belong in `include_debug=true`,
  `fields=full`, or `media --debug`.
- `history` and `timeline` fill local image/video/file/voice materials
  internally by default; pass `--include-media-paths=false` only when you need
  text-only results. Images/videos in agent view should expose directly
  readable local `path` values only. Voice rows are read from `media_0.db` /
  `VoiceInfo`; wx-mcp attempts local transcription with `faster-whisper
  large-v3` first and returns `voice.transcript` by default, while raw SILK/WAV
  paths stay in debug/media output.
- wx-mcp best-effort decodes local image `.dat` files into
  `~/.wx-mcp/media-cache`; if WeChat V4 image `image_key` or `image_xor_key` is
  missing/stale, wx-mcp first runs `wxkey image-key`, writes the refreshed key
  material to config, and retries decoding. Only failed refreshes should surface
  concise warnings in agent/lite views and detailed `decode_status=needs_image_key`
  in debug/full output. wx-mcp does not perform visual recognition.
- Use `media` after `history`/`search` when you need a separate attachment
  lookup by `local_id`/`server_id`. Its default output is agent-ready: path
  values are only directly readable local files. Raw `.dat`, duplicate
  candidate paths, `file://` URIs, `local_path_details`, raw type/variant codes,
  resource ids/status, and decode internals require `--include-debug` /
  `--debug`.
- Use `search` default `search_mode=fts`; it reads WeChat's live local FTS DB by
  default, with metadata cache only for name resolution.
- Use `history` with `--chat`/`--talker` plus `--after`/`--before` for live
  incremental reads. There is no message-body cache mode and no global
  `new_messages` stream.
- Use `export` for large single-chat file outputs instead of asking the model to
  hold all rows in context. Global no-keyword export is intentionally unsupported.
- If a client still needs MCP, run or register `wx-mcp serve-mcp`; do not point
  MCP clients at bare `wx-mcp`.

## Failure Handling

- If macOS key setup fails, inspect installer `blocked_by` / `next_action`, then
  run `./wxkey doctor` if needed. Do not suggest disabling SIP; the supported
  recovery path is fixing the no-SIP sudo/Keychain route.
- If macOS key setup returns partial key coverage, run `./wxkey doctor`
  yourself, identify the missing DB paths, ask the user to open only the
  corresponding chats/pages in WeChat, wait briefly, then rerun `./wxkey setup`
  yourself. `./wxkey doctor` is the lightweight cached-coverage check; use
  `./wxkey doctor --scan` only when live task_for_pid/current-heap coverage must
  be revalidated.
- If Windows key setup fails, inspect installer `blocked_by` / `next_action`,
  confirm WeChat/Weixin is logged in, confirm `WX_MCP_DB_ROOT` points to the
  account directory that directly contains `db_storage`, and rerun
  `.\wx-mcp.exe cache refresh --force`.
- If a display-name chat lookup fails, call `resolve-chat` and pass the returned
  raw `username` through `--talker` or `--chat`.
- If name/session cache-dependent filters fail, inspect `cache status`; normal
  commands should already have attempted an automatic metadata refresh.
- Treat `errors[]`, `parse_error`, and missing enrichment fields as actionable
  diagnostics, not prose.
