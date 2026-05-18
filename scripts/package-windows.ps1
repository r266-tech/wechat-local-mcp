param(
  [string]$Version = "1.0.0",
  [string]$WcdbLib = $env:WX_MCP_WCDB_LIB
)

$ErrorActionPreference = "Stop"
$SourceDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $SourceDir

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
  throw "WCDB DLL missing. Set WX_MCP_WCDB_LIB or place libWCDB.dll/WCDB.dll under .\lib."
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is required to build wx-mcp.exe"
}

$distRoot = Join-Path $SourceDir "dist"
$dist = Join-Path $distRoot "wx-mcp-v$Version-windows-amd64"
if (Test-Path $dist) { Remove-Item -LiteralPath $dist -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dist | Out-Null

& go build -trimpath -ldflags="-s -w" -o (Join-Path $dist "wx-mcp.exe") ./cmd/wx-mcp
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

Copy-Item -LiteralPath $WcdbLib -Destination (Join-Path $dist "libWCDB.dll") -Force
Copy-Item README.md, LICENSE, SECURITY.md, THIRD_PARTY_NOTICES.md, AGENTS.md, mcp-server.json, install.ps1 -Destination $dist -Force
if (Test-Path (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $dist "docs") | Out-Null
  Copy-Item -LiteralPath (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md") -Destination (Join-Path $dist "docs\WINDOWS_USER_GUIDE.md") -Force
}

$zip = Join-Path $distRoot "wx-mcp-v$Version-windows-amd64.zip"
$latest = Join-Path $distRoot "wx-mcp-latest-windows-amd64.zip"
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
