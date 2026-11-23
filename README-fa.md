# موتورهای وارپ واتاشی (`v1.2.6-ws2`)

[![English](https://img.shields.io/badge/README-English-blue)](README.md)

این مخزن هر دو موتور Cloudflare WARP مورد استفاده در **Watashi Manager** را می‌سازد و منتشر می‌کند:

1. **`warp-plus`** — ساخته شده از تگ رسمی [`bepass-org/warp-plus`](https://github.com/bepass-org/warp-plus) (`v1.2.6`) با اصلاح نشت رم، goroutine، UDP و gVisor netstack.
2. **`watashi-masque`** — موتور جدید WARP روی **MASQUE (HTTP/3)** ساخته شده از تگ رسمی [`Diniboy1123/usque`](https://github.com/Diniboy1123/usque) (`v4.2.1`) همراه با زیرفرمان `watashi`.

هر دو موتور مستقیماً از کد رسمی بالادست و Go رسمی (بدون هیچ میرور واسطه) ساخته، تست و برای معماری‌های `linux/amd64` و `linux/arm64` در Releases منتشر می‌شوند.

---

## چه چیزهایی در `warp-plus` درست شد

| مشکل | فایل | issue در ریپوی اصلی |
|---|---|---|
| **نشت اصلی رم:** وقتی برنامه اتصال را می‌بست، رله منتظر سرور مقصد می‌ماند و اگر سرور ساکت بود، تا ابد منتظر می‌ماند. هر اتصال این‌طوری دو goroutine، دو بافر ۶۴ کیلوبایتی و یک اتصال TCP داخل gVisor را برای همیشه نگه می‌داشت. | `wiresocks/relay.go`، `wiresocks/proxy.go` | #43، #177، #237 |
| بافرهای TCP داخل gVisor برای هر اتصال ۱ مگابایت بودند و کوچک نمی‌شدند. حالا از ۱۲۸ کیلوبایت شروع می‌شوند و فقط وقتی برنامه واقعاً سریع می‌خواند، تا ۲ مگابایت بزرگ می‌شوند. | `patch-netstack.py` | #43 |
| اتصال UDP اگر هیچ بسته‌ای نمی‌گرفت، برای همیشه باز می‌ماند و روی آدرسی bind می‌شد که خطای `cannot assign requested address` می‌داد. | `socks5/server.go` | #95، #206، #265 |
| بستهٔ UDP زیر بار خراب می‌شد (چند goroutine از یک بافر مشترک استفاده می‌کردند) و مقصدهای دامنه‌ای به شکل `:443` شماره‌گیری می‌شدند. | `socks5/common.go` | — |
| فورواردر حالت gool با cancel متوقف نمی‌شد و روی سوکت بسته‌شده یک هستهٔ CPU را صددرصد مشغول می‌کرد. | `wiresocks/udpfw.go` | — |
| تست اتصال در هر تلاش یک transport جدید می‌ساخت و body پاسخ را نمی‌بست؛ تلاش ناموفق هم دستگاه وایرگارد را روشن جا می‌گذاشت. | `app/wg.go`، `app/app.go` | #242، #277 |
| خطای Accept بی‌وقفه تکرار می‌شد و ژورنال را پر می‌کرد. | `mixed/proxy.go`، `socks5/server.go` | — |

---

## ساخت مستقیم روی سرور لینوکس

### ساخت و نصب `warp-plus`
```bash
bash build.sh --install
```

### ساخت و نصب `watashi-masque` (HTTP/3)
```bash
bash build-masque.sh --install
```

اجرای هر اسکریپت بدون `--install` فایل‌های زیپ `amd64` و `arm64` را همراه با `SHA256SUMS` داخل پوشهٔ `./dist` می‌سازد.

---

## انتشار نسخهٔ جدید از طریق GitHub Actions

با زدن یک تگ جدید، GitHub Actions به‌صورت خودکار هر دو موتور را می‌سازد، تست می‌کند و همهٔ فایل‌های زیپ را همراه `SHA256SUMS` در صفحهٔ Releases منتشر می‌کند:

```bash
git tag -a v1.2.6-ws2 -m "v1.2.6-ws2"
git push origin v1.2.6-ws2
```

---

## بررسی وضعیت روی سرور Watashi Manager

این نسخه هر ۱۰ دقیقه یک خط وضعیت در ژورنال می‌نویسد:

```bash
journalctl -u watashi-warp -o cat | grep 'watashi stats' | tail -6
```

اگر نشت برطرف شده باشد، عدد `goroutines` و `heap_inuse` زیر بار بالا و پایین می‌رود، ولی روز به روز بالاتر نمی‌رود. برای بررسی دقیق‌تر می‌توانید pprof را هم روشن کنید: `WARP_PLUS_PPROF=127.0.0.1:6061`.

---

## برگشت به نسخهٔ قبل

برای `warp-plus`:
```bash
d=/opt/watashi-manager/other/warp/warpplus
install -m 755 "$d/warp-plus.previous" "$d/warp-plus" && systemctl restart watashi-warp
```

برای `watashi-masque`:
```bash
d=/opt/watashi-manager/other/warp/warpplus
install -m 755 "$d/watashi-masque.previous" "$d/watashi-masque" && systemctl restart watashi-warp
```
