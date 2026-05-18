# wx-mcp Windows User Guide

This guide is for Windows users who want to install and use `wx-mcp` with a
logged-in Windows WeChat account.

## What wx-mcp Does

`wx-mcp` reads the local Windows WeChat database and exposes chat, contact,
search, media, stats, and export tools to MCP clients such as Codex or Claude.

On Windows, `wx-mcp` can automatically scan the running `Weixin.exe` or
`WeChat.exe` process for SQLCipher raw keys. It verifies those keys against the
configured WeChat databases, then stores a schema-2 key map in:

```text
%USERPROFILE%\.config\wxcli\config.json
```

Keys are not printed to terminal output or install logs.

## Package Layout

A Windows package should contain these files:

```text
wx-mcp\
  wx-mcp.exe
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
SQLCipher symbols used by `wx-mcp`.

## Requirements

- Windows 10 or newer.
- Windows WeChat 4.x installed and logged in.
- At least one chat opened after login, so the database keys are present in the
  running WeChat process.
- `wx-mcp.exe` and `libWCDB.dll` in the same install directory.
- If WeChat data is not in a default location, the account directory must be
  configured with `WX_MCP_DB_ROOT`.

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
cd D:\wx-mcp
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -DryRun -All -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

By default, the installer uses `%LOCALAPPDATA%\wx-mcp` unless an install
directory is provided.

`-All` runs the first `cache refresh --force` in the foreground. The installer
only returns `status=ready` after `wx-mcp` has verified Windows key extraction
and built the cache. If you only want to start refresh in the background, add
`-BackgroundRefresh`; in that mode `status=warming_cache` means the background
process was launched, not that key extraction has already completed.

## Install To D:\wx-mcp

To install directly into `D:\wx-mcp`:

```powershell
cd D:\wx-mcp-source
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json -InstallDir D:\wx-mcp
```

Equivalent environment variable:

```powershell
[Environment]::SetEnvironmentVariable("WX_MCP_INSTALL_DIR", "D:\wx-mcp", "User")
```

Open a new PowerShell window after setting persistent environment variables.

## Configure A Non-Default WeChat Data Root

If WeChat data is under a custom path, set `WX_MCP_DB_ROOT` to the account
directory:

```powershell
[Environment]::SetEnvironmentVariable(
  "WX_MCP_DB_ROOT",
  "E:\Wechat\message\xwechat_files\wxid_xxxxx_1234",
  "User"
)
```

For the current PowerShell session only:

```powershell
$env:WX_MCP_DB_ROOT = "E:\Wechat\message\xwechat_files\wxid_xxxxx_1234"
```

Then run:

```powershell
D:\wx-mcp\wx-mcp.exe cache refresh --force
```

## Multiple Accounts

If more than one account directory exists, set `WX_MCP_DB_ROOT` explicitly.

Example discovery command:

```powershell
Get-ChildItem -LiteralPath "E:\Wechat" -Recurse -Directory -Force |
  Where-Object { Test-Path (Join-Path $_.FullName "db_storage") } |
  Select-Object FullName
```

Pick the directory that directly contains `db_storage`.

## Optional Process Overrides

Normally no process override is needed. `wx-mcp` scans running `Weixin.exe` and
`WeChat.exe` processes.

If key extraction fails, specify the WeChat process id:

```powershell
Get-Process Weixin
[Environment]::SetEnvironmentVariable("WX_MCP_WECHAT_PID", "12345", "User")
```

Or only for the current terminal:

```powershell
$env:WX_MCP_WECHAT_PID = "12345"
```

If a future WeChat build uses a different process name:

```powershell
[Environment]::SetEnvironmentVariable("WX_MCP_WECHAT_PROCESS", "Weixin,WeChat", "User")
```

## Verify The Install

Run:

```powershell
D:\wx-mcp\wx-mcp.exe cache status
D:\wx-mcp\wx-mcp.exe cache refresh --force
D:\wx-mcp\wx-mcp.exe sessions --limit 5
D:\wx-mcp\wx-mcp.exe contacts --limit 5
```

A healthy refresh contains non-zero counts for contacts, sessions, and messages,
and `message_errors` should be `0`.

Example success shape:

```json
{
  "stats": {
    "contacts": 8605,
    "messages": 52730,
    "sessions": 316,
    "message_errors": 0
  }
}
```

## Common CLI Commands

```powershell
D:\wx-mcp\wx-mcp.exe sessions --limit 20
D:\wx-mcp\wx-mcp.exe contacts --limit 20
D:\wx-mcp\wx-mcp.exe search "keyword" --limit 20
D:\wx-mcp\wx-mcp.exe history "contact or group name" --limit 50
D:\wx-mcp\wx-mcp.exe media "contact or group name" --type image --limit 10
D:\wx-mcp\wx-mcp.exe stats "contact or group name"
```

When used through an MCP client, call tools such as `sessions`, `messages`,
`search`, `contacts`, `media_resources`, `stats`, and `export_messages`.

## Troubleshooting

### PowerShell says scripts are disabled

Use:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -All -Yes -Json
```

### No account directory with db_storage found

Set `WX_MCP_DB_ROOT` to the account directory that directly contains
`db_storage`.

### No running Weixin.exe or WeChat.exe process found

Start Windows WeChat, log in, open at least one chat, and rerun:

```powershell
D:\wx-mcp\wx-mcp.exe cache refresh --force
```

### No usable Windows WeChat raw keys found

Check that `WX_MCP_DB_ROOT` belongs to the same account currently logged in to
Windows WeChat. If multiple WeChat processes exist, set `WX_MCP_WECHAT_PID`.

### DLL not found

Put `libWCDB.dll` beside `wx-mcp.exe`, or set:

```powershell
[Environment]::SetEnvironmentVariable("WX_MCP_WCDB_LIB", "D:\wx-mcp\libWCDB.dll", "User")
```

### Installed exe is in use during update

Close the MCP client or stop old `wx-mcp.exe` processes:

```powershell
Get-Process wx-mcp -ErrorAction SilentlyContinue | Stop-Process -Force
```

Then rerun the installer.

## Privacy And Safety

- `wx-mcp` reads local WeChat databases on the same Windows user account.
- Extracted database keys are stored in the local user config file.
- Do not share `config.json`, cache files, or plaintext snapshots.
- Do not upload `C:\Users\<you>\.wx-mcp\cache` or `C:\Users\<you>\.config\wxcli`
  to issue trackers or chat systems.

## Uninstall

Remove the install directory:

```powershell
Remove-Item -LiteralPath D:\wx-mcp -Recurse -Force
```

Optional user data cleanup:

```powershell
Remove-Item -LiteralPath "$env:USERPROFILE\.config\wxcli" -Recurse -Force
Remove-Item -LiteralPath "$env:USERPROFILE\.wx-mcp" -Recurse -Force
```

Only remove user data if you no longer need cached indexes or key config.
