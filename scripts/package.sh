#!/usr/bin/env bash
# Build a distribution zip: wx-mcp + wxkey binaries + local libWCDB.dylib +
# install.sh + README. Friend/agent解压后跑
# `./install.sh --all --yes --json` 即可完成安装和 MCP 注册.
# 前提: 若目标机器没有现成 key, 推荐先跑 ./wxkey bootstrap; 它会走 no-SIP
# 的 ad-hoc 重签路线完成首次 key 初始化. 已预先写好 ~/.config/wxcli/config.json
# 时, wx-mcp 运行时解密不要求关闭 SIP.
set -euo pipefail

VERSION="${1:-1.0.0}"
SRCDIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SRCDIR"

DYLIB_SRC="${WX_MCP_WCDB_DYLIB:-$SRCDIR/lib/libWCDB.dylib}"
if [[ ! -f "$DYLIB_SRC" ]]; then
  echo "ERROR: libWCDB.dylib missing — set WX_MCP_WCDB_DYLIB or place it at $SRCDIR/lib/libWCDB.dylib" >&2
  exit 1
fi

WXKEY_SRC="${WXKEY_SRC:-$HOME/cc-workspace/mcp-servers/wxkey}"

DIST="$SRCDIR/dist/wx-mcp-v${VERSION}-darwin-arm64"
rm -rf "$DIST" && mkdir -p "$DIST"

echo "→ building wx-mcp binary..."
go build -o "$DIST/wx-mcp" ./cmd/wx-mcp
chmod +x "$DIST/wx-mcp"

echo "→ building wxkey binary..."
if [[ -d "$WXKEY_SRC" ]]; then
  ( cd "$WXKEY_SRC" && go build -o "$DIST/wxkey" ./cmd/wxkey )
else
  GOBIN="$DIST" go install github.com/r266-tech/wxkey/cmd/wxkey@latest
fi
chmod +x "$DIST/wxkey"

echo "→ bundling libWCDB.dylib ($(du -h "$DYLIB_SRC" | cut -f1))..."
cp "$DYLIB_SRC" "$DIST/libWCDB.dylib"

echo "→ copying README..."
cp README.md "$DIST/"

echo "→ copying installer..."
cp install.sh "$DIST/"
chmod +x "$DIST/install.sh"

echo "→ zipping..."
cd dist
zip -qr "wx-mcp-v${VERSION}-darwin-arm64.zip" "wx-mcp-v${VERSION}-darwin-arm64"

echo
echo "✓ dist/wx-mcp-v${VERSION}-darwin-arm64.zip"
ls -lh "wx-mcp-v${VERSION}-darwin-arm64.zip"
