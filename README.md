# Kiya Framework

Framework web Go yang aman, cepat, dan modular dengan fitur keamanan enterprise bawaan (WAF, Rate Limiting, Session Management, Pongo2 View Engine, dan Active Record ORM).

## 3 Pilar Utama Framework Kiya

1. **Keamanan Maksimal (OWASP Compliance Standard)**:
   - **WAF Engine (Coraza)** bawaan untuk proteksi otomatis SQLi, XSS, Path Traversal, Command Injection.
   - **Isolasi Session Scope (`WithSessionResolver`)**: Mengisolasi cookie session per rute/scope.
   - **Persistent HMAC Browser Cookie (`WithBrowserCookie`)**: Signature HMAC-SHA256 bound ke `User-Agent` untuk mencegah pencurian cookie session.
   - **Auto Anti-CSRF & Anti-Bot Honeypot**: Penanganan otomatis token CSRF dan jebakan bot spam.
2. **Performa High Concurrency (Ribuan Akses Bersamaan)**:
   - **Zero-Allocation Context Reuse (`sync.Pool`)**: Mengurangi beban alokasi memori GC secara dramatis pada traffic tinggi.
   - **Zero-Cron Real-time Aggregation**: Terintegrasi sempurna dengan Database Triggers & Active Record ORM.
   - **Fast Trie-Tree Routing**: Routing ultra-cepat berbasis Trie Tree dengan auto trailing-slash normalization.
3. **User-Friendly & Developer-Centric**:
   - API yang bersih, mudah digunakan (*developer-friendly*), serta menghasilkan pengalaman pengguna (*UX*) yang responsif & bersahabat.

---

## Dokumentasi Berkas Framework & Cara Penggunaan

### 1. `kiya.go` - Entrypoint & Engine Constructor

Berkas **`kiya.go`** adalah pusat pengatur (*core constructor*) utama dari framework Kiya. Berkas ini menyediakan fungsi `kiya.New(opts ...Option) (*Router, error)` untuk menginisialisasi seluruh subsistem server HTTP.

#### Fitur & Subsistem yang Diinisialisasi di `kiya.go`:

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
| **SMTP Mailer** | `kiya.SendMail()` | Utilitas pengirim email generik dengan dukungan template HTML. |

#### Cara Inisialisasi Engine:
```go
app, err := kiya.New(
    kiya.WithAddr("0.0.0.0", 8080),
    kiya.WithDebug(true),
    kiya.WithCORS("*"),
    kiya.WithViews(views.FS),
    kiya.WithEncryption("secret-key-32-bytes"),
    kiya.WithSession("session-secret-key"),
    // Dynamic Split Session Isolation (misal memisahkan session Admin /manage dan Publik /)
    kiya.WithSessionResolver(func(r *http.Request) (name string, path string) {
        if strings.HasPrefix(r.URL.Path, "/manage") {
            return "app_manage_sess", "/manage"
        }
        return "app_site_sess", "/"
    }),
    // Persistent Browser Cookie (30 hari, HMAC signature bound ke User-Agent)
    kiya.WithBrowserCookie("browser"),
    kiya.WithCSRF(),
    kiya.WithoutCSRF("/api"), // Pengecualian rute dari CSRF protection
)

// Mengakses Browser Tracking di Handler:
// browserID := c.BrowserID()     -> Menghasilkan string unique browser ID (HMAC-signed)
// isValid   := c.ValidBrowser()   -> bool: true jika cookie dari browser yang sama (un-tampered & UA cocok)
```

---

### 2. Berkas `models.go` & `history.go` - Active Record & Audit Trail History (`BaseModel`)

Fitur **`BaseModel`** di Kiya membuat struct Go bertindak sebagai ORM Active Record dengan query builder terintegrasi (`.WhereEq()`, `.Find()`, `.Insert()`, `.Update()`, `.Delete()`, `.Purge()`).

#### Aturan Field Standar pada `BaseModel` & Tabel Database:

Secara standar, setiap model database **WAJIB memuat seluruh field audit & lifecycle berikut** (kecuali secara eksplisit disebutkan tidak perlu):

| Field Go | Tag Struct | Tipe Data SQL | Peran / Fitur Otomatis Kiya |
| :--- | :--- | :--- | :--- |
| `kiya.BaseModel` | `table:"nama_tabel"` | - | **WAJIB**: Embedding BaseModel + Tag Nama Tabel |
| `ID` | `db:"id"` | `BIGINT PRIMARY KEY` | **WAJIB**: Primary Key Integer |
| `CreatedAt` | `db:"created_at"` | `TIMESTAMP` | Auto Timestamp pembuatan baris |
| `CreatedBy` | `db:"created_by"` | `VARCHAR(100)` | Auto Actor name/ID saat `.Insert()` |
| `UpdatedAt` | `db:"updated_at"` | `TIMESTAMP` | Auto Timestamp update terakhir |
| `ModifiedBy` | `db:"modified_by"` | `VARCHAR(100)` | Auto Actor name/ID saat `.Update()` |
| `DeletedAt` | `db:"deleted_at"` | `TIMESTAMP NULL` | Auto Soft Delete saat `.Delete()` |
| `DeletedBy` | `db:"deleted_by"` | `VARCHAR(100)` | Auto Actor name/ID saat `.Delete()` |
| `-` | `db:"active"` | `VIRTUAL GENERATED` | Helper Unique Constraint (`CASE WHEN deleted_at IS NULL THEN 1 ELSE NULL END`) |
| `History` | `db:"history"` | `TEXT / JSON` | Auto Audit Trail Log JSON Array (`created`, `modified`, `deleted`, `restored`, delta `changes`) |

#### Contoh Struktur Model Standar Lengkap:
```go
package models

import (
    "time"
    "github.com/fuadarradhi/kiya"
)

type Siswa struct {
    kiya.BaseModel `table:"siswa"` // WAJIB 1: Embed BaseModel + Tag Table

    ID          int64  `db:"id"`   // WAJIB 2: Primary Key Integer
    NISN        string `db:"nisn" validate:"required"`
    NamaLengkap string `db:"nama_lengkap" validate:"required"`

    // Field Audit Actor & History (Otomatis Diisi Kiya)
    CreatedBy  string `db:"created_by"`  // Auto actor name saat Insert
    ModifiedBy string `db:"modified_by"` // Auto actor name saat Update
    DeletedBy  string `db:"deleted_by"`  // Auto actor name saat Delete
    History    string `db:"history"`     // Auto Audit Trail JSON Array

    // Timestamps & Soft Delete
    CreatedAt time.Time  `db:"created_at"` // Auto Timestamp
    UpdatedAt time.Time  `db:"updated_at"` // Auto Timestamp
    DeletedAt *time.Time `db:"deleted_at"` // Auto Soft Delete
}
```

---

### 3. Panduan Penggunaan Fitur Framework (`Context`)

#### A. AES-256 URL ID Encryption & Decryption (`c.EncryptID` & `c.DecryptID`)
```go
// Mengenkripsi ID int64 -> URL Safe Encrypted String
encryptedString, err := c.EncryptID(42)

// Otomatis membaca ?id= dari URL, mendekripsi, dan mengembalikan int64
id, err := c.DecryptID()
```

#### B. Struct Payload Validation (`c.Validator`)
```go
type LoginRequest struct {
    Email    string `json:"email" validate:"required,email"`
    Password string `json:"password" validate:"required"`
}

func LoginHandler(c *kiya.Context) error {
    var req LoginRequest
    if err := c.BindJSON(&req); err != nil {
        return c.APIResponse(400, "Payload JSON tidak valid", nil, nil)
    }

    v := c.Validator(&req)
    if err := v.Validate(); err != nil {
        return v.Errors()
    }

    return c.APIResponse(200, "Login Berhasil", nil, req)
}
```

#### C. Render HTML Template Pongo2 (`c.Render`)
```go
func HomeHandler(c *kiya.Context) error {
    return c.Render(200, "site/home.html", kiya.Map{
        "title": "Halaman Utama",
    })
}
```

#### D. Otomatisasi CSRF Injection (`InjectCSRFIntoForms` & `InjectCSRFMeta`)
Ketika `WithCSRF()` aktif, Kiya secara otomatis melakukan hal berikut pada setiap respon HTML:
1. **Auto-Inject Input Hidden Form**: Setiap tag `<form method="POST">` (atau PUT/DELETE/PATCH) akan otomatis disisipkan `<input type="hidden" name="csrf_token" value="...">` tanpa perlu menulis tag input manual.
2. **Auto-Inject Meta Tag Header**: Tag `<head>` akan otomatis disisipkan `<meta name="csrf-token" content="...">` untuk memudahkan request AJAX/Fetch JavaScript.
3. **Template Variable**: Variabel `csrf_token` juga otomatis tersedia di template Pongo2 (`{{ csrf_token }}`).

#### E. Otomatisasi Anti-Bot Honeypot (`WithHoneypot` & `WithoutHoneypot`)
Ketika `WithHoneypot()` aktif (secara bawaan menggunakan field name `honeypot_token`):
1. **Auto-Inject Input Tersembunyi**: Setiap tag `<form method="POST">` (atau PUT/DELETE/PATCH) akan otomatis disisipkan `<input type="text" name="honeypot_token" value="" style="display:none !important;" tabindex="-1" autocomplete="off" aria-hidden="true">`.
2. **Auto-Detect Spam Bot**: Pengunjung manusia tidak akan melihat maupun mengisi input tersembunyi ini. Jika ada spam bot otomatis yang mengisi field `honeypot_token`, Kiya secara otomatis menolak request tersebut (`400 Bad Request: Spam submission detected`) sebelum masuk ke handler aplikasi.
3. **Pengecualian Rute**: Gunakan `WithoutHoneypot("/api")` untuk mengecualikan endpoint REST API atau Webhook dari pemeriksaan Honeypot.

#### F. CSRF untuk API JSON/SPA (`c.GenerateCSRFToken` & `c.VerifyCSRFToken`)
`InjectCSRFIntoForms`/`WithCSRF()` (poin D) hanya berlaku untuk HTML yang dirender Pongo2 - tidak ada `<form>` untuk disisipi pada backend JSON murni (SPA yang fetch/XHR). Untuk kasus ini, `Context` membungkus `security.GenerateCSRFToken`/`VerifyCSRFToken` secara langsung:
```go
// Endpoint bootstrap, dipanggil sekali oleh SPA sebelum request mutating pertama.
// Tidak butuh login - kiya membuat session baru untuk visitor anonim juga.
func CSRFTokenHandler(c *kiya.Context) error {
    token, err := c.GenerateCSRFToken()
    if err != nil {
        return c.JSON(500, kiya.Map{"message": "Gagal membuat token CSRF"})
    }
    return c.JSON(200, kiya.Map{"csrf_token": token})
}

// Middleware pemeriksa, dipasang di route group yang menerima method mutating.
func VerifyCSRF(next kiya.HandlerFunc) kiya.HandlerFunc {
    return func(c *kiya.Context) error {
        if c.Request().Method == http.MethodGet {
            return next(c)
        }
        if !c.VerifyCSRFToken(c.Request().Header.Get("X-CSRF-Token")) {
            return c.JSON(403, kiya.Map{"message": "Token CSRF tidak valid"})
        }
        return next(c)
    }
}
```
Token terikat ke session ID + timestamp (AES-GCM, kadaluwarsa 7200 detik) - sama persis mekanisme yang dipakai `InjectCSRFIntoForms`, cuma jalur pengambilan/pengirimannya beda (header `X-CSRF-Token`, bukan hidden input form).

#### G. Session Lifetime Kustom (`Session.SetMaxAge`)
Default umur cookie session diatur global lewat `cfg.Server.SessionMaxAge`. Untuk kasus per-session butuh umur berbeda (mis. fitur "ingat saya" yang memperpanjang sesi login jadi 7 hari, dibanding sesi biasa yang lebih pendek), panggil `SetMaxAge` sebelum `Save()`:
```go
sess := c.Session()
sess.SetString("actor_id", "42")
if req.Remember {
    sess.SetMaxAge(7 * 24 * 3600) // 7 hari
}
sess.Save()
```
`0` = session cookie (hilang saat browser ditutup), negatif = hapus cookie saat `Save()`.

---

### 4. Berkas `router.go` - Routing, Grouping (`Route`), & Trailing Slash Normalization

Kiya Framework memiliki router bawaan yang cepat dengan fitur pencarian berbasis Trie Tree ([internal/router/tree.go](file:///d:/Development/Project/Sinansikula/sinansikula/server/kiya/internal/router/tree.go)).

#### Fitur Automatic Trailing Slash Normalization:
Secara internal, Kiya selalu membersihkan path menggunakan `path.Clean()` pada saat **pendaftaran route (`AddRoute`)** maupun **pencarian request (`FindRoute`)**.

Oleh karena itu:
- URL `/manage` dan `/manage/` secara otomatis di-normalize ke segment yang sama (`["manage"]`).
- Saat membuat sub-route/grouping via `r.Route("/manage", ...)`, Anda **CUKUP mendaftarkan 1 rute saja** untuk root grup:

```go
app.Route("/manage", func(r *kiya.Router) {
    // Cukup mendaftarkan r.Get("", ...) saja.
    // Jalur /manage maupun /manage/ otomatis dilayani oleh handler ini.
    r.Get("", manage.DashboardHandler)
    r.Get("/dashboard", manage.DashboardHandler)
})
```

---

### Live Reload (Development Mode)

- **WSL (Coding di Windows & Menjalankan di WSL)**:
  ```bash
  nodemon --legacy-watch . --ext go --exec "go run app/main.go" --signal SIGTERM
  ```

- **Linux Native**:
  ```bash
  reflex -d none -s -R vendor. -r .go$ -- go run app/main.go
  ```

