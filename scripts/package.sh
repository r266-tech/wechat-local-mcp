#!/usr/bin/env bash
# Build a distribution zip: wechat-cli + wxkey binaries + local libWCDB.dylib +
# install.sh + one-line bootstrap helper + docs. Friend/agent解压后跑
# `./install.sh --all --yes --json` 即可完成 CLI 安装和 key/cache 初始化.
# 如需兼容旧 MCP 客户端, 显式追加 `--mcp` 注册 `wechat-cli serve-mcp`.
# 前提: 若目标机器没有现成 schema-2 key map, ./install.sh --all 会跑
# ./wxkey bootstrap; 它会走 no-SIP + Keychain sudo + ad-hoc 重签路线完成首次
# key 初始化. wechat-cli 运行时解密不要求关闭 SIP.
set -euo pipefail

VERSION="${1:-1.0.0}"
SRCDIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SRCDIR"

DYLIB_SRC="${WECHAT_CLI_WCDB_DYLIB:-${WX_MCP_WCDB_DYLIB:-$SRCDIR/lib/libWCDB.dylib}}"
if [[ ! -f "$DYLIB_SRC" ]]; then
  echo "ERROR: libWCDB.dylib missing — set WECHAT_CLI_WCDB_DYLIB or place it at $SRCDIR/lib/libWCDB.dylib" >&2
  exit 1
fi

WXKEY_SRC="${WXKEY_SRC:-$SRCDIR/../wxkey}"
WXKEY_GO_INSTALL="${WXKEY_GO_INSTALL:-github.com/r266-tech/wxkey/cmd/wxkey@latest}"

DIST="$SRCDIR/dist/wechat-cli-v${VERSION}-darwin-arm64"
rm -rf "$DIST" && mkdir -p "$DIST"

echo "→ building wechat-cli binary..."
# -trimpath strips build-host absolute paths from the binary; -ldflags "-s -w"
# strips symbol/debug tables so release binaries do not leak the build
# environment (e.g. /Users/<dev>/... or Go module cache locations).
go build -trimpath -ldflags="-s -w" -o "$DIST/wechat-cli" ./cmd/wx-mcp
chmod +x "$DIST/wechat-cli"

echo "→ building wxkey binary..."
if [[ -d "$WXKEY_SRC" ]]; then
  ( cd "$WXKEY_SRC" && go build -trimpath -ldflags="-s -w" -o "$DIST/wxkey" ./cmd/wxkey )
else
  GOFLAGS="-trimpath -ldflags=-s -w" GOBIN="$DIST" go install "$WXKEY_GO_INSTALL"
fi
chmod +x "$DIST/wxkey"

echo "→ bundling libWCDB.dylib ($(du -h "$DYLIB_SRC" | cut -f1))..."
cp "$DYLIB_SRC" "$DIST/libWCDB.dylib"

echo "→ copying docs..."
cp README.md llms.txt LICENSE SECURITY.md THIRD_PARTY_NOTICES.md AGENTS.md mcp-server.json "$DIST/"
mkdir -p "$DIST/scripts"
cp scripts/install-release.sh "$DIST/scripts/"

echo "→ copying installer..."
cp install.sh "$DIST/"
chmod +x "$DIST/install.sh"

echo "→ zipping..."
cd dist
zip -qr "wechat-cli-v${VERSION}-darwin-arm64.zip" "wechat-cli-v${VERSION}-darwin-arm64"
shasum -a 256 "wechat-cli-v${VERSION}-darwin-arm64.zip" > "wechat-cli-v${VERSION}-darwin-arm64.zip.sha256"
cp "wechat-cli-v${VERSION}-darwin-arm64.zip" "wechat-cli-latest-darwin-arm64.zip"
shasum -a 256 "wechat-cli-latest-darwin-arm64.zip" > "wechat-cli-latest-darwin-arm64.zip.sha256"

echo
echo "✓ dist/wechat-cli-v${VERSION}-darwin-arm64.zip"
ls -lh "wechat-cli-v${VERSION}-darwin-arm64.zip"
echo "✓ dist/wechat-cli-v${VERSION}-darwin-arm64.zip.sha256"
echo "✓ dist/wechat-cli-latest-darwin-arm64.zip"
