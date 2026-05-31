package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	releaseInstallShellURL      = "https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh"
	releaseInstallPowerShellURL = "https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1"
)

type updateOptions struct {
	DryRun       bool
	KeepDownload bool
	Repo         string
	Tag          string
	Asset        string
}

func runUpdateCLI(args []string, opts cliOptions) {
	updateOpts, err := parseUpdateArgs(args)
	if err != nil {
		exitCLIError(opts, 2, "invalid_argument", err.Error(), "update", "update")
	}

	data, err := runReleaseUpdate(updateOpts)
	if err != nil {
		exitCLIError(opts, 1, "update_failed", err.Error(), "update", "update")
	}
	writeCLISuccess("update", "update", data, opts)
}

func parseUpdateArgs(args []string) (updateOptions, error) {
	var opts updateOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.DryRun = true
		case arg == "--keep-download":
			opts.KeepDownload = true
		case arg == "--repo":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Repo = v
		case strings.HasPrefix(arg, "--repo="):
			opts.Repo = strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
		case arg == "--tag":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Tag = v
		case strings.HasPrefix(arg, "--tag="):
			opts.Tag = strings.TrimSpace(strings.TrimPrefix(arg, "--tag="))
		case arg == "--asset":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Asset = v
		case strings.HasPrefix(arg, "--asset="):
			opts.Asset = strings.TrimSpace(strings.TrimPrefix(arg, "--asset="))
		default:
			return opts, fmt.Errorf("unknown update argument %q", arg)
		}
	}
	if opts.Repo == "" && os.Getenv("WECHAT_CLI_REPO") == "" && os.Getenv("WX_MCP_REPO") == "" {
		opts.Repo = "https://github.com/r266-tech/wechat-cli"
	}
	return opts, nil
}

func updateArgValue(args []string, idx *int, name string) (string, error) {
	if *idx+1 >= len(args) {
		return "", fmt.Errorf("%s requires a value", name)
	}
	*idx = *idx + 1
	v := strings.TrimSpace(args[*idx])
	if v == "" {
		return "", fmt.Errorf("%s requires a non-empty value", name)
	}
	return v, nil
}

func runReleaseUpdate(opts updateOptions) (map[string]any, error) {
	switch runtime.GOOS {
	case "darwin":
		return runDarwinReleaseUpdate(opts)
	case "windows":
		return startWindowsReleaseUpdate(opts)
	default:
		return nil, fmt.Errorf("wechat-cli update supports macOS and Windows releases only, not %s", runtime.GOOS)
	}
}

func runDarwinReleaseUpdate(opts updateOptions) (map[string]any, error) {
	data := updateResultBase(opts)
	data["status"] = "running"
	data["background"] = false

	cmd := exec.Command("/bin/zsh", darwinUpdateCommandArgs(opts)...)
	cmd.Env = updateCommandEnv(os.Environ(), opts)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	attachUpdaterOutput(data, stdout.Bytes(), stderr.Bytes())
	if err != nil {
		return nil, fmt.Errorf("release updater failed: %w%s", err, updateFailureHint(stdout.String(), stderr.String()))
	}
	data["status"] = "completed"
	if opts.DryRun {
		data["next_action"] = "Dry run only; rerun wechat-cli update without --dry-run to apply changes."
	} else {
		data["next_action"] = "Run wechat-cli sessions to verify the updated install."
	}
	return data, nil
}

func darwinUpdateCommandArgs(opts updateOptions) []string {
	script := `set -euo pipefail
url="$1"
shift
curl -fsSL "$url" | env WECHAT_CLI_INSTALL_JSON=1 zsh -s -- "$@"`
	args := []string{"-c", script, "wechat-cli-update", releaseInstallShellURL}
	args = append(args, releaseInstallerArgs(opts)...)
	return args
}

func releaseInstallerArgs(opts updateOptions) []string {
	args := []string{"--update"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Tag != "" {
		args = append(args, "--tag", opts.Tag)
	}
	if opts.Asset != "" {
		args = append(args, "--asset", opts.Asset)
	}
	return args
}

func startWindowsReleaseUpdate(opts updateOptions) (map[string]any, error) {
	data := updateResultBase(opts)
	logPath, err := updateLogPath()
	if err != nil {
		return nil, err
	}
	scriptPath, err := writeWindowsUpdateScript()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
		"-ParentPid", strconv.Itoa(os.Getpid()),
		"-Url", releaseInstallPowerShellURL,
		"-LogPath", logPath,
	}
	if opts.DryRun {
		args = append(args, "-DryRun")
	}
	if opts.KeepDownload {
		args = append(args, "-KeepDownload")
	}
	if opts.Repo != "" {
		args = append(args, "-Repo", opts.Repo)
	}
	if opts.Tag != "" {
		args = append(args, "-Tag", opts.Tag)
	}
	if opts.Asset != "" {
		args = append(args, "-Asset", opts.Asset)
	}

	cmd, err := startPowerShell(args)
	if err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	data["status"] = "started"
	data["background"] = true
	data["pid"] = pid
	data["log"] = logPath
	data["script"] = scriptPath
	data["next_action"] = "Wait for the updater to finish, then run wechat-cli sessions. Agents should inspect data.log if verification fails."
	return data, nil
}

func startPowerShell(args []string) (*exec.Cmd, error) {
	var errs []error
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		cmd := exec.Command(name, args...)
		if err := cmd.Start(); err == nil {
			return cmd, nil
		} else {
			errs = append(errs, err)
		}
	}
	return nil, errors.Join(errs...)
}

func updateCommandEnv(env []string, opts updateOptions) []string {
	env = upsertEnv(env, "WECHAT_CLI_INSTALL_JSON", "1")
	if opts.KeepDownload {
		env = upsertEnv(env, "WECHAT_CLI_KEEP_DOWNLOAD", "1")
	}
	return env
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func updateResultBase(opts updateOptions) map[string]any {
	query := map[string]any{
		"command":  "update",
		"platform": runtime.GOOS,
		"method":   "release_bootstrap",
		"dry_run":  opts.DryRun,
	}
	if opts.Repo != "" {
		query["repo"] = opts.Repo
	}
	if opts.Tag != "" {
		query["tag"] = opts.Tag
	}
	if opts.Asset != "" {
		query["asset"] = opts.Asset
	}
	if opts.KeepDownload {
		query["keep_download"] = true
	}
	return map[string]any{"query": query}
}

func attachUpdaterOutput(data map[string]any, stdout, stderr []byte) {
	if parsed, ok := parseJSONOutput(stdout); ok {
		data["installer"] = parsed
	} else if s := strings.TrimSpace(string(stdout)); s != "" {
		data["stdout"] = truncateUpdateOutput(s)
	}
	if s := strings.TrimSpace(string(stderr)); s != "" && envBoolAny("WECHAT_CLI_UPDATE_DEBUG") {
		data["stderr"] = truncateUpdateOutput(s)
	}
}

func parseJSONOutput(raw []byte) (any, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func truncateUpdateOutput(s string) string {
	const limit = 8000
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... truncated ..."
}

func updateFailureHint(stdout, stderr string) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		return ""
	}
	return ": " + truncateUpdateOutput(msg)
}

func updateLogPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "wechat-cli", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("update-%s.log", time.Now().Format("20060102-150405"))), nil
}

func writeWindowsUpdateScript() (string, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("wechat-cli-update-%d.ps1", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(windowsReleaseUpdateScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

const windowsReleaseUpdateScript = `
param(
  [int]$ParentPid,
  [string]$Url,
  [string]$LogPath,
  [string]$Repo = "",
  [string]$Tag = "",
  [string]$Asset = "",
  [switch]$DryRun,
  [switch]$KeepDownload
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $LogPath) | Out-Null

function Write-UpdateLog([string]$Text) {
  Add-Content -LiteralPath $LogPath -Value ("[{0}] {1}" -f (Get-Date).ToString("s"), $Text)
}

try {
  Write-UpdateLog "wechat-cli update started"
  if ($ParentPid -gt 0) {
    try {
      Wait-Process -Id $ParentPid -Timeout 120 -ErrorAction SilentlyContinue
    } catch {
    }
  }

  $bootstrap = Join-Path ([IO.Path]::GetTempPath()) ("wechat-cli-install-release-" + [Guid]::NewGuid().ToString("N") + ".ps1")
  Write-UpdateLog "Downloading bootstrap: $Url"
  Invoke-WebRequest -Uri $Url -OutFile $bootstrap -UseBasicParsing

  $installArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $bootstrap, "-Update", "-Json")
  if ($DryRun) { $installArgs += "-DryRun" }
  if ($KeepDownload) { $installArgs += "-KeepDownload" }
  if (-not [string]::IsNullOrWhiteSpace($Repo)) { $installArgs += @("-Repo", $Repo) }
  if (-not [string]::IsNullOrWhiteSpace($Tag)) { $installArgs += @("-Tag", $Tag) }
  if (-not [string]::IsNullOrWhiteSpace($Asset)) { $installArgs += @("-Asset", $Asset) }

  Write-UpdateLog "Running release updater"
  & powershell @installArgs 2>&1 | Tee-Object -FilePath $LogPath -Append | Out-Null
  $code = $LASTEXITCODE
  Write-UpdateLog "release updater exited with code $code"
  exit $code
} catch {
  Write-UpdateLog ("ERROR: " + $_.Exception.Message)
  exit 1
}
`
