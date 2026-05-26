param(
  [string]$Version = "1.0.0",
  [string]$WcdbLib = $(if (-not [string]::IsNullOrWhiteSpace($env:WECHAT_CLI_WCDB_LIB)) { $env:WECHAT_CLI_WCDB_LIB } else { $env:WX_MCP_WCDB_LIB })
)

$ErrorActionPreference = "Stop"
$SourceDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $SourceDir

$RequiredWcdbExports = @(
  "sqlite3_open_v2",
  "sqlite3_close_v2",
  "sqlite3_key_v2",
  "sqlite3_exec",
  "sqlite3_prepare_v2",
  "sqlite3_step",
  "sqlite3_finalize",
  "sqlite3_column_count",
  "sqlite3_column_name",
  "sqlite3_column_text",
  "sqlite3_column_int64",
  "sqlite3_column_bytes",
  "sqlite3_column_blob",
  "sqlite3_column_type",
  "sqlite3_bind_text",
  "sqlite3_bind_blob",
  "sqlite3_bind_int64",
  "sqlite3_bind_null",
  "sqlite3_reset",
  "sqlite3_clear_bindings",
  "sqlite3_errmsg",
  "sqlite3_backup_init",
  "sqlite3_backup_step",
  "sqlite3_backup_finish"
)

function Assert-WcdbDllExports {
  param([string]$Path)

  Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public static class WxMcpNative {
  [DllImport("kernel32", SetLastError=true, CharSet=CharSet.Unicode)]
  public static extern IntPtr LoadLibrary(string lpFileName);

  [DllImport("kernel32", SetLastError=true, CharSet=CharSet.Ansi)]
  public static extern IntPtr GetProcAddress(IntPtr hModule, string procName);
}
"@ -ErrorAction SilentlyContinue

  $handle = [WxMcpNative]::LoadLibrary((Resolve-Path -LiteralPath $Path).Path)
  if ($handle -eq [IntPtr]::Zero) {
    $lastError = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
    throw "WCDB DLL failed to load: $Path (Win32 error $lastError)"
  }

  $missing = @()
  foreach ($name in $RequiredWcdbExports) {
    if ([WxMcpNative]::GetProcAddress($handle, $name) -eq [IntPtr]::Zero) {
      $missing += $name
    }
  }
  if ($missing.Count -gt 0) {
    throw "WCDB DLL missing required exports: $($missing -join ', ')"
  }
}

if ([string]::IsNullOrWhiteSpace($WcdbLib)) {
  foreach ($cand in @(
    (Join-Path $SourceDir "lib\libWCDB.dll"),
    (Join-Path $SourceDir "lib\WCDB.dll"),
    (Join-Path $SourceDir "libWCDB.dll"),
    (Join-Path $SourceDir "WCDB.dll")
  )) {
    if (Test-Path $cand) {
      $WcdbLib = $cand
      break
    }
  }
}
if ([string]::IsNullOrWhiteSpace($WcdbLib) -or -not (Test-Path $WcdbLib)) {
  throw "WCDB DLL missing. Set WECHAT_CLI_WCDB_LIB or place libWCDB.dll/WCDB.dll under .\lib."
}
Assert-WcdbDllExports $WcdbLib
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is required to build wechat-cli.exe"
}

$distRoot = Join-Path $SourceDir "dist"
$dist = Join-Path $distRoot "wechat-cli-v$Version-windows-amd64"
if (Test-Path $dist) { Remove-Item -LiteralPath $dist -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dist | Out-Null

& go build -trimpath -ldflags="-s -w" -o (Join-Path $dist "wechat-cli.exe") ./cmd/wx-mcp
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

Copy-Item -LiteralPath $WcdbLib -Destination (Join-Path $dist "libWCDB.dll") -Force
Copy-Item README.md, llms.txt, LICENSE, SECURITY.md, THIRD_PARTY_NOTICES.md, AGENTS.md, mcp-server.json, install.ps1 -Destination $dist -Force
New-Item -ItemType Directory -Force -Path (Join-Path $dist "scripts") | Out-Null
Copy-Item -LiteralPath (Join-Path $SourceDir "scripts\install-release.ps1") -Destination (Join-Path $dist "scripts\install-release.ps1") -Force
if (Test-Path (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $dist "docs") | Out-Null
  Copy-Item -LiteralPath (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md") -Destination (Join-Path $dist "docs\WINDOWS_USER_GUIDE.md") -Force
}

$zip = Join-Path $distRoot "wechat-cli-v$Version-windows-amd64.zip"
$latest = Join-Path $distRoot "wechat-cli-latest-windows-amd64.zip"
if (Test-Path $zip) { Remove-Item -LiteralPath $zip -Force }
if (Test-Path $latest) { Remove-Item -LiteralPath $latest -Force }
Compress-Archive -Path $dist -DestinationPath $zip -Force
Copy-Item -LiteralPath $zip -Destination $latest -Force
Get-FileHash $zip -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $(Split-Path $zip -Leaf)" } | Set-Content "$zip.sha256"
Get-FileHash $latest -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $(Split-Path $latest -Leaf)" } | Set-Content "$latest.sha256"

[ordered]@{
  zip = $zip
  latest = $latest
  sha256 = "$zip.sha256"
} | ConvertTo-Json -Depth 3
