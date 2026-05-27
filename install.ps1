param(
  [switch]$All,
  [switch]$Yes,
  [switch]$Json,
  [switch]$DryRun,
  [switch]$Update,
  [switch]$Uninstall,
  [switch]$PurgeState,
  [switch]$ClearState,
  [switch]$Refresh,
  [switch]$BackgroundRefresh,
  [switch]$Doctor,
  [switch]$Mcp,
  [switch]$NoMcp,
  [string]$McpClient = "none",
  [string]$InstallDir = $(if (-not [string]::IsNullOrWhiteSpace($env:WECHAT_CLI_INSTALL_DIR)) { $env:WECHAT_CLI_INSTALL_DIR } else { $env:WX_MCP_INSTALL_DIR }),
  [string]$BinDir = $(if (-not [string]::IsNullOrWhiteSpace($env:WECHAT_CLI_BIN_DIR)) { $env:WECHAT_CLI_BIN_DIR } else { $env:WX_MCP_BIN_DIR })
)

$ErrorActionPreference = "Stop"

$AppName = "wechat-cli"
$LegacyAppName = "wx-mcp"
$McpName = "wechat-cli"
$LegacyMcpName = "wx-mcp"
$SourceDir = $PSScriptRoot
$local = $env:LOCALAPPDATA
if ([string]::IsNullOrWhiteSpace($local)) {
  $local = Join-Path $HOME "AppData\Local"
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
  $InstallDir = Join-Path $local $AppName
}
$LegacyInstallDir = Join-Path $local "wx-mcp"
if ([string]::IsNullOrWhiteSpace($BinDir)) {
  $windowsApps = Join-Path $local "Microsoft\WindowsApps"
  if (Test-Path $windowsApps) {
    $BinDir = $windowsApps
  } else {
    $BinDir = Join-Path $HOME ".local\bin"
  }
}
$ShimPath = Join-Path $BinDir "$AppName.cmd"
$LegacyShimPath = Join-Path $BinDir "$LegacyAppName.cmd"

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
$purgeState = [bool]($PurgeState -or $ClearState)
$registerMcp = $false
$mcpOptionSeen = [bool]($Mcp -or $NoMcp -or $PSBoundParameters.ContainsKey("McpClient"))

function Add-Action([string]$s) { $actions.Add($s) | Out-Null }
function Add-Warning([string]$s) { $warnings.Add($s) | Out-Null }
function Add-ErrorText([string]$s) { $errors.Add($s) | Out-Null }
function Have-Command([string]$name) { return $null -ne (Get-Command $name -ErrorAction SilentlyContinue) }
function Write-Log([string]$text) {
  New-Item -ItemType Directory -Force -Path $logDir | Out-Null
  Add-Content -Path $log -Value ("[{0}] {1}" -f ([DateTime]::UtcNow.ToString("o")), $text)
}
function Write-HumanResult($Out) {
  Write-Host ""
  if ($Out.errors.Count -gt 0) {
    Write-Host "$AppName $($Out.mode) failed"
  } else {
    Write-Host "$AppName $($Out.mode) complete"
  }
  Write-Host "  status: $($Out.status)"
  Write-Host "  install_dir: $($Out.install_dir)"
  Write-Host "  command: $($Out.command)"
  if (-not [string]::IsNullOrWhiteSpace($Out.blocked_by)) {
    Write-Host "  blocked_by: $($Out.blocked_by)"
  }
  if (-not [string]::IsNullOrWhiteSpace($Out.next_action)) {
    Write-Host "  next: $($Out.next_action)"
  }
  if ($Out.mcp_registered_clients.Count -gt 0) {
    Write-Host "  mcp_registered: $($Out.mcp_registered_clients -join ', ')"
  } elseif ($script:registerMcp -and $McpClient -ne "none") {
    Write-Host "  mcp_registered: no supported client command found"
  }
  if ($Out.refresh_ran) {
    Write-Host "  metadata_cache: complete"
  }
  if ($Out.warnings.Count -gt 0) {
    Write-Host "  warnings:"
    foreach ($warning in $Out.warnings) {
      Write-Host "    - $warning"
    }
  }
  if ($Out.errors.Count -gt 0) {
    Write-Host "  errors:"
    foreach ($err in $Out.errors) {
      Write-Host "    - $err"
    }
  }
  if ($Out.errors.Count -eq 0 -and -not $DryRun -and $Out.mode -in @("install", "update")) {
    Write-Host ""
    Write-Host "Next: run $AppName sessions to verify end-to-end access."
  }
}
function Finish {
  param([int]$Code = 0)
  $out = [ordered]@{
    name = $AppName
    platform = "windows"
    mode = $script:mode
    status = $script:status
    blocked_by = $script:blockedBy
    next_action = $script:nextAction
    install_dir = $InstallDir
    bin_dir = $BinDir
    command = $ShimPath
    log = $log
    dry_run = [bool]$DryRun
    purge_state = [bool]$script:purgeState
    mcp_client = $McpClient
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
    Write-HumanResult $out
  }
  exit $Code
}

function Resolve-WechatCli {
  $bin = Join-Path $SourceDir "$AppName.exe"
  if (Test-Path $bin) {
    Add-Action "copy $AppName.exe from source directory"
    return @{ Mode = "copy"; Path = $bin }
  }
  $legacyBin = Join-Path $SourceDir "$LegacyAppName.exe"
  if (Test-Path $legacyBin) {
    Add-Action "copy legacy $LegacyAppName.exe from source directory"
    return @{ Mode = "copy"; Path = $legacyBin }
  }
  if ((Test-Path (Join-Path $SourceDir "cmd\wechat-cli\main.go")) -and (Have-Command "go")) {
    Add-Action "build $AppName.exe from source"
    return @{ Mode = "build"; Path = $SourceDir }
  }
  throw "$AppName.exe not found and Go is not available to build it"
}

function Resolve-WcdbDll {
  $candidates = @()
  if (-not [string]::IsNullOrWhiteSpace($env:WECHAT_CLI_WCDB_LIB)) { $candidates += $env:WECHAT_CLI_WCDB_LIB }
  if (-not [string]::IsNullOrWhiteSpace($env:WECHAT_CLI_WCDB_DYLIB)) { $candidates += $env:WECHAT_CLI_WCDB_DYLIB }
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
  throw "WCDB DLL not found; put libWCDB.dll or WCDB.dll beside install.ps1, under .\lib, under ~/.config/wxcli/lib, or set WECHAT_CLI_WCDB_LIB"
}

function Confirm-Or-Die {
  if ($DryRun) { return }
  if ($Yes) { return }
  if ($Json) {
    throw "non-interactive install/update/uninstall requires -Yes"
  }
  $answer = Read-Host "Proceed with $AppName $mode into $InstallDir? [y/N]"
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
  if (-not $script:registerMcp -or $NoMcp -or $McpClient -eq "none") { return }
  $exe = Join-Path $InstallDir "$AppName.exe"
  $found = $false
  if (($McpClient -eq "auto" -or $McpClient -eq "codex") -and (Have-Command "codex")) {
    Add-Action "register Codex MCP server $McpName at $exe serve-mcp"
    if ($DryRun) {
      $found = $true
    } else {
    try {
      & codex mcp remove $McpName *> $null
      & codex mcp remove $LegacyMcpName *> $null
      & codex mcp add $McpName -- $exe serve-mcp *> $null
      $registered.Add("codex") | Out-Null
      $found = $true
    } catch {
      Add-Warning "Codex MCP registration failed: $($_.Exception.Message)"
    }
    }
  }
  if (($McpClient -eq "auto" -or $McpClient -eq "claude") -and (Have-Command "claude")) {
    Add-Action "register Claude MCP server $McpName at $exe serve-mcp"
    if ($DryRun) {
      $found = $true
    } else {
    try {
      & claude mcp remove -s user $McpName *> $null
      & claude mcp remove -s user $LegacyMcpName *> $null
      & claude mcp add -s user $McpName $exe serve-mcp *> $null
      $registered.Add("claude") | Out-Null
      $found = $true
    } catch {
      Add-Warning "Claude MCP registration failed: $($_.Exception.Message)"
    }
    }
  }
  if (-not $found -and $McpClient -eq "auto") {
    Add-Warning "no supported MCP client command found; skipped registration"
  }
}

function Remove-McpEntries {
  if ($NoMcp -or $McpClient -eq "none") { return }
  if (($McpClient -eq "auto" -or $McpClient -eq "codex") -and (Have-Command "codex")) {
    Add-Action "remove Codex MCP server $McpName and legacy $LegacyMcpName"
    if (-not $DryRun) {
      try {
        & codex mcp remove $McpName *> $null
        & codex mcp remove $LegacyMcpName *> $null
      } catch {
        Add-Warning "Codex MCP removal failed: $($_.Exception.Message)"
      }
    }
  }
  if (($McpClient -eq "auto" -or $McpClient -eq "claude") -and (Have-Command "claude")) {
    Add-Action "remove Claude MCP server $McpName and legacy $LegacyMcpName"
    if (-not $DryRun) {
      try {
        & claude mcp remove -s user $McpName *> $null
        & claude mcp remove -s user $LegacyMcpName *> $null
      } catch {
        Add-Warning "Claude MCP removal failed: $($_.Exception.Message)"
      }
    }
  }
}

function Resolve-Components {
  $wx = Resolve-WechatCli
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
    Add-Action "would install $AppName.exe into $InstallDir"
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
      Write-Log "go build -o $InstallDir\$AppName.exe ./cmd/wechat-cli"
      & go build -o (Join-Path $InstallDir "$AppName.exe") ./cmd/wechat-cli 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
      if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    } finally {
      Pop-Location
    }
  } else {
    Copy-Item -LiteralPath $wx.Path -Destination (Join-Path $InstallDir "$AppName.exe") -Force
  }

  $dll = $components.Dll
  $dllName = Split-Path $dll -Leaf
  Copy-Item -LiteralPath $dll -Destination (Join-Path $InstallDir $dllName) -Force
  if ($dllName -ne "libWCDB.dll") {
    Copy-Item -LiteralPath $dll -Destination (Join-Path $InstallDir "libWCDB.dll") -Force
  }
  Copy-InstallDocs
}

function Path-ContainsBinDir {
  $pathValue = [Environment]::GetEnvironmentVariable("Path", "Process")
  if ([string]::IsNullOrWhiteSpace($pathValue)) { return $false }
  foreach ($entry in $pathValue -split ";") {
    if ([string]::IsNullOrWhiteSpace($entry)) { continue }
    try {
      if ([IO.Path]::GetFullPath($entry.TrimEnd("\")).TrimEnd("\") -ieq [IO.Path]::GetFullPath($BinDir).TrimEnd("\")) {
        return $true
      }
    } catch {
      if ($entry.TrimEnd("\") -ieq $BinDir.TrimEnd("\")) { return $true }
    }
  }
  return $false
}

function Install-CliShim {
  Add-Action "create CLI command shim $ShimPath -> $InstallDir\$AppName.exe"
  if ($DryRun) { return }
  New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
  if (Test-Path $ShimPath) {
    $existing = Get-Item -LiteralPath $ShimPath
    $content = ""
    if (-not $existing.PSIsContainer) {
      $content = Get-Content -LiteralPath $ShimPath -Raw -ErrorAction SilentlyContinue
    }
    if ($content -notmatch [regex]::Escape($InstallDir) -and $content -notmatch "wechat-cli\\wechat-cli\.exe") {
      Add-Warning "not replacing existing command at $ShimPath; run $InstallDir\$AppName.exe directly or remove that file"
      return
    }
  }
  $exe = Join-Path $InstallDir "$AppName.exe"
  $cmd = "@echo off`r`n`"$exe`" %*`r`n"
  Set-Content -LiteralPath $ShimPath -Value $cmd -Encoding ASCII
  if (-not (Path-ContainsBinDir)) {
    Add-Warning "$BinDir is not in PATH for this process; open a new shell or run $ShimPath"
  }
}

function Run-CacheRefresh {
  if (-not ($All -or $Refresh)) { return }
  $exe = Join-Path $InstallDir "$AppName.exe"
  if ($BackgroundRefresh) {
    Add-Action "start metadata cache refresh in background"
    if ($DryRun) { return }
    & $exe cache refresh --background 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "metadata cache refresh background start failed with exit code $LASTEXITCODE" }
    $script:status = "warming_cache"
    $script:nextAction = "$AppName is installed; metadata cache refresh is warming in the background."
    $script:refreshRan = $true
    return
  }

  Add-Action "run metadata cache refresh in foreground to verify Windows key setup"
  if ($DryRun) { return }
  & $exe cache refresh --force 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "metadata cache refresh failed with exit code $LASTEXITCODE" }
  $script:status = "ready"
  $script:nextAction = "$AppName is installed and the metadata cache refresh completed."
  $script:refreshRan = $true
}

function Clear-LegacyMessageCache {
  foreach ($cacheRoot in @((Join-Path $HOME ".wechat-cli\cache"), (Join-Path $HOME ".wx-mcp\cache"))) {
    if (-not (Test-Path $cacheRoot)) { continue }
    Add-Action "drop existing cache indexes and non-metadata raw snapshots under $cacheRoot"
    if ($DryRun) { continue }
    Get-ChildItem -LiteralPath $cacheRoot -Directory -ErrorAction SilentlyContinue | ForEach-Object {
      foreach ($name in @("index.sqlite", "index.sqlite-wal", "index.sqlite-shm")) {
        $p = Join-Path $_.FullName $name
        if (Test-Path $p) { Remove-Item -LiteralPath $p -Force }
      }
      $raw = Join-Path $_.FullName "raw"
      if (Test-Path $raw) {
        Get-ChildItem -LiteralPath $raw -Force -ErrorAction SilentlyContinue | ForEach-Object {
          if ($_.Name -notin @("contact", "session")) {
            Remove-Item -LiteralPath $_.FullName -Recurse -Force
          }
        }
      }
    }
  }
}

function Add-PurgeStateActions {
  Add-Action ("remove wxkey config file {0}" -f (Join-Path $HOME ".config\wxcli\config.json"))
  Add-Action ("remove empty wxkey config dir {0} when no lib remains" -f (Join-Path $HOME ".config\wxcli"))
  Add-Action ("remove wechat-cli state dir {0}" -f (Join-Path $HOME ".wechat-cli"))
  Add-Action ("remove legacy wx-mcp state dir {0}" -f (Join-Path $HOME ".wx-mcp"))
  Add-Action "remove wechat-cli install logs $logDir"
  Add-Action ("remove legacy wx-mcp logs {0}" -f (Join-Path $HOME "Library\Logs\wx-mcp"))
}

function Invoke-PurgeState {
  $paths = @(
    (Join-Path $HOME ".config\wxcli\config.json"),
    (Join-Path $HOME ".wechat-cli"),
    (Join-Path $HOME ".wx-mcp"),
    $logDir
  )
  foreach ($path in $paths) {
    if (Test-Path $path) {
      Remove-Item -LiteralPath $path -Recurse -Force
    }
  }
  $wxcliDir = Join-Path $HOME ".config\wxcli"
  if (Test-Path $wxcliDir) {
    try {
      Remove-Item -LiteralPath $wxcliDir -Force -ErrorAction Stop
    } catch {
      # Keep ~/.config/wxcli/lib if the user installed a shared WCDB DLL there.
    }
  }
  $legacyLog = Join-Path $HOME "Library\Logs\wx-mcp"
  if (Test-Path $legacyLog) {
    Remove-Item -LiteralPath $legacyLog -Recurse -Force
  }
}

function Clear-State {
  Add-PurgeStateActions
  if (-not $DryRun) {
    Invoke-PurgeState
  }
  $script:status = "state_cleared"
}

function Stop-InstalledProcesses {
  $roots = @($InstallDir, $LegacyInstallDir) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
  if ($roots.Count -eq 0) { return }
  $ownPid = $PID
  $procs = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
    $_.ProcessId -ne $ownPid -and
    -not [string]::IsNullOrWhiteSpace($_.ExecutablePath) -and
    ($roots | Where-Object { $_.ExecutablePath.StartsWith($_, [System.StringComparison]::OrdinalIgnoreCase) }).Count -gt 0
  }
  foreach ($proc in $procs) {
    try {
      Stop-Process -Id $proc.ProcessId -Force -ErrorAction Stop
    } catch {
      Add-Warning ("failed to stop process {0}: {1}" -f $proc.ProcessId, $_.Exception.Message)
    }
  }
}

function Uninstall-WxMcp {
  Add-Action "stop running wechat-cli/wx-mcp processes from install dirs"
  Add-Action "remove CLI command shim $ShimPath if managed by wechat-cli"
  Add-Action "remove legacy CLI command shim $LegacyShimPath if managed by wechat-cli"
  Add-Action "remove install directory $InstallDir"
  Add-Action "remove legacy install directory $LegacyInstallDir"
  Remove-McpEntries
  if ($script:purgeState) {
    Add-PurgeStateActions
  }
  if (-not $DryRun) {
    Stop-InstalledProcesses
  }
  if (-not $DryRun -and (Test-Path $InstallDir)) {
    Remove-Item -LiteralPath $InstallDir -Recurse -Force
  }
  if (-not $DryRun) {
    foreach ($path in @($ShimPath, $LegacyShimPath)) {
      if (-not (Test-Path -LiteralPath $path)) { continue }
      $content = Get-Content -LiteralPath $path -Raw -ErrorAction SilentlyContinue
      if ($content -match [regex]::Escape($InstallDir) -or $content -match [regex]::Escape($LegacyInstallDir) -or $content -match "wechat-cli\\wechat-cli\.exe" -or $content -match "wx-mcp\\wx-mcp\.exe") {
        Remove-Item -LiteralPath $path -Force
      }
    }
  }
  if (-not $DryRun -and (Test-Path $LegacyInstallDir)) {
    Remove-Item -LiteralPath $LegacyInstallDir -Recurse -Force
  }
  if (-not $DryRun -and $script:purgeState) {
    Invoke-PurgeState
  }
  $script:status = "removed"
}

function Run-Doctor {
  $checks.Add("os=Windows") | Out-Null
  $checks.Add("install_dir_exists=$(Test-Path $InstallDir)") | Out-Null
  $checks.Add("installed_wechat_cli=$(Test-Path (Join-Path $InstallDir "$AppName.exe"))") | Out-Null
  $checks.Add("installed_libWCDB=$(Test-Path (Join-Path $InstallDir 'libWCDB.dll'))") | Out-Null
  $checks.Add("shim_exists=$(Test-Path $ShimPath)") | Out-Null
  $checks.Add("wechat_cli_on_path=$(Have-Command $AppName)") | Out-Null
  $checks.Add("go=$(Have-Command 'go')") | Out-Null
  $checks.Add("codex=$(Have-Command 'codex')") | Out-Null
  $checks.Add("claude=$(Have-Command 'claude')") | Out-Null
}

try {
  if ($Doctor) { $mode = "doctor" }
  elseif ($ClearState) { $mode = "clear-state" }
  elseif ($Uninstall) { $mode = "uninstall" }
  elseif ($Update) { $mode = "update" }

  if ($Mcp) {
    $registerMcp = $true
    if ($McpClient -eq "none") { $McpClient = "auto" }
  } elseif ($McpClient -ne "none") {
    $registerMcp = $true
  }
  if ($NoMcp) {
    $registerMcp = $false
    $McpClient = "none"
  }
  if ($Uninstall -and -not $mcpOptionSeen) {
    $registerMcp = $true
    $McpClient = "auto"
  }
  if ($McpClient -notin @("auto", "claude", "codex", "none")) {
    throw "-McpClient must be auto, claude, codex, or none"
  }

  if ($PurgeState -and -not ($Uninstall -or $ClearState)) {
    throw "-PurgeState is only valid with -Uninstall; use -ClearState to remove state without uninstalling"
  }

  if ($Doctor) {
    Run-Doctor
    Finish 0
  }

  Confirm-Or-Die

  if ($ClearState) {
    Clear-State
    Finish 0
  }

  if ($Uninstall) {
    Uninstall-WxMcp
    Finish 0
  }

  if ($Update) {
    Update-Source
  }

  Install-Components
  Install-CliShim
  Clear-LegacyMessageCache
  Register-Mcp
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
    $nextAction = "Verify WECHAT_CLI_DB_ROOT belongs to the logged-in Windows WeChat account; if multiple WeChat processes exist, set WECHAT_CLI_WECHAT_PID and rerun install.ps1 -All -Yes -Json."
  } elseif ($message -match "no account directory with db_storage|WECHAT_CLI_DB_ROOT|WX_MCP_DB_ROOT") {
    $blockedBy = "db_root_not_found"
    $nextAction = "Set WECHAT_CLI_DB_ROOT to the WeChat account directory that directly contains db_storage, then rerun install.ps1 -All -Yes -Json."
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
