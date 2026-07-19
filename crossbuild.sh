#!/bin/sh
# crossbuild.sh - 在当前平台交叉编译 wstunnel 到 linux-amd64 / windows-amd64 / darwin-arm64。
#
# 用法:
#   ./crossbuild.sh              # 编译三个平台
#   ./crossbuild.sh clean        # 清理 binaries/
#
# 无需 gox 等外部工具，纯 Go 原生 GOOS/GOARCH 交叉编译。
# 版本号通过 ldflags 从 git 描述注入到 main.version。

set -e

TARGETS="
linux/amd64
windows/amd64
darwin/arm64
"

# 获取版本号：优先 git tag，其次 git short hash，最后 dev
if command -v git >/dev/null 2>&1; then
    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
else
    VERSION=dev
fi
LDFLAGS="-s -w -X main.version=${VERSION}"

OUT_DIR="binaries"
mkdir -p "$OUT_DIR"

if [ "$1" = "clean" ]; then
    rm -rf "$OUT_DIR"
    echo "cleaned $OUT_DIR"
    exit 0
fi

echo "== building wstunnel ${VERSION} =="
printf "%s\n" "$TARGETS" | grep -v '^$' | while IFS=/ read -r GOOS GOARCH; do
    [ -z "$GOOS" ] && continue
    # 扩展名: Windows 用 .exe，其余无
    case "$GOOS" in
        windows) EXT=".exe" ;;
        *)       EXT="" ;;
    esac
    OUT="${OUT_DIR}/wstunnel-${GOOS}-${GOARCH}${EXT}"

    echo "--> ${GOOS}/${GOARCH} -> ${OUT}"
    GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .

    # 打印文件大小
    if command -v du >/dev/null 2>&1; then
        SIZE=$(du -h "$OUT" | cut -f1)
        echo "    size: ${SIZE}"
    fi
done

echo "== done. artifacts in ${OUT_DIR}/ =="
ls -1 "$OUT_DIR" 2>/dev/null
