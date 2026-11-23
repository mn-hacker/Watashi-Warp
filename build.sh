#!/bin/bash
# watashi warp-plus v1.2.6-ws1 - builds the fixed engine from upstream v1.2.6.
#
# usage (root, on any Linux box with internet):
#   bash build.sh              build, vet, test, and leave the zips in ./dist
#   bash build.sh --install    the same, then put the binary for this machine
#                              into the panel and restart WARP
#
# Nothing is downloaded from a mirror: the source comes from the upstream tag,
# Go from go.dev, modules from the Go module proxy, all over https.
set -euo pipefail
cd "$(dirname -- "$0")"
KIT=$(pwd)

UPSTREAM_TAG=${UPSTREAM_TAG:-v1.2.6}
WS_VERSION=${WS_VERSION:-v1.2.6-ws2}
GO_VERSION=${WS_GO_VERSION:-go1.22.12}
WORK=${WS_WORK:-/opt/ws-build/warp-plus}
GOROOT_DIR=${WS_GOROOT:-/opt/ws-build/go}
DIST="$KIT/dist"
INSTALL=0
[ "${1:-}" = "--install" ] && INSTALL=1

say() { printf '\n== %s\n' "$*"; }
die() { printf '\n!! %s\n' "$*" >&2; exit 1; }

host_arch() {
    case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "unsupported machine: $(uname -m)" ;;
    esac
}

# ------------------------------------------------------------------ Go
say "Go $GO_VERSION"
if [ ! -x "$GOROOT_DIR/bin/go" ] || [ "$("$GOROOT_DIR/bin/go" env GOVERSION 2>/dev/null)" != "$GO_VERSION" ]; then
    rm -rf "$GOROOT_DIR"
    mkdir -p "$(dirname "$GOROOT_DIR")"
    tgz="/tmp/$GO_VERSION.linux-$(host_arch).tar.gz"
    curl -fL --retry 3 -o "$tgz" "https://go.dev/dl/$GO_VERSION.linux-$(host_arch).tar.gz"
    tar -C "$(dirname "$GOROOT_DIR")" -xzf "$tgz"
    [ "$(dirname "$GOROOT_DIR")/go" = "$GOROOT_DIR" ] || mv "$(dirname "$GOROOT_DIR")/go" "$GOROOT_DIR"
    rm -f "$tgz"
fi
export PATH="$GOROOT_DIR/bin:$PATH" GOTOOLCHAIN=local GOFLAGS=-mod=mod
go version

# ------------------------------------------------------------------ source
say "upstream warp-plus $UPSTREAM_TAG"
rm -rf "$WORK"
mkdir -p "$WORK"
curl -fL --retry 3 "https://github.com/bepass-org/warp-plus/archive/refs/tags/$UPSTREAM_TAG.tar.gz" |
    tar -xz -C "$WORK" --strip-components=1
cd "$WORK"

# the overlay was written against these exact lines; anything else means the
# base moved and the overlay must be looked at again before it is used
grep -q 'copyConnTimeout(conn, req.Conn, buf1, timeout)' wiresocks/proxy.go || die "wiresocks/proxy.go is not the $UPSTREAM_TAG the kit expects"
grep -q '<-cConn.frc' proxy/pkg/socks5/server.go || die "socks5/server.go is not the $UPSTREAM_TAG the kit expects"
grep -q 'func establishWireguard(.*) error {' app/wg.go || die "app/wg.go is not the $UPSTREAM_TAG the kit expects"
grep -q 'could not enable TCP SACK' wireguard/tun/netstack/tun.go || die "netstack/tun.go is not the $UPSTREAM_TAG the kit expects"

say "applying the watashi fixes"
cp -a "$KIT/overlay/." .
python3 "$KIT/patch-netstack.py" wireguard/tun/netstack/tun.go
gofmt -w wiresocks app proxy/pkg/socks5 proxy/pkg/mixed cmd/warp-plus wireguard/tun/netstack/tun.go

say "checks"
go mod download
go vet ./app/... ./wiresocks/... ./proxy/... ./cmd/... ./wireguard/tun/netstack/...
if command -v gcc >/dev/null 2>&1; then
    CGO_ENABLED=1 go test -race -count=1 ./wiresocks/... ./proxy/pkg/socks5/...
else
    echo "(no gcc here, so the race detector is skipped)"
    go test -count=1 ./wiresocks/... ./proxy/pkg/socks5/...
fi

# ------------------------------------------------------------------ build
say "building $WS_VERSION"
rm -rf "$DIST"
mkdir -p "$DIST"
for arch in amd64 arm64; do
    out="$WORK/out/$arch"
    mkdir -p "$out"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$WS_VERSION" -o "$out/warp-plus" ./cmd/warp-plus
    # python instead of zip: not every server has the zip command
    python3 - "$out/warp-plus" "$DIST/warp-plus_linux-$arch.zip" <<'PY'
import sys, zipfile
src, dst = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(dst, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as z:
    info = zipfile.ZipInfo('warp-plus')
    info.external_attr = 0o100755 << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    z.writestr(info, open(src, 'rb').read())
PY
done
cd "$DIST"
sha256sum warp-plus_linux-*.zip | tee SHA256SUMS

say "lines for common/packages.lock once the zips are published"
echo "replace OWNER/REPO with your repository:"
while read -r sum file; do
    arch=${file#warp-plus_linux-}
    arch=${arch%.zip}
    echo "warp-plus|${WS_VERSION#v}|$arch|https://github.com/OWNER/REPO/releases/download/$WS_VERSION/$file|$sum"
done <SHA256SUMS

# ------------------------------------------------------------------ install
if [ "$INSTALL" = 1 ]; then
    say "installing into the panel"
    dst=/opt/watashi-manager/other/warp/warpplus
    [ -d "$dst" ] || die "$dst does not exist"
    arch=$(host_arch)
    [ -f "$dst/warp-plus" ] && cp -f "$dst/warp-plus" "$dst/warp-plus.previous"
    install -m 755 "$WORK/out/$arch/warp-plus" "$dst/warp-plus"
    "$dst/warp-plus" version
    bash "$dst/run.sh" || true
    echo "to go back: install -m 755 $dst/warp-plus.previous $dst/warp-plus && systemctl restart watashi-warp"
fi
