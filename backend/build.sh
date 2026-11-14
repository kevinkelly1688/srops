#!/usr/bin/env bash
# 打包发布：bash build.sh [linux-amd64|linux-arm64|windows-amd64]（默认 linux-amd64）
# 产物：dist/sr-ops-<版本>-<平台>.tar.gz，内含服务端二进制、前端 web/、systemd 单元与说明。
set -euo pipefail
cd "$(dirname "$0")"
ROOT=..
VER=$(tr -d 'v\r\n' < "$ROOT/VERSION")
TARGET=${1:-linux-amd64}
GOOS=${TARGET%-*}; GOARCH=${TARGET#*-}
EXT=""; [ "$GOOS" = windows ] && EXT=".exe"
OUT="dist/sr-ops-$VER-$TARGET"

export GOTOOLCHAIN=local   # 避免受全局 GOTOOLCHAIN 配置影响
python "$ROOT/tools/build_help.py" >/dev/null   # 刷新手册打包与资源版本号
go vet ./...
go test -count=1 ./...

rm -rf "$OUT" && mkdir -p "$OUT/web/docs"
CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -trimpath -ldflags "-s -w -X main.version=$VER" -o "$OUT/sr-ops$EXT" ./cmd/sr-ops
cp -r "$ROOT/index.html" "$ROOT/css" "$ROOT/js" "$OUT/web/"
cp "$ROOT/docs/用户使用手册.md" "$OUT/web/docs/"
cp deploy/sr-ops.service deploy/install.sh README.md "$OUT/"
chmod +x "$OUT/install.sh"
echo "v$VER" > "$OUT/VERSION"
tar -czf "$OUT.tar.gz" -C dist "$(basename "$OUT")"
sha256sum "$OUT.tar.gz" > "$OUT.tar.gz.sha256"
echo "已生成 $OUT.tar.gz"
