param(
  [string]$WcdbVersion = "2.1.16",
  [string]$OutDir = (Join-Path (Resolve-Path (Join-Path $PSScriptRoot "..")).Path "lib"),
  [string]$WorkDir = (Join-Path ([System.IO.Path]::GetTempPath()) "wx-mcp-wcdb-build"),
  [switch]$KeepWorkDir
)

$ErrorActionPreference = "Stop"

function Find-WcdbDll {
  param([string]$BuildDir)

  $candidates = Get-ChildItem -LiteralPath $BuildDir -Recurse -File -Filter "WCDB.dll" |
    Where-Object { $_.FullName -match "\\Release\\" } |
    Sort-Object Length -Descending
  if (-not $candidates) {
    $candidates = Get-ChildItem -LiteralPath $BuildDir -Recurse -File -Filter "WCDB.dll" |
      Sort-Object Length -Descending
  }
  if (-not $candidates) {
    throw "WCDB.dll was not produced under $BuildDir"
  }
  return $candidates[0].FullName
}

if (-not (Get-Command cmake -ErrorAction SilentlyContinue)) {
  throw "CMake is required to build WCDB.dll"
}

$versionNoPrefix = $WcdbVersion.TrimStart("v")
$sourceUrl = "https://github.com/Tencent/wcdb/releases/download/v$versionNoPrefix/wcdb-$versionNoPrefix.zip"
$archive = Join-Path $WorkDir "wcdb-$versionNoPrefix.zip"
$extractRoot = Join-Path $WorkDir "src"
$buildDir = Join-Path $WorkDir "build"

if (Test-Path $WorkDir) {
  Remove-Item -LiteralPath $WorkDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $WorkDir, $extractRoot, $OutDir | Out-Null

try {
  Invoke-WebRequest -Uri $sourceUrl -OutFile $archive
  Expand-Archive -LiteralPath $archive -DestinationPath $extractRoot -Force

  $sourceDir = Join-Path $extractRoot "wcdb-$versionNoPrefix\src"
  if (-not (Test-Path $sourceDir)) {
    throw "WCDB source directory not found: $sourceDir"
  }

  $sqliteExportFlag = "/DSQLITE_API=__declspec(dllexport)"
  cmake -S $sourceDir -B $buildDir -G "Visual Studio 17 2022" -A x64 -DBUILD_SHARED_LIBS=ON "-DCMAKE_C_FLAGS=$sqliteExportFlag"
  if ($LASTEXITCODE -ne 0) { throw "cmake configure failed" }

  cmake --build $buildDir --config Release --target WCDB --parallel
  if ($LASTEXITCODE -ne 0) { throw "cmake build failed" }

  $dll = Find-WcdbDll -BuildDir $buildDir
  $dest = Join-Path $OutDir "libWCDB.dll"
  Copy-Item -LiteralPath $dll -Destination $dest -Force

  [ordered]@{
    wcdb_version = $versionNoPrefix
    source_url = $sourceUrl
    dll = $dest
    bytes = (Get-Item -LiteralPath $dest).Length
  } | ConvertTo-Json -Depth 3
} finally {
  if (-not $KeepWorkDir -and (Test-Path $WorkDir)) {
    Remove-Item -LiteralPath $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
