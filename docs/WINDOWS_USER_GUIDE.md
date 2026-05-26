# wechat-cli Windows User Guide

This guide is for Windows users who want to install and use `wechat-cli` with a
logged-in Windows WeChat account.

## What wechat-cli Does

`wechat-cli` reads the local Windows WeChat database and exposes chat, contact,
search, media, stats, and export commands as JSON.

On Windows, `wechat-cli` can automatically scan the running `Weixin.exe` or
`WeChat.exe` process for SQLCipher raw keys. It verifies those keys against the
configured WeChat databases, then stores a schema-2 key map in:

```text
%USERPROFILE%\.config\wxcli\config.json
```

Keys are not printed to terminal output or install logs.

## Package Layout

A Windows package should contain these files:

```text
wechat-cli\
  wechat-cli.exe
  libWCDB.dll
  install.ps1
  mcp-server.json
  AGENTS.md
  README.md
  LICENSE
  SECURITY.md
  THIRD_PARTY_NOTICES.md
```

`libWCDB.dll` may also be a SQLCipher-compatible DLL that exports the SQLite and
SQLCipher symbols used by `wechat-cli`.

## Requirements

- Windows 10 or newer.
- Windows WeChat 4.x installed and logged in.
- At least one chat opened after login, so the database keys are present in the
  running WeChat process.
- `wechat-cli.exe` and `libWCDB.dll` in the same install directory.
- If WeChat data is not in a default location, the account directory must be
  configured with `WECHAT_CLI_DB_ROOT`.

The account directory is the folder that directly contains `db_storage`.

Example:

```text
E:\Wechat\message\xwechat_files\wxid_xxxxx_1234
  db_storage\
  msg\
```

Use the account directory itself, not `E:\Wechat` and not `db_storage`.

## Install For Current User

Open PowerShell in the package directory:

```powershell
cd D:\wechat-cli
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

By default, the installer uses `%LOCALAPPDATA%\wechat-cli` unless an install
directory is provided.

`-All` runs the first `cache refresh --force` in the foreground. The installer
only returns `status=ready` after `wechat-cli` has verified Windows key extraction
and built the cache. If you only want to start refresh in the background, add
`-BackgroundRefresh`; in that mode `status=warming_cache` means the background
process was launched, not that key extraction has already completed.

## Install To D:\wechat-cli

To install directly into `D:\wechat-cli`:

```powershell
cd D:\wechat-cli-source
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json -InstallDir D:\wechat-cli
```

Equivalent environment variable:

```powershell
[Environment]::SetEnvironmentVariable("WECHAT_CLI_INSTALL_DIR", "D:\wechat-cli", "User")
```

Open a new PowerShell window after setting persistent environment variables.

## Configure A Non-Default WeChat Data Root

If WeChat data is under a custom path, set `WECHAT_CLI_DB_ROOT` to the account
directory:

```powershell
[Environment]::SetEnvironmentVariable(
  "WECHAT_CLI_DB_ROOT",
  "E:\Wechat\message\xwechat_files\wxid_xxxxx_1234",
  "User"
)
```

For the current PowerShell session only:

```powershell
$env:WECHAT_CLI_DB_ROOT = "E:\Wechat\message\xwechat_files\wxid_xxxxx_1234"
```

Then run:

```powershell
D:\wechat-cli\wechat-cli.exe cache refresh --force
```

## Multiple Accounts

If more than one account directory exists, set `WECHAT_CLI_DB_ROOT` explicitly.

Example discovery command:

```powershell
Get-ChildItem -LiteralPath "E:\Wechat" -Recurse -Directory -Force |
  Where-Object { Test-Path (Join-Path $_.FullName "db_storage") } |
  Select-Object FullName
```

Pick the directory that directly contains `db_storage`.

## Optional Process Overrides

Normally no process override is needed. `wechat-cli` scans running `Weixin.exe` and
`WeChat.exe` processes.

If key extraction fails, specify the WeChat process id:

```powershell
Get-Process Weixin
[Environment]::SetEnvironmentVariable("WECHAT_CLI_WECHAT_PID", "12345", "User")
```

Or only for the current terminal:

```powershell
$env:WECHAT_CLI_WECHAT_PID = "12345"
```

If a future WeChat build uses a different process name:

```powershell
[Environment]::SetEnvironmentVariable("WECHAT_CLI_WECHAT_PROCESS", "Weixin,WeChat", "User")
```

## Verify The Install

Run:

```powershell
D:\wechat-cli\wechat-cli.exe cache status
D:\wechat-cli\wechat-cli.exe cache refresh --force
D:\wechat-cli\wechat-cli.exe sessions --limit 5
D:\wechat-cli\wechat-cli.exe contacts --limit 5
```

A healthy refresh contains non-zero counts for contacts and sessions. Chat
message bodies are not cached; `messages`, `search`, and single-chat
`export_messages` read the live WeChat DB when called.

Example success shape:

```json
{
  "stats": {
    "contacts": 8605,
    "sessions": 316,
    "skipped_live_source": 24,
    "source_dbs": 26
  }
}
```

## Common CLI Commands

```powershell
D:\wechat-cli\wechat-cli.exe sessions --limit 20
D:\wechat-cli\wechat-cli.exe contacts --limit 20
D:\wechat-cli\wechat-cli.exe search "keyword" --limit 20
D:\wechat-cli\wechat-cli.exe history "contact or group name" --limit 50
D:\wechat-cli\wechat-cli.exe media "contact or group name" --type image --limit 10
D:\wechat-cli\wechat-cli.exe stats "contact or group name"
```

If an MCP client is explicitly required, register the optional compatibility
adapter with `-Mcp`; the adapter runs `wechat-cli.exe serve-mcp`.

## Troubleshooting

### PowerShell says scripts are disabled

Use:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

### No account directory with db_storage found

Set `WECHAT_CLI_DB_ROOT` to the account directory that directly contains
`db_storage`.

### No running Weixin.exe or WeChat.exe process found

Start Windows WeChat, log in, open at least one chat, and rerun:

```powershell
D:\wechat-cli\wechat-cli.exe cache refresh --force
```

### No usable Windows WeChat raw keys found

Check that `WECHAT_CLI_DB_ROOT` belongs to the same account currently logged in to
Windows WeChat. If multiple WeChat processes exist, set `WECHAT_CLI_WECHAT_PID`.

### DLL not found

Put `libWCDB.dll` beside `wechat-cli.exe`, or set:

```powershell
[Environment]::SetEnvironmentVariable("WECHAT_CLI_WCDB_LIB", "D:\wechat-cli\libWCDB.dll", "User")
```

### Installed exe is in use during update

Close terminals, agents, or optional MCP clients that are still using
`wechat-cli.exe`, or stop old processes:

```powershell
Get-Process wechat-cli -ErrorAction SilentlyContinue | Stop-Process -Force
```

Then rerun the installer.

## Privacy And Safety

- `wechat-cli` reads local WeChat databases on the same Windows user account.
- Extracted database keys are stored in the local user config file.
- Do not share `config.json`, cache files, or plaintext snapshots.
- Do not upload `C:\Users\<you>\.wechat-cli\cache` or `C:\Users\<you>\.config\wxcli`
  to issue trackers or chat systems.

## Reset / Uninstall

Use the installer so optional MCP client registrations are removed as well as files.

Clear keys/cache/logs but keep the installed binaries:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -ClearState -Yes -Json
```

Uninstall binaries and optional MCP registration while keeping keys/cache:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -Yes -Json
```

Return to a fresh-user state:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -PurgeState -Yes -Json
```

`-ClearState` / `-PurgeState` delete
`%USERPROFILE%\.config\wxcli\config.json`, `%USERPROFILE%\.wechat-cli`, and
installer logs, but keep `%USERPROFILE%\.config\wxcli\lib`.
