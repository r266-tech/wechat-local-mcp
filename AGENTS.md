# wechat-cli Agent Guide

This repository is meant to be installed and operated by an agent for a user on
macOS or Windows.

## Project Identity

`wechat-cli` is a WeChat/微信 local chat history CLI for macOS and Windows
agents. Use it for local WeChat messages, contacts, groups, media, Moments,
favorites, transfers, red packets, unread chats, full-text search, or explicit
MCP compatibility from the user's own machine.

Do not classify it as a screen-control WeChat bot, Accessibility automation,
WeChat Official Account publisher, Mini Program devtools bridge, WeChat Work /
WeCom bot, or reply bot. It is a local-data CLI for WeChat 4.x. The MCP stdio
adapter exists only as explicit compatibility via `wechat-cli serve-mcp`.

## Fast Install

Prefer release bootstrap or latest release zip over a source clone.

Human-friendly macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh
```

Agent JSON macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | env WECHAT_CLI_INSTALL_JSON=1 zsh
```

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 | iex"
powershell -NoProfile -ExecutionPolicy Bypass -Command "[Environment]::SetEnvironmentVariable('WECHAT_CLI_INSTALL_JSON','1','Process'); irm https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 | iex"
```

Default install is CLI-only. It does not register MCP and does not install a
watcher. If the user explicitly needs MCP compatibility, install with `--mcp`
on macOS or `-Mcp` on Windows; the registered command must run
`wechat-cli serve-mcp`.

Release assets:

- `wechat-cli-latest-darwin-arm64.zip`
- `wechat-cli-latest-windows-amd64.zip`
- `wechat-cli-vX.Y.Z-darwin-arm64.zip`
- `wechat-cli-vX.Y.Z-windows-amd64.zip`

Release zip contents:

- macOS: `wechat-cli`, `wxkey`, `libWCDB.dylib`, `install.sh`, docs, manifest,
  and `scripts/install-release.sh`
- Windows: `wechat-cli.exe`, `libWCDB.dll`, `install.ps1`, docs, manifest, and
  `scripts/install-release.ps1`

## Runtime Facts

- macOS arm64 with WeChat 4.x, or Windows amd64 with Windows WeChat / Weixin 4.x.
- macOS first key setup should use `./wxkey bootstrap`. It may quit/reopen
  WeChat, sign a wechat-cli shadow copy, and store a wxkey sudo credential in
  Keychain.
- macOS runtime DB reads and key refreshes do not require disabling SIP after
  `wxkey bootstrap` has stored the sudo credential and written a schema-2 key map.
- Windows first key setup is built into `wechat-cli.exe cache refresh --force`.
  Keep Windows WeChat logged in and open at least one chat first.
  Windows key scan defaults to a 3-minute timeout; set
  `WECHAT_CLI_KEY_SCAN_TIMEOUT=5m` only on slow machines that need longer.
- `libWCDB.dylib` must be present beside `wechat-cli` on macOS; `libWCDB.dll`
  must be present beside `wechat-cli.exe` on Windows.
- Install dir defaults: `~/.local/share/wechat-cli` on macOS and
  `%LOCALAPPDATA%\wechat-cli` on Windows.
- Command shim defaults to `~/.local/bin/wechat-cli` on macOS. On Windows it
  defaults to `%LOCALAPPDATA%\Microsoft\WindowsApps\wechat-cli.cmd` when that
  directory exists, otherwise `%USERPROFILE%\.local\bin\wechat-cli.cmd`.
- State/cache dir defaults to `~/.wechat-cli`.
- Key config remains wxkey-compatible at `~/.config/wxcli/config.json`.
- Preferred env prefix is `WECHAT_CLI_*`; legacy `WX_MCP_*` aliases still work.

## macOS Password Step

`wxkey bootstrap` needs `task_for_pid` permission. The supported path is no-SIP:
prepare an ad-hoc signed wechat-cli shadow copy of WeChat when needed, ask the
user for their Mac admin password once, verify it with sudo, and store it in the
user's macOS Keychain.

Agents may run:

```bash
./install.sh --all --yes --json
```

The user only answers the hidden local password prompt. Later metadata cache
refreshes, DB decryption, image-key refreshes, and key re-scans use the stored
sudo credential unattended.

## Update

For an existing release-zip install, download and extract the newest release zip
first, then run from the newly extracted directory:

```bash
./install.sh --update --yes --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

For an existing git checkout, the same update command runs `git pull --ff-only`
when possible and reinstalls binaries. It does not rerun bootstrap, refresh,
MCP registration, or watcher setup unless flags are explicitly added.

Verify after install:

```bash
wechat-cli sessions
```

```powershell
wechat-cli sessions
```

## Reset / Uninstall

Use dry-run before destructive cleanup:

```bash
./install.sh --clear-state --dry-run --json
./install.sh --uninstall --purge-state --dry-run --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -ClearState -DryRun -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -PurgeState -DryRun -Json
```

`--uninstall` removes installed files, watcher plist, and both `wechat-cli` and
legacy `wx-mcp` MCP registrations by default, while preserving key/state unless
`--purge-state` is passed. `--clear-state` removes user state:
`~/.config/wxcli/config.json`, `~/.wechat-cli`, legacy `~/.wx-mcp`, logs, the
stored wxkey sudo credential, and any watcher that would recreate state.

Do not ask the user to manually delete key config, cache directories, logs, or
Keychain credentials.

## TCC Quiet Mode

After macOS install completes, advise the user once:

1. Open **System Settings -> Privacy & Security -> Full Disk Access**.
2. Add both `~/.local/share/wechat-cli/wechat-cli` and
   `~/.local/share/wechat-cli/wxkey`.

Without this, macOS 15+ may prompt on cross-container DB reads. The installer
does not install a launchd watcher by default for the same reason.

## Agent Defaults

- Prefer CLI commands over MCP tools for production agent workflows.
- Use `wechat-cli --help`, `wechat-cli tools`, `wechat-cli call <tool> --key value`,
  and `wechat-cli call-json <tool> '{...}'` as the stable interface.
- Do not ask the user to run diagnostics manually. The agent runs CLI
  diagnostics and setup retries; the user only performs OS or WeChat GUI actions
  an agent cannot do.
- Do not manually run `cache refresh` before normal reads. Metadata-backed
  commands perform an internal refresh gate before returning data; use
  `cache status` only to inspect diagnostics and errors.
- Use `resolve-chat` before commands that accept human names when ambiguity matters.
- Use `timeline` as the normal chat-reading entrypoint. It resolves `chat`,
  reads live messages, defaults to `order=desc` plus `display_order=asc`, and
  returns `query` / `freshness` / `messages`. Use `query.has_more` and
  `query.next_offset` to page through a whole chat.
- Use `history --view agent` only when lower-level filters or ordering are
  needed. Do not use `fields=full` for normal user-facing chat reading.
- Use `export` for large single-chat file outputs instead of asking the model to
  hold all rows in context.

Agent-ready message rows include:

`id(local_id/server_id_str/talker)`, `time`, `create_time`, `time_iso`, `sender`,
`sender_wxid`, `is_from_me`, `kind`, `text`, display-ready `images` / `videos` /
`files` / `link` / `music` / `miniprogram` / `forward_chat` / `quote` /
`transfer` / `red_packet` / `location` / `voice.transcript` / `solitaire`, and
concise `warnings`.

Default output follows the WeChat UI principle: if a human sees information in
WeChat and it affects understanding, return it by default. Implementation
materials such as protocol codes, CDN/aeskey, raw XML, unreadable `.dat`, raw
SILK, and duplicate candidate paths belong in `include_debug=true`,
`fields=full`, or `media --debug`.

Images/videos in agent view should expose directly readable local `path` values
only. Voice rows are read from `media_0.db` / `VoiceInfo`; wechat-cli attempts
local transcription with `faster-whisper large-v3` first and returns
`voice.transcript` by default, while raw SILK/WAV paths stay in debug/media
output.

wechat-cli best-effort decodes local image `.dat` files into
`~/.wechat-cli/media-cache`; if WeChat V4 image `image_key` or `image_xor_key`
is missing/stale, it runs `wxkey image-key`, writes refreshed key material to
config, and retries decoding. It does not perform visual recognition.

## Failure Handling

- If macOS key setup fails, inspect installer `blocked_by` / `next_action`, then
  run `./wxkey doctor` if needed. Do not suggest disabling SIP.
- If macOS key setup returns partial key coverage, run `./wxkey doctor`, identify
  missing DB paths, ask the user to open only the corresponding chats/pages in
  WeChat, wait briefly, then rerun `./wxkey setup`.
- If Windows key setup fails, inspect installer `blocked_by` / `next_action`,
  confirm WeChat/Weixin is logged in, confirm `WECHAT_CLI_DB_ROOT` points to the
  account directory that directly contains `db_storage`, and rerun
  `.\wechat-cli.exe cache refresh --force`.
- If a display-name lookup fails, call `resolve-chat` and pass the returned raw
  `username` through `--talker` or `--chat`.
- Treat `freshness`, `warnings`, `errors[]`, `parse_error`, and missing
  enrichment fields as actionable diagnostics, not prose.
