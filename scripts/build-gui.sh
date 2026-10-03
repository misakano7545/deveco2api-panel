#!/usr/bin/env bash
# build-gui.sh 构建内嵌 webview 版登录器（deveco2api-login-gui）。
#
# 为什么要这层壳：webview_go 的 cgo 指令写死 `pkg-config: gtk+-3.0 webkit2gtk-4.0`，
# 而 Debian 13 / Ubuntu 24.04+ 只提供 webkit2gtk-4.1（4.0 开发包已从仓库移除）。
# 这里在临时目录放一个同名 .pc 别名，实际链接的仍是 4.1 —— webview 用到的
# WebKitGTK API 在 4.1 上未变。macOS（WKWebView）/ Windows（WebView2）不需要这层。
#
# 用法：bash scripts/build-gui.sh [输出文件]
set -euo pipefail

out=${1:-deveco2api-login-gui}
cd "$(dirname "$0")/.."

shim=$(mktemp -d)
trap 'rm -rf "$shim"' EXIT

# 只有 Linux 需要这层：macOS（WKWebView）/ Windows（WebView2）的 webview_go 不查 webkit2gtk
if [ "$(uname -s)" = "Linux" ] && ! pkg-config --exists webkit2gtk-4.0; then
  pc41=$(pkg-config --variable=pcfiledir webkit2gtk-4.1 2>/dev/null || true)
  if [ -z "$pc41" ]; then
    echo "缺少 WebKitGTK 开发库：apt-get install -y libwebkit2gtk-4.1-dev libgtk-3-dev" >&2
    exit 1
  fi
  cp "$pc41/webkit2gtk-4.1.pc" "$shim/webkit2gtk-4.0.pc"
  export PKG_CONFIG_PATH="$shim${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
  echo "使用 webkit2gtk-4.1 作为 4.0 的 pkg-config 别名（$shim）"
fi

CGO_ENABLED=1 go build -tags webview -trimpath -ldflags="-s -w" -o "$out" ./cmd/login-gui
echo "built $out"
