# payment-service

Layanan **pembayaran** untuk Rusuh Booking Tiket (**BE-2**). Service membuat transaksi Midtrans Snap, mengelola status pembayaran dan TTL, memproses callback Midtrans secara idempoten, lalu menerbitkan event pembayaran ke Kafka.

- Arsitektur: Hexagonal (Ports & Adapters), `net/http` + PostgreSQL + Kafka + Midtrans
- Port lokal: **8083**
- Endpoint booking/payment memerlukan `X-Payment-Token` dari booking-service
- Callback Midtrans diverifikasi menggunakan signature key

## Prasyarat

- Go 1.26+
- PostgreSQL dan Kafka dari repo `tools`
- Midtrans Sandbox untuk mencoba alur charge end-to-end
- `PAYMENT_TOKEN_SECRET` sama dengan secret yang dipakai booking-service

## Menjalankan

```bash
cd ../tools
make up
```

Lalu jalankan service:

```bash
cd ../payment-service
APP_ENV=local APP_PORT=8083 DB_PASSWORD=password \
KAFKA_BROKERS=localhost:29092 \
PAYMENT_TOKEN_SECRET=dev-payment-token-secret \
ACCESS_TOKEN_SECRET=dev-access-token-secret \
OTEL_ENABLED=false make run
```

Untuk menjalankan charge dengan Midtrans Sandbox, tambahkan `MIDTRANS_SERVER_KEY` dan `MIDTRANS_SIGNATURE_KEY` dari konfigurasi sandbox. Jangan simpan credential sandbox atau production di repository. Compose lokal memakai PostgreSQL `localhost:5432` (`rusuh_booking`, user `postgres`, password `password`) dan Kafka `localhost:29092`.

## Endpoint

| Method | Path | Akses | Keterangan |
|---|---|---|---|
| GET | `/api/payment/v1/booking/{bookingId}` | `X-Payment-Token` | Detail booking dan status pembayaran |
| POST | `/api/payment/v1/charge` | `X-Payment-Token` | Membuat/mengambil transaksi Midtrans Snap untuk booking |
| POST | `/api/payment/v1/callback` | Midtrans | Webhook; signature diverifikasi oleh service |
| GET | `/sys/ping` | Publik | Health check |

Contoh membuat transaksi:

```bash
curl -X POST http://localhost:8083/api/payment/v1/charge \
  -H 'X-Payment-Token: <payment-token-dari-booking>' \
  -H 'Content-Type: application/json' \
  -d '{"bookingId":"<booking-uuid>"}'
```

Respons charge berisi `paymentId`, status, dan `snapToken`/`redirectUrl` bila transaksi berhasil dibuat. Midtrans mengirim callback ke `/api/payment/v1/callback`; frontend tidak boleh memanggil endpoint callback.

## Konfigurasi

| Env | Default lokal | Keterangan |
|---|---|---|
| `APP_ENV` / `APP_PORT` | `local` / `8083` | Cloud Run dapat mengisi `PORT` |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | Logging terstruktur |
| `DB_HOST` / `DB_PORT` | `localhost` / `5432` | PostgreSQL bersama |
| `DB_USER` / `DB_PASSWORD` / `DB_NAME` | `postgres` / kosong / `rusuh_booking` | Compose lokal: password `password` |
| `DB_SSL_MODE` | `disable` | SSL mode PostgreSQL |
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` | `25` / `10` | Pool koneksi |
| `DB_CONN_TIMEOUT_SECONDS` | `5` | Timeout koneksi |
| `VALKEY_ADDR` / `VALKEY_PASSWORD` | `localhost:6379` / kosong | Cache redirect URL; bila Valkey gagal, service tetap berjalan tanpa menyimpan redirect URL |
| `VALKEY_TLS` | `false` | TLS Valkey |
| `KAFKA_BROKERS` | `localhost:9092` | Compose lokal: `localhost:29092` |
| `KAFKA_USERNAME` / `KAFKA_PASSWORD` / `KAFKA_TLS` | kosong / kosong / `false` | Opsi autentikasi broker |
| `PAYMENT_TOKEN_SECRET` | wajib diisi | Validasi token pembayaran dari booking-service |
| `ACCESS_TOKEN_SECRET` | kosong | Secret access token jika middleware opsional-auth digunakan |
| `MIDTRANS_SERVER_KEY` | kosong | Server key Midtrans; gunakan key sandbox di lokal |
| `MIDTRANS_SIGNATURE_KEY` | fallback ke server key | Secret untuk verifikasi callback Midtrans |
| `MIDTRANS_ENV` | `sandbox` | `sandbox` atau `production` |
| `OTEL_ENABLED` | aktif | Set `false` untuk menonaktifkan ekspor Cloud Trace saat lokal |
| `OTEL_SAMPLE_RATIO` | `1.0` | Rasio trace yang disimpan, antara `0` dan `1` |

## Pengembangan

```bash
make test
make build
make golint
make scan       # gosec
make vuln       # govulncheck
```

Skema database ada di `migrations/`; database lokal bersama diinisialisasi oleh `tools/docker/postgres/init` saat volume PostgreSQL pertama kali dibuat.
