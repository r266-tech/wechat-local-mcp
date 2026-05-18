param(
  [switch]$All,
  [switch]$Yes,
  [switch]$Json,
  [switch]$DryRun,
  [switch]$Update,
  [switch]$Uninstall,
  [switch]$Refresh,
  [switch]$BackgroundRefresh,
  [switch]$Doctor,
  [switch]$NoMcp,
  [string]$McpClient = "auto",
  [string]$InstallDir = $env:WX_MCP_INSTALL_DIR
)

$ErrorActionPreference = "Stop"

$SourceDir = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
  $local = $env:LOCALAPPDATA
  if ([string]::IsNullOrWhiteSpace($local)) {
    $local = Join-Path $HOME "AppData\Local"
  }
  $InstallDir = Join-Path $local "wx-mcp"
}

$actions = New-Object System.Collections.Generic.List[string]
$warnings = New-Object System.Collections.Generic.List[string]
$errors = New-Object System.Collections.Generic.List[string]
$checks = New-Object System.Collections.Generic.List[string]
$registered = New-Object System.Collections.Generic.List[string]
$mode = "install"
$status = "ok"
$blockedBy = ""
$nextAction = ""
$logDir = Join-Path $InstallDir "logs"
$log = Join-Path $logDir "install.log"
$refreshRan = $false

function Add-Action([string]$s) { $actions.Add($s) | Out-Null }
function Add-Warning([string]$s) { $warnings.Add($s) | Out-Null }
function Add-ErrorText([string]$s) { $errors.Add($s) | Out-Null }
function Have-Command([string]$name) { return $null -ne (Get-Command $name -ErrorAction SilentlyContinue) }
function Write-Log([string]$text) {
  New-Item -ItemType Directory -Force -Path $logDir | Out-Null
  Add-Content -Path $log -Value ("[{0}] {1}" -f ([DateTime]::UtcNow.ToString("o")), $text)
}
function Finish {
  param([int]$Code = 0)
  $out = [ordered]@{
    name = "wx-mcp"
    platform = "windows"
    mode = $script:mode
    status = $script:status
    blocked_by = $script:blockedBy
    next_action = $script:nextAction
    install_dir = $InstallDir
    log = $log
    dry_run = [bool]$DryRun
    mcp_registered = ($registered.Count -gt 0)
    mcp_registered_clients = @($registered)
    refresh_ran = [bool]$script:refreshRan
    actions = @($actions)
    warnings = @($warnings)
    errors = @($errors)
    checks = @($checks)
  }
  if ($Json) {
    $out | ConvertTo-Json -Depth 8 -Compress
  } else {
    $out | ConvertTo-Json -Depth 8
  }
  exit $Code
}

function Resolve-WxMcp {
  $bin = Join-Path $SourceDir "wx-mcp.exe"
  if (Test-Path $bin) {
    Add-Action "copy wx-mcp.exe from source directory"
    return @{ Mode = "copy"; Path = $bin }
  }
  if ((Test-Path (Join-Path $SourceDir "cmd\wx-mcp\main.go")) -and (Have-Command "go")) {
    Add-Action "build wx-mcp.exe from source"
    return @{ Mode = "build"; Path = $SourceDir }
  }
  throw "wx-mcp.exe not found and Go is not available to build it"
}

function Resolve-WcdbDll {
  $candidates = @()
  if (-not [string]::IsNullOrWhiteSpace($env:WX_MCP_WCDB_LIB)) { $candidates += $env:WX_MCP_WCDB_LIB }
  if (-not [string]::IsNullOrWhiteSpace($env:WX_MCP_WCDB_DYLIB)) { $candidates += $env:WX_MCP_WCDB_DYLIB }
  $candidates += (Join-Path $SourceDir "libWCDB.dll")
  $candidates += (Join-Path $SourceDir "WCDB.dll")
  $candidates += (Join-Path $SourceDir "lib\libWCDB.dll")
  $candidates += (Join-Path $SourceDir "lib\WCDB.dll")
  $candidates += (Join-Path $HOME ".config\wxcli\lib\libWCDB.dll")
  $candidates += (Join-Path $HOME ".config\wxcli\lib\WCDB.dll")
  foreach ($cand in $candidates) {
    if (-not [string]::IsNullOrWhiteSpace($cand) -and (Test-Path $cand)) {
      Add-Action "copy WCDB DLL from $cand"
      return $cand
    }
  }
  throw "WCDB DLL not found; put libWCDB.dll or WCDB.dll beside install.ps1, under .\lib, under ~/.config/wxcli/lib, or set WX_MCP_WCDB_LIB"
}

function Confirm-Or-Die {
  if ($DryRun) { return }
  if ($Yes) { return }
  if ($Json) {
    throw "non-interactive install/update/uninstall requires -Yes"
  }
  $answer = Read-Host "Proceed with wx-mcp $mode into $InstallDir? [y/N]"
  if ($answer -notin @("y", "Y", "yes", "YES")) {
    $script:status = "cancelled"
    Finish 1
  }
}

function Copy-InstallDocs {
  $docs = @(
    "install.ps1",
    "README.md",
    "AGENTS.md",
    "mcp-server.json",
    "LICENSE",
    "SECURITY.md",
    "THIRD_PARTY_NOTICES.md"
  )
  foreach ($doc in $docs) {
    $src = Join-Path $SourceDir $doc
    if (Test-Path $src) {
      Copy-Item -LiteralPath $src -Destination (Join-Path $InstallDir $doc) -Force
    }
  }
  $guide = Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md"
  if (Test-Path $guide) {
    New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir "docs") | Out-Null
    Copy-Item -LiteralPath $guide -Destination (Join-Path $InstallDir "docs\WINDOWS_USER_GUIDE.md") -Force
  }
  Add-Action "copy installer docs and manifest"
}

function Register-Mcp {
  if ($NoMcp -or $McpClient -eq "none") { return }
  $exe = Join-Path $InstallDir "wx-mcp.exe"
  $found = $false
  if (($McpClient -eq "auto" -or $McpClient -eq "codex") -and (Have-Command "codex")) {
    Add-Action "register Codex MCP server wx-mcp"
    try {
      & codex mcp remove wx-mcp *> $null
      & codex mcp add wx-mcp -- $exe *> $null
      $registered.Add("codex") | Out-Null
      $found = $true
    } catch {
      Add-Warning "Codex MCP registration failed: $($_.Exception.Message)"
    }
  }
  if (($McpClient -eq "auto" -or $McpClient -eq "claude") -and (Have-Command "claude")) {
    Add-Action "register Claude MCP server wx-mcp"
    try {
      & claude mcp remove -s user wx-mcp *> $null
      & claude mcp add -s user wx-mcp $exe *> $null
      $registered.Add("claude") | Out-Null
      $found = $true
    } catch {
      Add-Warning "Claude MCP registration failed: $($_.Exception.Message)"
    }
  }
  if (-not $found -and $McpClient -eq "auto") {
    Add-Warning "no supported MCP client command found; skipped registration"
  }
}

function Resolve-Components {
  $wx = Resolve-WxMcp
  $dll = Resolve-WcdbDll
  return @{ Wx = $wx; Dll = $dll }
}

function Update-Source {
  if (Test-Path (Join-Path $SourceDir ".git")) {
    if (-not (Have-Command "git")) {
      throw "git checkout update requested, but git is not available"
    }
    Add-Action "git pull --ff-only in $SourceDir"
    if (-not $DryRun) {
      New-Item -ItemType Directory -Force -Path $logDir | Out-Null
      Push-Location $SourceDir
      try {
        & git pull --ff-only 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "git pull --ff-only failed with exit code $LASTEXITCODE" }
      } finally {
        Pop-Location
      }
    }
  } else {
    Add-Warning "source directory is not a git checkout; -Update reinstalls the files currently on disk. For release zip installs, download the newest zip first."
  }
}

function Install-Components {
  $components = Resolve-Components
  if ($DryRun) {
    Add-Action "would install wx-mcp.exe into $InstallDir"
    Add-Action "would copy WCDB DLL into $InstallDir"
    Add-Action "would copy installer docs and manifest"
    return
  }

  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  New-Item -ItemType Directory -Force -Path $logDir | Out-Null

  $wx = $components.Wx
  if ($wx.Mode -eq "build") {
    Push-Location $wx.Path
    try {
      Write-Log "go build -o $InstallDir\wx-mcp.exe ./cmd/wx-mcp"
      & go build -o (Join-Path $InstallDir "wx-mcp.exe") ./cmd/wx-mcp 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
      if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    } finally {
      Pop-Location
    }
  } else {
    Copy-Item -LiteralPath $wx.Path -Destination (Join-Path $InstallDir "wx-mcp.exe") -Force
  }

  $dll = $components.Dll
  $dllName = Split-Path $dll -Leaf
  Copy-Item -LiteralPath $dll -Destination (Join-Path $InstallDir $dllName) -Force
  if ($dllName -ne "libWCDB.dll") {
    Copy-Item -LiteralPath $dll -Destination (Join-Path $InstallDir "libWCDB.dll") -Force
  }
  Copy-InstallDocs
}

function Run-CacheRefresh {
  if (-not ($All -or $Refresh)) { return }
  $exe = Join-Path $InstallDir "wx-mcp.exe"
  if ($BackgroundRefresh) {
    Add-Action "start cache refresh in background"
    if ($DryRun) { return }
    & $exe cache refresh --background 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "cache refresh background start failed with exit code $LASTEXITCODE" }
    $script:status = "warming_cache"
    $script:nextAction = "wx-mcp is installed; cache refresh is warming in the background."
    $script:refreshRan = $true
    return
  }

  Add-Action "run cache refresh in foreground to verify Windows key setup"
  if ($DryRun) { return }
  & $exe cache refresh --force 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "cache refresh failed with exit code $LASTEXITCODE" }
  $script:status = "ready"
  $script:nextAction = "wx-mcp is installed and the cache refresh completed."
  $script:refreshRan = $true
}

function Uninstall-WxMcp {
  Add-Action "remove install directory $InstallDir"
  if (-not $DryRun -and (Test-Path $InstallDir)) {
    Remove-Item -LiteralPath $InstallDir -Recurse -Force
  }
  $script:status = "removed"
}

function Run-Doctor {
  $checks.Add("os=Windows") | Out-Null
  $checks.Add("install_dir_exists=$(Test-Path $InstallDir)") | Out-Null
  $checks.Add("installed_wx_mcp=$(Test-Path (Join-Path $InstallDir 'wx-mcp.exe'))") | Out-Null
  $checks.Add("installed_libWCDB=$(Test-Path (Join-Path $InstallDir 'libWCDB.dll'))") | Out-Null
  $checks.Add("go=$(Have-Command 'go')") | Out-Null
  $checks.Add("codex=$(Have-Command 'codex')") | Out-Null
  $checks.Add("claude=$(Have-Command 'claude')") | Out-Null
}

try {
  if ($Doctor) { $mode = "doctor" }
  elseif ($Uninstall) { $mode = "uninstall" }
  elseif ($Update) { $mode = "update" }

  if ($Doctor) {
    Run-Doctor
    Finish 0
  }

  Confirm-Or-Die

  if ($Uninstall) {
    Uninstall-WxMcp
    Finish 0
  }

  if ($Update) {
    Update-Source
  }

  Install-Components
  if (-not $DryRun) {
    Register-Mcp
  }
  Run-CacheRefresh
  if ($DryRun) {
    $status = "dry_run"
    $nextAction = "Dry run only; rerun install.ps1 -All -Yes -Json to install."
  } elseif (-not ($All -or $Refresh)) {
    $status = "ready"
  } else {
    # Run-CacheRefresh already set status and next_action.
  }
  Finish 0
} catch {
  $status = "blocked"
  if ([string]::IsNullOrWhiteSpace($blockedBy)) { $blockedBy = "install_failed" }
  $message = $_.Exception.Message
  if ($message -match "no running Weixin.exe/WeChat.exe") {
    $blockedBy = "wechat_not_running"
    $nextAction = "Start Windows WeChat, finish login, open one chat, then rerun install.ps1 -All -Yes -Json."
  } elseif ($message -match "no usable Windows WeChat raw keys") {
    $blockedBy = "key_scan_failed"
    $nextAction = "Verify WX_MCP_DB_ROOT belongs to the logged-in Windows WeChat account; if multiple WeChat processes exist, set WX_MCP_WECHAT_PID and rerun install.ps1 -All -Yes -Json."
  } elseif ($message -match "no account directory with db_storage|WX_MCP_DB_ROOT") {
    $blockedBy = "db_root_not_found"
    $nextAction = "Set WX_MCP_DB_ROOT to the WeChat account directory that directly contains db_storage, then rerun install.ps1 -All -Yes -Json."
  } elseif ($message -match "WCDB DLL") {
    $blockedBy = "wcdb_dll_missing"
    $nextAction = "Use the Windows release zip with libWCDB.dll included, or put libWCDB.dll beside install.ps1, then rerun install.ps1 -All -Yes -Json."
  } elseif ([string]::IsNullOrWhiteSpace($nextAction)) {
    $nextAction = "Fix the reported error and rerun install.ps1 -All -Yes -Json."
  }
  Add-ErrorText $_.Exception.Message
  Write-Log $_.Exception.Message
  Finish 1
}
