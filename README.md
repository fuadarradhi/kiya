# Kiya Framework 🚀

Framework web Go yang aman, cepat, dan modular dengan fitur keamanan enterprise bawaan (WAF, Rate Limiting, Session Management, Pongo2 View Engine, dan Active Record ORM).

---

## 📚 Dokumentasi Berkas Framework & Cara Penggunaan

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

#### 💡 Cara Inisialisasi Engine:
```go
app, err := kiya.New(
    kiya.WithAddr("0.0.0.0", 8080),
    kiya.WithDebug(true),
    kiya.WithCORS("*"),
    kiya.WithViews(views.FS),
    kiya.WithEncryption("secret-key-32-bytes"),
)
```

---

### 2. Berkas `models.go` & `history.go` — Active Record & Audit Trail History (`BaseModel`)

Fitur **`BaseModel`** di Kiya membuat struct Go bertindak sebagai ORM Active Record dengan query builder terintegrasi (`.WhereEq()`, `.Find()`, `.Insert()`, `.Update()`, `.Delete()`, `.Purge()`).

#### 📋 Aturan Field Standar pada `BaseModel` & Tabel Database:

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

#### 📝 Contoh Struktur Model Standar Lengkap:
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
