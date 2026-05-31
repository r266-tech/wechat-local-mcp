#!/bin/zsh
set -euo pipefail

REPO="${WECHAT_CLI_REPO:-${WX_MCP_REPO:-https://github.com/r266-tech/wechat-cli}}"
TAG="${WECHAT_CLI_RELEASE_TAG:-${WX_MCP_RELEASE_TAG:-latest}}"
ASSET="${WECHAT_CLI_RELEASE_ASSET:-${WX_MCP_RELEASE_ASSET:-wechat-cli-latest-darwin-arm64.zip}}"
KEEP_DOWNLOAD="${WECHAT_CLI_KEEP_DOWNLOAD:-${WX_MCP_KEEP_DOWNLOAD:-0}}"
JSON="${WECHAT_CLI_INSTALL_JSON:-${WX_MCP_INSTALL_JSON:-0}}"
MODE="install"
DRY_RUN=0
CLEANUP_DIR=""

typeset -a INSTALL_ARGS
INSTALL_ARGS=(--all --yes)

usage() {
  cat <<'EOF'
Usage:
  curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh
  ./scripts/install-release.sh [--dry-run] [--json] [--update] [installer args...]

Environment:
  WECHAT_CLI_REPO             GitHub repo URL or owner/name. Default: r266-tech/wechat-cli.
  WECHAT_CLI_RELEASE_TAG      GitHub release tag. Default: latest.
  WECHAT_CLI_RELEASE_ASSET    Release asset name. Default: wechat-cli-latest-darwin-arm64.zip.
  WECHAT_CLI_INSTALL_JSON     Pass --json to the bundled installer when set to 1.
  WECHAT_CLI_KEEP_DOWNLOAD    Keep the temporary download directory when set to 1.
EOF
}

say() {
  if [[ "$JSON" == "1" ]]; then
    print -r -- "$*" >&2
  else
    print -r -- "$*"
  fi
}

warn() {
  print -r -- "WARN: $*" >&2
}

fail() {
  print -r -- "ERROR: $*" >&2
  exit 1
}

have_cmd() {
  command -v "$1" >/dev/null 2>&1
}

repo_slug() {
  local repo="$1"
  repo="${repo#https://github.com/}"
  repo="${repo#http://github.com/}"
  repo="${repo%.git}"
  repo="${repo%/}"
  print -r -- "$repo"
}

repo_url() {
  local repo="$1"
  if [[ "$repo" == http://* || "$repo" == https://* ]]; then
    repo="${repo%.git}"
    print -r -- "${repo%/}"
  else
    repo="${repo%.git}"
    print -r -- "https://github.com/${repo%/}"
  fi
}

download_file() {
  local url="$1"
  local dest="$2"
  if have_cmd curl; then
    curl -fsSL --retry 3 --connect-timeout 15 -o "$dest" "$url"
  elif have_cmd wget; then
    wget -O "$dest" "$url"
  else
    fail "curl or wget is required to download wechat-cli."
  fi
}

cleanup_download() {
  [[ -n "${CLEANUP_DIR:-}" ]] || return
  rm -rf "$CLEANUP_DIR"
}

fetch_text() {
  local url="$1"
  if have_cmd curl; then
    curl -fsL --retry 3 --connect-timeout 15 "$url"
  elif have_cmd wget; then
    wget -qO- "$url"
  else
    fail "curl or wget is required to query GitHub releases."
  fi
}

asset_url() {
  local base="$1"
  local slug="$2"
  local tag="$3"
  local asset="$4"
  if [[ "$tag" == "latest" ]]; then
    print -r -- "$base/releases/latest/download/$asset"
  else
    print -r -- "$base/releases/download/$tag/$asset"
  fi
}

fallback_asset_url() {
  local slug="$1"
  local tag="$2"
  local api
  if [[ "$tag" == "latest" ]]; then
    api="https://api.github.com/repos/$slug/releases/latest"
  else
    api="https://api.github.com/repos/$slug/releases/tags/$tag"
  fi
  fetch_text "$api" \
    | sed -nE 's/.*"browser_download_url": "([^"]*darwin-arm64\.zip)".*/\1/p' \
    | grep -v '\.sha256$' \
    | head -n 1 \
    || true
}

verify_sha256() {
  local zip="$1"
  local sha_file="$2"
  [[ -f "$sha_file" ]] || return 0
  if ! have_cmd shasum; then
    warn "shasum not found; skipping checksum verification."
    return 0
  fi
  local expected actual
  expected="$(awk '{print tolower($1); exit}' "$sha_file")"
  actual="$(shasum -a 256 "$zip" | awk '{print tolower($1)}')"
  [[ -n "$expected" ]] || fail "empty sha256 file."
  [[ "$expected" == "$actual" ]] || fail "sha256 mismatch for downloaded release zip."
}

parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --help|-h)
        usage
        exit 0
        ;;
      --dry-run)
        DRY_RUN=1
        INSTALL_ARGS+=(--dry-run)
        shift
        ;;
      --json)
        JSON=1
        shift
        ;;
      --update)
        MODE="update"
        INSTALL_ARGS=(--update --yes)
        shift
        ;;
      --tag)
        [[ "$#" -ge 2 ]] || fail "--tag requires a value."
        TAG="$2"
        shift 2
        ;;
      --tag=*)
        TAG="${1#*=}"
        shift
        ;;
      --asset)
        [[ "$#" -ge 2 ]] || fail "--asset requires a value."
        ASSET="$2"
        shift 2
        ;;
      --asset=*)
        ASSET="${1#*=}"
        shift
        ;;
      --repo)
        [[ "$#" -ge 2 ]] || fail "--repo requires a value."
        REPO="$2"
        shift 2
        ;;
      --repo=*)
        REPO="${1#*=}"
        shift
        ;;
      *)
        INSTALL_ARGS+=("$1")
        shift
        ;;
    esac
  done
  if [[ "$JSON" == "1" ]]; then
    INSTALL_ARGS+=(--json)
  fi
  if [[ "$DRY_RUN" -eq 1 && "$MODE" == "install" ]]; then
    :
  fi
}

main() {
  parse_args "$@"

  [[ "$(uname -s)" == "Darwin" ]] || fail "this installer is for macOS; use scripts/install-release.ps1 on Windows."
  case "$(uname -m)" in
    arm64|aarch64) ;;
    *) fail "this release installer supports macOS arm64 only." ;;
  esac
  have_cmd unzip || fail "unzip is required."

  local slug base url tmp zip sha extract install_script install_dir fallback
  slug="$(repo_slug "$REPO")"
  base="$(repo_url "$REPO")"
  url="$(asset_url "$base" "$slug" "$TAG" "$ASSET")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/wechat-cli-install.XXXXXX")"
  if [[ "$KEEP_DOWNLOAD" != "1" ]]; then
    CLEANUP_DIR="$tmp"
    trap cleanup_download EXIT INT TERM
  else
    say "Keeping download directory: $tmp"
  fi

  zip="$tmp/$ASSET"
  sha="$tmp/$ASSET.sha256"
  extract="$tmp/extract"
  mkdir -p "$extract"

  say "Downloading wechat-cli release: $url"
  if ! download_file "$url" "$zip"; then
    warn "stable asset download failed; querying GitHub release metadata."
    fallback="$(fallback_asset_url "$slug" "$TAG")"
    [[ -n "$fallback" ]] || fail "could not find a darwin-arm64 release asset for $slug."
    url="$fallback"
    zip="$tmp/${fallback:t}"
    say "Downloading wechat-cli release: $url"
    download_file "$url" "$zip"
  fi

  if download_file "$url.sha256" "$sha" >/dev/null 2>&1; then
    verify_sha256 "$zip" "$sha"
    say "Verified release checksum."
  else
    warn "release checksum file not found; continuing without checksum verification."
  fi

  unzip -q "$zip" -d "$extract"
  install_script="$(find "$extract" -maxdepth 3 -type f -name install.sh | head -n 1)"
  [[ -n "$install_script" ]] || fail "install.sh not found inside release zip."
  install_dir="${install_script:h}"

  say "Running bundled installer from $install_dir"
  ( cd "$install_dir" && ./install.sh "${INSTALL_ARGS[@]}" )
}

main "$@"
