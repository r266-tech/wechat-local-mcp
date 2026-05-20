#!/usr/bin/env zsh
set -u

APP_NAME="wx-mcp"
MCP_NAME="wx-mcp"
WATCHER_LABEL="com.r266.wx-mcp-cache-watcher"
SOURCE_DIR="${0:A:h}"
INSTALL_DIR="${WX_MCP_INSTALL_DIR:-$HOME/.local/share/wx-mcp}"
LOG_DIR="$HOME/Library/Logs/wx-mcp"
INSTALL_LOG="$LOG_DIR/install.log"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
PLIST_PATH="$LAUNCH_AGENTS_DIR/$WATCHER_LABEL.plist"
WATCHER_INTERVAL=300
MCP_CLIENT="auto"
MCP_SCOPE="user"

JSON=0
ASSUME_YES=0
DRY_RUN=0
MODE="install"
DO_BOOTSTRAP=0
DO_REFRESH=0
DO_WATCHER=0
REGISTER_MCP=1
PURGE_STATE=0

WXMCP_MODE=""
WXMCP_SOURCE=""
WXKEY_MODE=""
WXKEY_SOURCE=""
LIB_SOURCE=""

MCP_REGISTERED=0
WATCHER_INSTALLED=0
BOOTSTRAP_RAN=0
REFRESH_RAN=0
INSTALL_STATUS="ok"
BLOCKED_BY=""
NEXT_ACTION=""

typeset -a ACTIONS
typeset -a WARNINGS
typeset -a ERRORS
typeset -a CHECKS
typeset -a MCP_REGISTERED_CLIENTS

usage() {
  cat <<'EOF'
Usage:
  ./install.sh [--all] [--yes] [--json]
  ./install.sh --update [--yes] [--json]
  ./install.sh --doctor [--json]
  ./install.sh --dry-run --all --json
  ./install.sh --uninstall --yes [--json]
  ./install.sh --uninstall --purge-state --yes [--json]
  ./install.sh --clear-state --yes [--json]

Install options:
  --all                     Install, register MCP, run wxkey bootstrap,
                            and refresh metadata cache (does NOT install watcher;
                            add --watcher explicitly if you want periodic
                            background refresh — see README on TCC trade-off).
  --update                  Update an existing git checkout with
                            `git pull --ff-only`, then reinstall binaries.
                            Does not bootstrap, refresh metadata cache, register MCP,
                            or touch watcher unless those flags are added.
  --bootstrap               Run wxkey bootstrap after installing binaries.
  --refresh                 Start wx-mcp metadata cache refresh after installing binaries.
                            Defaults to background warmup; set
                            WX_MCP_INSTALL_SYNC_REFRESH=1 for foreground wait.
  --watcher                 Install launchd cache watcher (5-min periodic
                            metadata cache refresh). WARNING: on macOS 15+ each refresh
                            triggers a "wx-mcp wants to access another app's
                            data" TCC prompt unless wx-mcp has Full Disk Access
                            granted in System Settings → Privacy & Security.
  --no-mcp                  Do not register MCP.
  --mcp-client auto|claude|codex|none
  --install-dir PATH        Default: ~/.local/share/wx-mcp
  --watcher-interval SEC    Default: 300.
  --yes                     Non-interactive approval for side effects.
  --json                    Emit a single JSON result to stdout.
  --dry-run                 Report planned actions without writing.
  --doctor                  Check local install prerequisites/status.
  --uninstall               Remove installed files, watcher plist, and MCP entry.
  --purge-state             With --uninstall, also remove wx-mcp state:
                            ~/.config/wxcli/config.json, ~/.wx-mcp, logs,
                            and the wxkey Keychain sudo credential.
  --clear-state             Only remove wx-mcp state; keep installed binaries
                            and MCP registration.

Environment:
  WX_MCP_INSTALL_DIR        Override install directory.
  WX_MCP_WCDB_DYLIB         Existing libWCDB.dylib to copy.
  WXKEY_SRC                 Source checkout for wxkey when installing from source.
  WXKEY_BIN                 Existing wxkey binary to copy.
  WXKEY_GO_INSTALL          Go package/version for source fallback
                            (default github.com/r266-tech/wxkey/cmd/wxkey@latest).
EOF
}

json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  print -r -- "$s"
}

xml_escape() {
  local s="$1"
  s="${s//&/&amp;}"
  s="${s//</&lt;}"
  s="${s//>/&gt;}"
  s="${s//\"/&quot;}"
  print -r -- "$s"
}

shell_escape() {
  print -r -- "${(q)1}"
}

json_bool() {
  if [[ "$1" -eq 1 ]]; then
    print -n "true"
  else
    print -n "false"
  fi
}

json_array() {
  local first=1 item
  print -n "["
  for item in "$@"; do
    if [[ "$first" -eq 0 ]]; then
      print -n ", "
    fi
    first=0
    print -n "\"$(json_escape "$item")\""
  done
  print -n "]"
}

emit_json() {
  local ok="$1"
  print "{"
  print "  \"ok\": $ok,"
  print "  \"mode\": \"$(json_escape "$MODE")\","
  print "  \"status\": \"$(json_escape "$INSTALL_STATUS")\","
  print "  \"blocked_by\": \"$(json_escape "$BLOCKED_BY")\","
  print "  \"next_action\": \"$(json_escape "$NEXT_ACTION")\","
  print "  \"source_dir\": \"$(json_escape "$SOURCE_DIR")\","
  print "  \"install_dir\": \"$(json_escape "$INSTALL_DIR")\","
  print "  \"mcp_client\": \"$(json_escape "$MCP_CLIENT")\","
  print "  \"mcp_scope\": \"$(json_escape "$MCP_SCOPE")\","
  print "  \"watcher_label\": \"$(json_escape "$WATCHER_LABEL")\","
  print "  \"watcher_interval\": $WATCHER_INTERVAL,"
  print "  \"log\": \"$(json_escape "$INSTALL_LOG")\","
  print "  \"mcp_registered\": $(json_bool "$MCP_REGISTERED"),"
  print "  \"watcher_installed\": $(json_bool "$WATCHER_INSTALLED"),"
  print "  \"bootstrap_ran\": $(json_bool "$BOOTSTRAP_RAN"),"
  print "  \"refresh_ran\": $(json_bool "$REFRESH_RAN"),"
  print "  \"purge_state\": $(json_bool "$PURGE_STATE"),"
  print -n "  \"mcp_registered_clients\": "; json_array "${MCP_REGISTERED_CLIENTS[@]}"; print ","
  print -n "  \"checks\": "; json_array "${CHECKS[@]}"; print ","
  print -n "  \"actions\": "; json_array "${ACTIONS[@]}"; print ","
  print -n "  \"warnings\": "; json_array "${WARNINGS[@]}"; print ","
  print -n "  \"errors\": "; json_array "${ERRORS[@]}"; print ""
  print "}"
}

finish() {
  local ok="$1"
  if [[ "$JSON" -eq 1 ]]; then
    emit_json "$ok"
  fi
}

say() {
  if [[ "$JSON" -eq 1 ]]; then
    ensure_log_dir
    print -r -- "$*" >> "$INSTALL_LOG"
  else
    print -r -- "$*"
  fi
}

warn() {
  WARNINGS+=("$1")
  if [[ "$JSON" -ne 1 ]]; then
    print -r -- "WARN: $1" >&2
  fi
}

die() {
  local msg="$1"
  local code="${2:-1}"
  ERRORS+=("$msg")
  if [[ "$JSON" -eq 1 ]]; then
    emit_json false
  else
    print -r -- "ERROR: $msg" >&2
  fi
  exit "$code"
}

ensure_log_dir() {
  mkdir -p "$LOG_DIR"
}

run_logged() {
  if [[ "$JSON" -eq 1 ]]; then
    ensure_log_dir
    {
      print -r -- ""
      print -r -- ">>> $*"
    } >> "$INSTALL_LOG"
    "$@" >> "$INSTALL_LOG" 2>&1
  else
    "$@"
  fi
}

run_logged_in() {
  local dir="$1"
  shift
  ( cd "$dir" && run_logged "$@" )
}

have_cmd() {
  command -v "$1" >/dev/null 2>&1
}

expand_path() {
  local p="$1"
  p="${p/#\~/$HOME}"
  print -r -- "$p"
}

parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --help|-h)
        usage
        exit 0
        ;;
      --json)
        JSON=1
        shift
        ;;
      --yes|-y)
        ASSUME_YES=1
        shift
        ;;
      --dry-run)
        DRY_RUN=1
        shift
        ;;
      --doctor)
        MODE="doctor"
        shift
        ;;
      --uninstall)
        MODE="uninstall"
        shift
        ;;
      --clear-state)
        MODE="clear-state"
        PURGE_STATE=1
        REGISTER_MCP=0
        MCP_CLIENT="none"
        shift
        ;;
      --purge-state)
        PURGE_STATE=1
        shift
        ;;
      --update)
        MODE="update"
        REGISTER_MCP=0
        MCP_CLIENT="none"
        shift
        ;;
      --all)
        DO_BOOTSTRAP=1
        DO_REFRESH=1
        REGISTER_MCP=1
        # watcher intentionally NOT in --all: on macOS 15+ the periodic
        # cross-container access triggers TCC re-prompts ("wx-mcp 想访问其他
        # App 的数据") repeatedly for ad-hoc signed binaries. Users who
        # actually want background metadata cache refresh can pass --watcher.
        shift
        ;;
      --bootstrap)
        DO_BOOTSTRAP=1
        shift
        ;;
      --no-bootstrap)
        DO_BOOTSTRAP=0
        shift
        ;;
      --refresh)
        DO_REFRESH=1
        shift
        ;;
      --no-refresh)
        DO_REFRESH=0
        shift
        ;;
      --watcher)
        DO_WATCHER=1
        shift
        ;;
      --no-watcher)
        DO_WATCHER=0
        shift
        ;;
      --no-mcp)
        REGISTER_MCP=0
        MCP_CLIENT="none"
        shift
        ;;
      --mcp-client)
        [[ "$#" -ge 2 ]] || die "--mcp-client requires a value" 2
        MCP_CLIENT="$2"
        shift 2
        ;;
      --mcp-client=*)
        MCP_CLIENT="${1#*=}"
        shift
        ;;
      --install-dir)
        [[ "$#" -ge 2 ]] || die "--install-dir requires a value" 2
        INSTALL_DIR="$(expand_path "$2")"
        shift 2
        ;;
      --install-dir=*)
        INSTALL_DIR="$(expand_path "${1#*=}")"
        shift
        ;;
      --watcher-interval)
        [[ "$#" -ge 2 ]] || die "--watcher-interval requires a value" 2
        WATCHER_INTERVAL="$2"
        shift 2
        ;;
      --watcher-interval=*)
        WATCHER_INTERVAL="${1#*=}"
        shift
        ;;
      *)
        die "unknown argument: $1" 2
        ;;
    esac
  done

  case "$MCP_CLIENT" in
    auto|claude|codex|none) ;;
    *) die "--mcp-client must be auto, claude, codex, or none" 2 ;;
  esac
  if [[ "$PURGE_STATE" -eq 1 && "$MODE" != "uninstall" && "$MODE" != "clear-state" ]]; then
    die "--purge-state is only valid with --uninstall; use --clear-state to remove state without uninstalling" 2
  fi
  [[ "$WATCHER_INTERVAL" == <-> ]] || die "--watcher-interval must be an integer" 2
  if [[ "$WATCHER_INTERVAL" -lt 60 ]]; then
    warn "watcher interval below 60s is allowed but may overlap long metadata cache refreshes"
  fi
}

confirm_or_die() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  if [[ "$ASSUME_YES" -eq 1 ]]; then
    return
  fi
  if [[ "$JSON" -eq 1 || ! -t 0 ]]; then
    die "non-interactive run requires --yes" 2
  fi
  print -n "Proceed with $MODE into $INSTALL_DIR? [y/N] "
  local ans
  read ans
  case "$ans" in
    y|Y|yes|YES) ;;
    *) die "cancelled" 1 ;;
  esac
}

resolve_components() {
  if [[ -f "$SOURCE_DIR/cmd/wx-mcp/main.go" ]]; then
    if have_cmd go; then
      WXMCP_MODE="build"
      WXMCP_SOURCE="$SOURCE_DIR"
    elif [[ -x "$SOURCE_DIR/wx-mcp" ]]; then
      WXMCP_MODE="copy"
      WXMCP_SOURCE="$SOURCE_DIR/wx-mcp"
      warn "go not found; using existing wx-mcp binary from source dir"
    else
      ERRORS+=("go not found and no wx-mcp binary available")
    fi
  elif [[ -x "$SOURCE_DIR/wx-mcp" ]]; then
    WXMCP_MODE="copy"
    WXMCP_SOURCE="$SOURCE_DIR/wx-mcp"
  else
    ERRORS+=("wx-mcp source or binary not found under $SOURCE_DIR")
  fi

  if [[ -x "$SOURCE_DIR/wxkey" ]]; then
    WXKEY_MODE="copy"
    WXKEY_SOURCE="$SOURCE_DIR/wxkey"
  elif [[ -n "${WXKEY_SRC:-}" && -f "$WXKEY_SRC/cmd/wxkey/main.go" && -n "$(command -v go 2>/dev/null)" ]]; then
    WXKEY_MODE="build"
    WXKEY_SOURCE="$WXKEY_SRC"
  elif [[ -f "$SOURCE_DIR/../wxkey/cmd/wxkey/main.go" && -n "$(command -v go 2>/dev/null)" ]]; then
    WXKEY_MODE="build"
    WXKEY_SOURCE="$SOURCE_DIR/../wxkey"
  elif [[ -n "${WXKEY_BIN:-}" && -x "$WXKEY_BIN" ]]; then
    WXKEY_MODE="copy"
    WXKEY_SOURCE="$WXKEY_BIN"
  elif [[ -x "$SOURCE_DIR/../wxkey/wxkey" ]]; then
    WXKEY_MODE="copy"
    WXKEY_SOURCE="$SOURCE_DIR/../wxkey/wxkey"
  elif have_cmd go; then
    WXKEY_MODE="go-install"
    WXKEY_SOURCE="${WXKEY_GO_INSTALL:-github.com/r266-tech/wxkey/cmd/wxkey@latest}"
  elif have_cmd wxkey; then
    WXKEY_MODE="copy"
    WXKEY_SOURCE="$(command -v wxkey)"
  else
    ERRORS+=("wxkey binary/source not found; install Go, use release zip with wxkey, set WXKEY_SRC, or set WXKEY_BIN")
  fi

  local cand
  for cand in "${WX_MCP_WCDB_DYLIB:-}" "$SOURCE_DIR/libWCDB.dylib" "$SOURCE_DIR/lib/libWCDB.dylib" "$HOME/.config/wxcli/lib/libWCDB.dylib" "$INSTALL_DIR/libWCDB.dylib"; do
    if [[ -f "$cand" ]]; then
      LIB_SOURCE="$cand"
      break
    fi
  done
  if [[ -z "$LIB_SOURCE" ]]; then
    ERRORS+=("libWCDB.dylib not found; use release zip, set WX_MCP_WCDB_DYLIB, or place it at ./lib/libWCDB.dylib / ~/.config/wxcli/lib/libWCDB.dylib")
  fi

  [[ -n "$WXMCP_MODE" ]] && ACTIONS+=("$WXMCP_MODE wx-mcp from $WXMCP_SOURCE")
  [[ -n "$WXKEY_MODE" ]] && ACTIONS+=("$WXKEY_MODE wxkey from $WXKEY_SOURCE")
  [[ -n "$LIB_SOURCE" ]] && ACTIONS+=("copy libWCDB.dylib from $LIB_SOURCE")
}

install_components() {
  resolve_components
  if [[ "${#ERRORS[@]}" -gt 0 ]]; then
    die "component resolution failed" 1
  fi
  ACTIONS+=("install files into $INSTALL_DIR")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  mkdir -p "$INSTALL_DIR"

  if [[ "$WXMCP_MODE" == "build" ]]; then
    run_logged_in "$WXMCP_SOURCE" go build -o "$INSTALL_DIR/wx-mcp" ./cmd/wx-mcp || die "build wx-mcp failed; see $INSTALL_LOG" 1
  else
    cp "$WXMCP_SOURCE" "$INSTALL_DIR/wx-mcp" || die "copy wx-mcp failed" 1
  fi
  chmod +x "$INSTALL_DIR/wx-mcp"

  if [[ "$WXKEY_MODE" == "build" ]]; then
    run_logged_in "$WXKEY_SOURCE" go build -o "$INSTALL_DIR/wxkey" ./cmd/wxkey || die "build wxkey failed; see $INSTALL_LOG" 1
  elif [[ "$WXKEY_MODE" == "go-install" ]]; then
    run_logged env GOBIN="$INSTALL_DIR" go install "$WXKEY_SOURCE" || die "install wxkey from GitHub failed; see $INSTALL_LOG" 1
  else
    cp "$WXKEY_SOURCE" "$INSTALL_DIR/wxkey" || die "copy wxkey failed" 1
  fi
  chmod +x "$INSTALL_DIR/wxkey"

  if [[ "$LIB_SOURCE" != "$INSTALL_DIR/libWCDB.dylib" ]]; then
    cp "$LIB_SOURCE" "$INSTALL_DIR/libWCDB.dylib" || die "copy libWCDB.dylib failed" 1
  fi
}

update_source() {
  if [[ -d "$SOURCE_DIR/.git" ]]; then
    ACTIONS+=("git pull --ff-only in $SOURCE_DIR")
    if [[ "$DRY_RUN" -eq 1 ]]; then
      return
    fi
    run_logged_in "$SOURCE_DIR" git pull --ff-only || die "git update failed; resolve the checkout or download the latest release zip" 1
    return
  fi
  warn "source_dir is not a git checkout; --update will reinstall current files only. For release-zip installs, have the agent download the latest GitHub release zip first."
}

register_mcp() {
  [[ "$REGISTER_MCP" -eq 1 ]] || return

  local client="$MCP_CLIENT"
  if [[ "$client" == "auto" ]]; then
    local found=0
    if have_cmd claude; then
      register_claude_mcp
      found=1
    fi
    if have_cmd codex; then
      register_codex_mcp
      found=1
    fi
    if [[ "$found" -eq 0 ]]; then
      warn "no supported MCP client command found; skipping MCP registration"
    fi
    return
  fi
  if [[ "$client" == "none" ]]; then
    return
  fi
  case "$client" in
    claude) register_claude_mcp ;;
    codex) register_codex_mcp ;;
  esac
}

register_claude_mcp() {
  if ! have_cmd claude; then
    die "claude command not found; use --mcp-client none or install Claude Code" 1
  fi
  ACTIONS+=("register Claude MCP server $MCP_NAME at $INSTALL_DIR/wx-mcp")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  run_logged claude mcp remove -s "$MCP_SCOPE" "$MCP_NAME" || true
  run_logged claude mcp add -s "$MCP_SCOPE" "$MCP_NAME" "$INSTALL_DIR/wx-mcp" || die "Claude MCP registration failed; see $INSTALL_LOG" 1
  MCP_REGISTERED=1
  MCP_REGISTERED_CLIENTS+=("claude")
}

register_codex_mcp() {
  if ! have_cmd codex; then
    die "codex command not found; use --mcp-client none or install Codex CLI" 1
  fi
  ACTIONS+=("register Codex MCP server $MCP_NAME at $INSTALL_DIR/wx-mcp")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  run_logged codex mcp remove "$MCP_NAME" || true
  run_logged codex mcp add "$MCP_NAME" -- "$INSTALL_DIR/wx-mcp" || die "Codex MCP registration failed; see $INSTALL_LOG" 1
  MCP_REGISTERED=1
  MCP_REGISTERED_CLIENTS+=("codex")
}

remove_mcp_entries() {
  [[ "$REGISTER_MCP" -eq 1 && "$MCP_CLIENT" != "none" ]] || return
  local client="$MCP_CLIENT"
  if [[ "$client" == "auto" || "$client" == "claude" ]]; then
    ACTIONS+=("remove Claude MCP server $MCP_NAME")
    if [[ "$DRY_RUN" -eq 0 && -n "$(command -v claude 2>/dev/null)" ]]; then
      run_logged claude mcp remove -s "$MCP_SCOPE" "$MCP_NAME" || true
    fi
  fi
  if [[ "$client" == "auto" || "$client" == "codex" ]]; then
    ACTIONS+=("remove Codex MCP server $MCP_NAME")
    if [[ "$DRY_RUN" -eq 0 && -n "$(command -v codex 2>/dev/null)" ]]; then
      run_logged codex mcp remove "$MCP_NAME" || true
    fi
  fi
}

classify_install_log_blocker() {
  local text=""
  if [[ -f "$INSTALL_LOG" ]]; then
    text="$(tail -120 "$INSTALL_LOG" 2>/dev/null)"
  fi
  case "$text" in
    *"WeChat is not ready yet"*|*"WeChat process not running"*|*"no WeChat 4.x account directory"*)
      BLOCKED_BY="wechat_not_ready"
      NEXT_ACTION="Open WeChat, finish login, open one chat, then rerun ./install.sh --bootstrap --refresh --yes --json."
      ;;
    *"Operation not permitted"*|*"codesign WeChat failed"*|*"app-management"*|*"App Management"*)
      BLOCKED_BY="app_management_denied"
      NEXT_ACTION="Grant App Management/Full Disk Access if macOS requests it, then rerun ./install.sh --bootstrap --refresh --yes --json."
      ;;
    *"task_for_pid"*|*"not permitted"*)
      BLOCKED_BY="task_for_pid_denied"
      NEXT_ACTION="Rerun ./wxkey bootstrap from the Mac desktop session and enter the wx-mcp hidden admin-password prompt."
      ;;
    *"Full Disk Access"*|*"TCC"*|*"another app"*)
      BLOCKED_BY="full_disk_access"
      NEXT_ACTION="Grant Full Disk Access to ~/.local/share/wx-mcp/wx-mcp and ~/.local/share/wx-mcp/wxkey, then rerun install."
      ;;
    *)
      BLOCKED_BY="bootstrap_failed"
      NEXT_ACTION="Inspect the install log and rerun ./wxkey doctor; do not disable SIP."
      ;;
  esac
}

run_bootstrap() {
  [[ "$DO_BOOTSTRAP" -eq 1 ]] || return
  ACTIONS+=("run wxkey bootstrap")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  if ! run_logged "$INSTALL_DIR/wxkey" bootstrap; then
    if [[ -f "$INSTALL_LOG" ]]; then
      local trail
      trail=$(grep -E '^\[wxkey\]|^\[FAIL\]|ERROR:|re-elevate|task_for_pid' "$INSTALL_LOG" 2>/dev/null | tail -5 | tr '\n' '|')
      [[ -n "$trail" ]] && ERRORS+=("wxkey log tail: ${trail%|}")
    fi
    INSTALL_STATUS="blocked"
    classify_install_log_blocker
    die "wxkey bootstrap failed; see $INSTALL_LOG. If install.sh was run through an AI agent or non-interactive shell, the macOS password prompt cannot surface — re-run \`$INSTALL_DIR/wxkey bootstrap\` directly on the Mac's desktop (no sudo)." 1
  fi
  BOOTSTRAP_RAN=1
}

run_cache_refresh() {
  [[ "$DO_REFRESH" -eq 1 ]] || return
  ACTIONS+=("start wx-mcp metadata cache refresh in background")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  if [[ "${WX_MCP_INSTALL_SYNC_REFRESH:-0}" == "1" ]]; then
    ACTIONS+=("run wx-mcp metadata cache refresh in foreground because WX_MCP_INSTALL_SYNC_REFRESH=1")
    run_logged "$INSTALL_DIR/wx-mcp" cache refresh || die "metadata cache refresh failed; see $INSTALL_LOG" 1
    INSTALL_STATUS="ready"
  else
    run_logged "$INSTALL_DIR/wx-mcp" cache refresh --background || die "metadata cache refresh background start failed; see $INSTALL_LOG" 1
    INSTALL_STATUS="warming_cache"
    NEXT_ACTION="wx-mcp is installed; metadata cache refresh is warming in the background and name/session tools will freshness-check before returning data."
    CHECKS+=("cache_refresh_background=true")
  fi
  REFRESH_RAN=1
}

write_watcher_script() {
  local script="$INSTALL_DIR/watcher.sh"
  cat > "$script" <<EOF
#!/usr/bin/env zsh
set -u

PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
INSTALL_DIR=$(shell_escape "$INSTALL_DIR")
LOG_DIR="\$HOME/Library/Logs/wx-mcp"
STATE_DIR="\$HOME/.wx-mcp"
LOCK_DIR="\$STATE_DIR/cache-refresh.lock"
LOG_FILE="\$LOG_DIR/cache-watcher.log"

mkdir -p "\$LOG_DIR" "\$STATE_DIR"

if ! mkdir "\$LOCK_DIR" 2>/dev/null; then
  now=\$(date +%s)
  mod=\$(stat -f %m "\$LOCK_DIR" 2>/dev/null || echo 0)
  if [[ "\$mod" == <-> && \$((now - mod)) -gt 7200 ]]; then
    rmdir "\$LOCK_DIR" 2>/dev/null || true
    mkdir "\$LOCK_DIR" 2>/dev/null || exit 0
  else
    echo "\$(date -u '+%Y-%m-%dT%H:%M:%SZ') skip: refresh already running" >> "\$LOG_FILE"
    exit 0
  fi
fi
trap 'rmdir "\$LOCK_DIR" 2>/dev/null || true' EXIT INT TERM

echo "\$(date -u '+%Y-%m-%dT%H:%M:%SZ') cache refresh start" >> "\$LOG_FILE"
WX_MCP_CACHE_LOCK_HELD=1 "\$INSTALL_DIR/wx-mcp" cache refresh >> "\$LOG_FILE" 2>&1
rc=\$?
echo "\$(date -u '+%Y-%m-%dT%H:%M:%SZ') cache refresh exit=\$rc" >> "\$LOG_FILE"
exit "\$rc"
EOF
  chmod +x "$script"
}

write_watcher_plist() {
  mkdir -p "$LAUNCH_AGENTS_DIR" "$LOG_DIR"
  local script="$INSTALL_DIR/watcher.sh"
  cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$(xml_escape "$WATCHER_LABEL")</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$script")</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>StartInterval</key>
  <integer>$WATCHER_INTERVAL</integer>
  <key>StandardOutPath</key>
  <string>$(xml_escape "$LOG_DIR/cache-watcher.launchd.log")</string>
  <key>StandardErrorPath</key>
  <string>$(xml_escape "$LOG_DIR/cache-watcher.launchd.err.log")</string>
</dict>
</plist>
EOF
}

install_watcher() {
  [[ "$DO_WATCHER" -eq 1 ]] || return
  ACTIONS+=("install launchd watcher $WATCHER_LABEL every ${WATCHER_INTERVAL}s")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  write_watcher_script
  write_watcher_plist

  local domain="gui/$(id -u)"
  run_logged launchctl bootout "$domain" "$PLIST_PATH" || true
  run_logged launchctl bootstrap "$domain" "$PLIST_PATH" || die "launchd watcher bootstrap failed; see $INSTALL_LOG" 1
  run_logged launchctl enable "$domain/$WATCHER_LABEL" || true
  run_logged launchctl kickstart -k "$domain/$WATCHER_LABEL" || true
  WATCHER_INSTALLED=1
}

cleanup_legacy_message_cache() {
  local cache_root="$HOME/.wx-mcp/cache"
  [[ -d "$cache_root" ]] || return
  ACTIONS+=("drop existing cache indexes and non-metadata raw snapshots under $cache_root")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  local root child
  for root in "$cache_root"/*; do
    [[ -d "$root" ]] || continue
    rm -f "$root/index.sqlite" "$root/index.sqlite-wal" "$root/index.sqlite-shm"
    if [[ -d "$root/raw" ]]; then
      for child in "$root/raw"/*; do
        [[ -e "$child" ]] || continue
        case "$(basename "$child")" in
          contact|session) ;;
          *) rm -rf "$child" ;;
        esac
      done
    fi
  done
}

doctor() {
  [[ "$(uname -s)" == "Darwin" ]] && CHECKS+=("os=Darwin") || WARNINGS+=("os is not Darwin")
  CHECKS+=("arch=$(uname -m)")
  [[ -d "$SOURCE_DIR" ]] && CHECKS+=("source_dir_exists=true") || WARNINGS+=("source_dir_missing=$SOURCE_DIR")
  [[ -d "$INSTALL_DIR" ]] && CHECKS+=("install_dir_exists=true") || CHECKS+=("install_dir_exists=false")
  [[ -x "$INSTALL_DIR/wx-mcp" ]] && CHECKS+=("installed_wx_mcp=true") || CHECKS+=("installed_wx_mcp=false")
  [[ -x "$INSTALL_DIR/wxkey" ]] && CHECKS+=("installed_wxkey=true") || CHECKS+=("installed_wxkey=false")
  [[ -f "$INSTALL_DIR/libWCDB.dylib" ]] && CHECKS+=("installed_libWCDB=true") || CHECKS+=("installed_libWCDB=false")
  have_cmd go && CHECKS+=("go=true") || CHECKS+=("go=false")
  have_cmd claude && CHECKS+=("claude=true") || CHECKS+=("claude=false")
  have_cmd codex && CHECKS+=("codex=true") || CHECKS+=("codex=false")
  [[ -f "$PLIST_PATH" ]] && CHECKS+=("watcher_plist=true") || CHECKS+=("watcher_plist=false")
  if [[ -f "$PLIST_PATH" ]] && have_cmd launchctl; then
    if launchctl print "gui/$(id -u)/$WATCHER_LABEL" >/dev/null 2>&1; then
      CHECKS+=("watcher_loaded=true")
    else
      CHECKS+=("watcher_loaded=false")
    fi
  fi
  if [[ -x "$INSTALL_DIR/wx-mcp" ]]; then
    if run_logged "$INSTALL_DIR/wx-mcp" cache status; then
      CHECKS+=("cache_status_ok=true")
    else
      CHECKS+=("cache_status_ok=false")
    fi
  fi
}

sudo_keychain_account() {
  if [[ -n "${WXKEY_ORIG_USER:-}" && "${WXKEY_ORIG_USER:-}" != "root" ]]; then
    print -r -- "$WXKEY_ORIG_USER"
  elif [[ -n "${SUDO_USER:-}" && "${SUDO_USER:-}" != "root" ]]; then
    print -r -- "$SUDO_USER"
  elif [[ -n "${USER:-}" ]]; then
    print -r -- "$USER"
  else
    id -un 2>/dev/null || print -r -- "wx-mcp"
  fi
}

queue_purge_state_actions() {
  ACTIONS+=("remove wxkey config file $HOME/.config/wxcli/config.json")
  ACTIONS+=("remove wx-mcp state dir $HOME/.wx-mcp")
  ACTIONS+=("remove wx-mcp logs $LOG_DIR")
  ACTIONS+=("delete Keychain generic password r266.wx-mcp.sudo for account $(sudo_keychain_account)")
}

run_purge_state() {
  rm -f "$HOME/.config/wxcli/config.json"
  rm -rf "$HOME/.wx-mcp"
  if [[ -x /usr/bin/security ]]; then
    /usr/bin/security delete-generic-password -a "$(sudo_keychain_account)" -s "r266.wx-mcp.sudo" >/dev/null 2>&1 || true
  fi
  rm -rf "$LOG_DIR"
}

remove_watcher() {
  ACTIONS+=("remove watcher $WATCHER_LABEL")
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  local domain="gui/$(id -u)"
  if [[ -f "$PLIST_PATH" ]]; then
    run_logged launchctl bootout "$domain" "$PLIST_PATH" || true
    rm -f "$PLIST_PATH"
  fi
  rm -f "$INSTALL_DIR/watcher.sh"
}

clear_state() {
  remove_watcher
  queue_purge_state_actions
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi
  run_purge_state
}

uninstall() {
  remove_watcher
  ACTIONS+=("remove install dir $INSTALL_DIR")
  remove_mcp_entries
  if [[ "$PURGE_STATE" -eq 1 ]]; then
    queue_purge_state_actions
  fi
  if [[ "$DRY_RUN" -eq 1 ]]; then
    return
  fi

  rm -rf "$INSTALL_DIR"
  if [[ "$PURGE_STATE" -eq 1 ]]; then
    run_purge_state
  fi
}

main() {
  parse_args "$@"
  INSTALL_DIR="$(expand_path "$INSTALL_DIR")"
  LOG_DIR="$(expand_path "$LOG_DIR")"
  INSTALL_LOG="$LOG_DIR/install.log"
  PLIST_PATH="$LAUNCH_AGENTS_DIR/$WATCHER_LABEL.plist"

  case "$MODE" in
    doctor)
      doctor
      finish true
      ;;
    uninstall)
      confirm_or_die
      uninstall
      finish true
      ;;
    clear-state)
      confirm_or_die
      clear_state
      finish true
      ;;
    install)
      confirm_or_die
      install_components
      cleanup_legacy_message_cache
      register_mcp
      run_bootstrap
      run_cache_refresh
      install_watcher
      finish true
      ;;
    update)
      confirm_or_die
      update_source
      install_components
      cleanup_legacy_message_cache
      register_mcp
      run_bootstrap
      run_cache_refresh
      install_watcher
      finish true
      ;;
    *)
      die "unknown mode: $MODE" 2
      ;;
  esac
}

main "$@"
