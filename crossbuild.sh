#!/bin/sh
# crossbuild.sh - 交叉编译 wstunnel 到 6 个平台目标。
#
# 用法:
#   ./crossbuild.sh              # 编译全部目标
#   ./crossbuild.sh clean        # 清理 binaries/
#
# 无需 gox 等外部工具，纯 Go 原生 GOOS/GOARCH 交叉编译。
# 版本号通过 ldflags 从 git 描述注入到 main.version。
# 目标行格式: GOOS/GOARCH[/输出名/GOARM]；armhf = 32 位 ARM，GOARM=6（兼容 ARMv6/v7，如树莓派）。

set -e

TARGETS="
linux/amd64
linux/arm64
linux/arm/armhf/6
windows/amd64
darwin/arm64
darwin/amd64
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
printf "%s\n" "$TARGETS" | grep -v '^$' | while IFS=/ read -r GOOS GOARCH NAME GOARM; do
    [ -z "$GOOS" ] && continue
    # 扩展名: Windows 用 .exe，其余无；输出名: 第三段（armhf），缺省用 GOARCH
    case "$GOOS" in
        windows) EXT=".exe" ;;
        *)       EXT="" ;;
    esac
    NAME="${NAME:-$GOARCH}"
    OUT="${OUT_DIR}/wstunnel-${GOOS}-${NAME}${EXT}"

    echo "--> ${GOOS}/${GOARCH}${GOARM:+ (GOARM=$GOARM)} -> ${OUT}"
    if [ -n "$GOARM" ]; then
        GOOS="$GOOS" GOARCH="$GOARCH" GOARM="$GOARM" CGO_ENABLED=0 \
            go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .
    else
        GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
            go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .
    fi

    # 打印文件大小
    if command -v du >/dev/null 2>&1; then
        SIZE=$(du -h "$OUT" | cut -f1)
        echo "    size: ${SIZE}"
    fi
done

echo "== done. artifacts in ${OUT_DIR}/ =="
ls -1 "$OUT_DIR" 2>/dev/null
