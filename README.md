# Watashi WARP Engines (`v1.2.6-ws2`)

[![فارسی](https://img.shields.io/badge/README-فارسی-blue)](README-fa.md)

This repository builds and publishes the two Cloudflare WARP engines used by **Watashi Manager**:

1. **`warp-plus`** — patched upstream [`bepass-org/warp-plus`](https://github.com/bepass-org/warp-plus) (`v1.2.6`) with memory-leak, goroutine, UDP, and gVisor netstack fixes.
2. **`watashi-masque`** — Cloudflare WARP over **MASQUE (HTTP/3)** built from upstream [`Diniboy1123/usque`](https://github.com/Diniboy1123/usque) (`v4.2.1`) with the `watashi` headless subcommand.

Both engines are built directly from official upstream tags and Go toolchains (no third-party mirrors), verified with `go vet` and unit tests, and published for `linux/amd64` and `linux/arm64` via GitHub Actions.

---

## What is fixed in `warp-plus`

| Issue | Files | Upstream Issues |
|---|---|---|
| **Primary memory leak:** When a client closed a connection, the relay waited indefinitely for the remote peer to close, leaking 2 goroutines, two 64 KB buffers, and a gVisor TCP socket per connection. | `wiresocks/relay.go`, `wiresocks/proxy.go` | #43, #177, #237 |
| **gVisor TCP buffer bloat:** TCP buffers started at 1 MB and never shrank, causing RAM spikes under speedtests. Now starts at 128 KB and scales up to 2 MB only when needed. | `patch-netstack.py` | #43 |
| **UDP leaks & bind errors:** Idle UDP associations stayed open forever, were not closed with their parent TCP session, and failed with `cannot assign requested address`. | `socks5/server.go` | #95, #206, #265 |
| **UDP buffer race & domain dialing:** Concurrent goroutines shared a single UDP buffer, and domain targets were dialed as `:443`. | `socks5/common.go` | — |
| **Gool UDP forwarder CPU spin:** Forwarder ignored context cancellation and spun a CPU core at 100% on closed sockets. | `wiresocks/udpfw.go` | — |
| **Health-check transport & WireGuard leak:** Connection tests created a new HTTP transport on every retry without closing response bodies, and failed attempts left WireGuard devices running. | `app/wg.go`, `app/app.go` | #242, #277 |
| **Accept error log flood:** Transient `Accept` errors looped tightly and flooded the systemd journal. | `mixed/proxy.go`, `socks5/server.go` | — |

---

## Building on a Linux server

### Build & install `warp-plus`
```bash
bash build.sh --install
```

### Build & install `watashi-masque` (HTTP/3)
```bash
bash build-masque.sh --install
```

Running either script without `--install` builds `linux/amd64` and `linux/arm64` archives and writes `SHA256SUMS` into `./dist`.

---

## Publishing a new release

Tag and push to trigger GitHub Actions, which builds both engines and publishes all archives together with a combined `SHA256SUMS`:

```bash
git tag -a v1.2.6-ws2 -m "v1.2.6-ws2"
git push origin v1.2.6-ws2
```

---

## Verifying on a Watashi Manager server

`warp-plus` logs a health summary every 10 minutes:

```bash
journalctl -u watashi-warp -o cat | grep 'watashi stats' | tail -6
```

For deeper profiling, set `WARP_PLUS_PPROF=127.0.0.1:6061`.

---

## Rolling back

```bash
d=/opt/watashi-manager/other/warp/warpplus
install -m 755 "$d/warp-plus.previous" "$d/warp-plus" && systemctl restart watashi-warp
```

For `watashi-masque`:

```bash
d=/opt/watashi-manager/other/warp/warpplus
install -m 755 "$d/watashi-masque.previous" "$d/watashi-masque" && systemctl restart watashi-warp
```
