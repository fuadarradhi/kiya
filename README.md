# Kiya Framework 🚀

Framework web Go yang aman, cepat, dan modular dengan fitur keamanan enterprise bawaan (WAF, Rate Limiting, Session Management, Pongo2 View Engine, dan Active Record ORM).

---

## 📚 Dokumentasi Berkas Framework

### 1. `kiya.go` — Entrypoint & Engine Constructor

Berkas **`kiya.go`** adalah pusat pengatur (*core constructor*) utama dari framework Kiya. Berkas ini menyediakan fungsi `kiya.New(opts ...Option) (*Router, error)` untuk menginisialisasi seluruh subsistem server HTTP.

#### 🛠️ Fitur & Subsistem yang Diinisialisasi di `kiya.go`:

| Subsistem | Komponen Internal | Deskripsi |
| :--- | :--- | :--- |
| **Config Validation** | `cfg.validate()` | Memvalidasi seluruh opsi masukan sebelum server dinyalakan. |
| **Logger** | `logger.Init()` | Sistem log dengan dukungan output JSON, log WAF, dan Notifikasi Telegram Alert. |
| **Database Pool** | `NewDatabase()` | Koneksi pool database (PostgreSQL, MySQL, SQLite). |
| **Template Renderer** | `web.NewRenderer()` | Inisialisasi engine template Pongo2 (Django/Jinja2-style). |
| **Session Management** | `sessionStore` | Opsi penyimpanan sesi berbasis **Cookie Store** atau **Redis Store**. |
| **Web App Firewall (WAF)** | `security.InitWAF()` | WAF Engine bawaan (Coraza) untuk perlindungan SQLi, XSS, dll. |
| **AES-256 Encryption** | `sha256(key)` | Kunci enkripsi AES-256-GCM untuk payload sensitif & CSRF token. |
| **Rate Limiter** | `rateLimiter` | Pembatas request per IP/Session (In-Memory atau Redis Backend). |
| **Memory Optimization** | `sync.Pool` | Resusabilitas objek `Context` HTTP untuk mengurangi beban Garbage Collection. |
| **Auto Health Check** | `/health` | Endpoint otomatis untuk monitoring kondisi server & database ping. |
| **Prometheus Metrics** | `/metrics` | Endpoint publikasi statistik performa server format Prometheus. |

#### 💡 Cara Penggunaan `kiya.New()`:
```go
app, err := kiya.New(
    kiya.WithAddr("0.0.0.0", 8080),
    kiya.WithDebug(true),
    kiya.WithCORS("*"),
    kiya.WithViews(views.FS),
)
```
