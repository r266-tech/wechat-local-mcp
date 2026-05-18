#!/usr/bin/env bash
# Build a distribution zip: wx-mcp + wxkey binaries + local libWCDB.dylib +
# install.sh + docs. Friend/agent解压后跑
# `./install.sh --all --yes --json` 即可完成安装和 MCP 注册.
# 前提: 若目标机器没有现成 schema-2 key map, ./install.sh --all 会跑
# ./wxkey bootstrap; 它会走 no-SIP + Keychain sudo + ad-hoc 重签路线完成首次
# key 初始化. wx-mcp 运行时解密不要求关闭 SIP.
set -euo pipefail

VERSION="${1:-1.0.0}"
SRCDIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SRCDIR"

DYLIB_SRC="${WX_MCP_WCDB_DYLIB:-$SRCDIR/lib/libWCDB.dylib}"
if [[ ! -f "$DYLIB_SRC" ]]; then
  echo "ERROR: libWCDB.dylib missing — set WX_MCP_WCDB_DYLIB or place it at $SRCDIR/lib/libWCDB.dylib" >&2
  exit 1
fi

WXKEY_SRC="${WXKEY_SRC:-$SRCDIR/../wxkey}"
WXKEY_GO_INSTALL="${WXKEY_GO_INSTALL:-github.com/r266-tech/wxkey/cmd/wxkey@latest}"

DIST="$SRCDIR/dist/wx-mcp-v${VERSION}-darwin-arm64"
rm -rf "$DIST" && mkdir -p "$DIST"

echo "→ building wx-mcp binary..."
# -trimpath strips build-host absolute paths from the binary; -ldflags "-s -w"
# strips symbol/debug tables so release binaries do not leak the build
# environment (e.g. /Users/<dev>/... or Go module cache locations).
go build -trimpath -ldflags="-s -w" -o "$DIST/wx-mcp" ./cmd/wx-mcp
chmod +x "$DIST/wx-mcp"

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

echo "→ copying installer..."
cp install.sh "$DIST/"
chmod +x "$DIST/install.sh"

echo "→ zipping..."
cd dist
zip -qr "wx-mcp-v${VERSION}-darwin-arm64.zip" "wx-mcp-v${VERSION}-darwin-arm64"
shasum -a 256 "wx-mcp-v${VERSION}-darwin-arm64.zip" > "wx-mcp-v${VERSION}-darwin-arm64.zip.sha256"
cp "wx-mcp-v${VERSION}-darwin-arm64.zip" "wx-mcp-latest-darwin-arm64.zip"
shasum -a 256 "wx-mcp-latest-darwin-arm64.zip" > "wx-mcp-latest-darwin-arm64.zip.sha256"

echo
echo "✓ dist/wx-mcp-v${VERSION}-darwin-arm64.zip"
ls -lh "wx-mcp-v${VERSION}-darwin-arm64.zip"
echo "✓ dist/wx-mcp-v${VERSION}-darwin-arm64.zip.sha256"
echo "✓ dist/wx-mcp-latest-darwin-arm64.zip"
