# Feature Flag + JSON Config Servisi — Nihai Plan (v0.1, Go)

Tek executable, SQLite (WAL), gömülü HTMX admin paneli. Dış bağımlılık yok: CDN yok, npm yok, JS framework yok. HTMX ve CSS `embed` ile binary'ye gömülür. CGO yok → cross-compile sorunsuz, statik binary.

---

## 1. Stack

| Katman | Seçim |
|---|---|
| Dil | Go (1.22+) |
| HTTP router | `net/http` standart kütüphane (1.22'nin method + path pattern desteği yeterli: `mux.HandleFunc("GET /v1/flags/{key}", ...)`) — istersen `chi` de olur ama gerek yok |
| SQLite driver | `modernc.org/sqlite` (pure Go, CGO'suz — PocketBase'in kullandığı) |
| Template | `html/template` (standart kütüphane) |
| Frontend | HTMX (embed) + el yazması CSS (dark/light mode) |
| Auth (UI) | Session cookie (SQLite `sessions` tablosu) |
| Auth (API) | `X-App-Key` header |
| Hash | `golang.org/x/crypto/argon2` (admin şifresi), `crypto/sha256` (API key) |
| Static/template embed | `embed` paketi (`//go:embed`) |

Toplam dış bağımlılık: `modernc.org/sqlite` + `golang.org/x/crypto`. Gerisi standart kütüphane.

---

## 2. Veri Modeli

```text
Project
└── Environment (prod otomatik oluşur, dev/staging eklenebilir)
    ├── Feature Flags   (bool)
    ├── JSON Configs    (herhangi bir geçerli JSON)
    └── API Keys        (read / write / admin, environment'a bağlı)
```

Sadeleştirmeler (kesinleşti):
- **Tag yok.**
- **Environment revision yok.** Sadece her flag/config'in kendi `version`'ı var, her update'te +1.
- **History yok, rollback yok, webhook yok** (v0.2'ye ertelendi).
- ETag/304 için environment'a tek bir `updated_at` (unix timestamp) kolonu yeter — herhangi bir değişiklikte güncellenir. Bu bir history değil, tek kolonluk görünmez bir iç detay; API cevaplarında revision diye bir alan **yok**.

---

## 3. SQLite Şeması

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 10000;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,           -- argon2id
  created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,           -- rastgele 32 byte hex
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);

CREATE TABLE projects (
  id         INTEGER PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE environments (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug       TEXT NOT NULL,              -- prod, staging...
  updated_at INTEGER NOT NULL,           -- ETag için, her değişiklikte güncellenir
  created_at INTEGER NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE api_keys (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key_hash       TEXT NOT NULL UNIQUE,   -- SHA-256(key)
  key_prefix     TEXT NOT NULL,          -- ilk 12 karakter, UI'da göstermek için
  scope          TEXT NOT NULL CHECK (scope IN ('read','write','admin')),
  label          TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  revoked_at     INTEGER                 -- NULL = aktif
);

CREATE TABLE feature_flags (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  enabled        INTEGER NOT NULL CHECK (enabled IN (0,1)),
  description    TEXT NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  updated_at     INTEGER NOT NULL,
  UNIQUE (environment_id, key)
);

CREATE TABLE configs (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  value          TEXT NOT NULL,          -- JSON string, kaydetmeden önce validate edilir
  description    TEXT NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  updated_at     INTEGER NOT NULL,
  UNIQUE (environment_id, key)
);
```

Key formatı kuralı (flag ve config key'leri): `^[a-z0-9_]{1,64}$`

---

## 4. DB Erişim Mimarisi (PocketBase pattern'i)

Go'da `database/sql` ile iki ayrı `*sql.DB` handle'ı aç:

```go
// Okuma: paralel, çok connection
readDB, _  := sql.Open("sqlite", dsn)
readDB.SetMaxOpenConns(8)

// Yazma: TEK connection — yazmalar uygulama seviyesinde serialize olur
writeDB, _ := sql.Open("sqlite", dsn)
writeDB.SetMaxOpenConns(1)
```

- Tüm SELECT'ler `readDB`'den, tüm INSERT/UPDATE/DELETE'ler `writeDB`'den.
- Her yazma işlemi bir transaction: flag/config değişikliği + `environments.updated_at` güncellemesi birlikte commit edilir.
- Her iki DSN'de de PRAGMA'lar açılışta set edilir (`_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)` — modernc DSN sözdizimi).

---

## 5. API Key Sistemi

Key formatı: `ff_{scope}_{env-slug}_{24 rastgele karakter}`
Örnek: `ff_read_prod_a1b2c3d4e5f6g7h8i9j0k1l2`

- Oluşturulunca **yalnızca bir kez** tam gösterilir; DB'de SHA-256 hash + ilk 12 karakter prefix saklanır.
- Doğrulama: gelen key hash'lenir, `key_hash` üzerinden lookup, `revoked_at IS NULL` kontrolü.
- Key environment'a bağlıdır → client project/environment göndermez.
- Scope yetkileri:
  - `read` → sadece client API (GET)
  - `write` → client API + manage API'de flag/config CRUD
  - `admin` → write + (ileride key yönetimi vb., v0.1'de write ile aynı davranabilir)

---

## 6. Client API (read key yeterli)

Tüm cevaplar `ETag` header'ı taşır: environment `updated_at` değerinden üretilir, örn. `ETag: "1723700000"`. Client `If-None-Match` gönderir, değişiklik yoksa `304 Not Modified` (boş gövde).

```http
GET /v1/snapshot            → { "flags": {...}, "configs": {...} }
GET /v1/flags               → { "flags": { "new_checkout": true, ... } }
GET /v1/flags/{key}         → { "key", "enabled", "version" }
GET /v1/configs             → { "configs": { "payment_rules": {...}, ... } }
GET /v1/configs/{key}       → { "key", "value", "version" }
```

Snapshot örnek cevabı:

```json
{
  "flags": {
    "new_checkout": true,
    "show_ads": false
  },
  "configs": {
    "payment_rules": {
      "minimumAmount": 50,
      "maximumAmount": 5000
    }
  }
}
```

Client kullanım pattern'i (README'ye de yazılacak):
1. Açılışta `/v1/snapshot` çek, cevabı ve ETag'i lokal sakla.
2. Periyodik olarak (örn. 60 sn) veya foreground'a dönüşte `If-None-Match` ile tekrar sor.
3. 304 → hiçbir şey yapma. 200 → yeni snapshot'ı kaydet.
4. Servise ulaşılamazsa: lokal cache → o da yoksa koddaki güvenli default.

---

## 7. Management API (write/admin key)

```http
POST   /v1/manage/flags               { "key", "enabled", "description?" }
PUT    /v1/manage/flags/{key}         { "enabled", "description?", "expectedVersion" }
DELETE /v1/manage/flags/{key}         { "expectedVersion" }

POST   /v1/manage/configs             { "key", "value", "description?" }
PUT    /v1/manage/configs/{key}       { "value", "description?", "expectedVersion" }
DELETE /v1/manage/configs/{key}       { "expectedVersion" }
```

Kurallar:
- `expectedVersion` uyuşmazsa → `409 Conflict` + güncel kaydın hali cevapta döner.
- Config `value` kaydedilmeden önce `json.Valid()` ile doğrulanır; geçersizse `400`.
- Update başarılıysa `version` +1 ve `environments.updated_at` güncellenir (aynı transaction).
- Başarılı cevap: `{ "key", "enabled"/"value", "version" }`
- Hata formatı her yerde aynı: `{ "error": { "code": "version_conflict", "message": "..." } }`

---

## 8. Admin UI (HTMX + html/template)

### Prensipler
- HTMX minified dosyası `//go:embed` ile binary'ye gömülür, `/static/htmx.min.js` olarak servis edilir. Dışarıya hiçbir istek yok.
- Tek CSS dosyası, el yazması. CSS custom properties ile tema:

```css
:root { --bg: #ffffff; --fg: #1a1a1a; --accent: #2563eb; ... }
[data-theme="dark"] { --bg: #111418; --fg: #e5e7eb; ... }
```

- Tema seçimi: `prefers-color-scheme` default + sağ üstte toggle, tercih `localStorage`'da, sayfa yüklenmeden önce küçük bir inline script ile uygulanır (flash önlenir).
- UI kendi endpoint'lerini kullanır (`/ui/...`), session cookie ile korunur. Management API'den ayrıdır; API'ler saf JSON kalır, UI endpoint'leri HTML fragment döner.
- Template'ler de `//go:embed templates/*` ile gömülür; fragment'lar `{{define "flag_row"}}` blokları olarak tanımlanıp HTMX cevaplarında `ExecuteTemplate(w, "flag_row", data)` ile render edilir.

### Sayfalar

```text
/ui/login                    Login formu
/ui/projects                 Proje listesi + yeni proje
/ui/p/{slug}                 Proje detay: environment listesi + yeni env
/ui/p/{slug}/{env}/flags     Flag listesi (toggle switch'ler)
/ui/p/{slug}/{env}/configs   Config listesi + JSON editör
/ui/p/{slug}/{env}/keys      API key listesi + oluştur/revoke
```

### HTMX etkileşimleri
- Flag toggle: `<input type="checkbox" hx-put="/ui/.../flags/new_checkout/toggle" hx-swap="outerHTML">` → satır fragment'ı döner. 409 gelirse satır kırmızı highlight + "Sayfa güncel değil, yenileyin" mesajı.
- Yeni flag/config: satır içi form, `hx-post`, başarıda listeye satır eklenir.
- Config editörü: `<textarea>` + kaydetmeden önce küçük bir inline JS ile `JSON.parse` denemesi (geçersizse buton disable + hata satırı). Monaco vb. yok.
- Silme: `hx-delete` + `hx-confirm="Silinsin mi?"`.
- API key oluşturma: modal fragment, key **bir kez** gösterilir, kopyala butonu.

---

## 9. Proje Yapısı

```text
featureflag/
├── go.mod
├── main.go                  # CLI: serve, admin-create; flag'ler (port, db path)
├── internal/
│   ├── db/
│   │   ├── db.go            # readDB + writeDB açılışı, PRAGMA DSN, migration runner
│   │   └── migrations/
│   │       └── 001_init.sql # //go:embed ile gömülür
│   ├── model/
│   │   └── model.go         # struct'lar
│   ├── auth/
│   │   ├── apikey.go        # key üretme, hash, scope middleware
│   │   └── session.go       # session middleware, login/logout
│   ├── api/
│   │   ├── client.go        # /v1/snapshot, /v1/flags, /v1/configs
│   │   ├── manage.go        # /v1/manage/*
│   │   └── etag.go          # ETag üret/karşılaştır helper
│   └── ui/
│       ├── ui.go            # /ui/* route'ları, template render helper
│       ├── flags.go
│       ├── configs.go
│       ├── projects.go
│       └── keys.go
├── templates/               # //go:embed
│   ├── base.html            # layout + tema toggle
│   ├── login.html
│   ├── projects.html
│   ├── flags.html           # + {{define "flag_row"}} fragment'ları
│   ├── configs.html
│   └── keys.html
└── static/                  # //go:embed
    ├── htmx.min.js
    └── app.css
```

Çalıştırma:

```bash
featureflag admin-create -username admin        # şifre prompt'tan
featureflag serve -port 8080 -db ./data.db
```

Build:

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o featureflag .
# İstediğin platforma cross-compile:
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o featureflag .
```

---

## 10. Uygulama Sırası (Milestone'lar)

1. **İskelet + DB**: `net/http` mux ayakta, migration çalışıyor, PRAGMA'lar DSN'de set, readDB(8) + writeDB(1) kurulu.
2. **Domain + Management API**: project/env/flag/config CRUD, `expectedVersion` + 409, `json.Valid` kontrolü. `curl` ile test.
3. **API key auth**: key üretme, hash lookup, scope middleware. Client API endpoint'leri + ETag/304.
4. **Admin UI**: login/session → projects → flags (toggle) → configs (editör) → keys. CSS + dark/light en sonda cilalanır.
5. **Paketleme**: embed'lerin doğrulanması, `CGO_ENABLED=0` release build, tek binary doğrulaması, kısa README (client pattern'i dahil).

Her milestone sonunda çalışan bir şey var; 2. adımdan itibaren servis gerçek anlamda kullanılabilir.

---

## 11. v0.2'ye Ertelenenler

Webhook + HMAC + retry/delivery log, rate limiting, key rotate, import/export, SSE, tag/filtreleme (istersen), JSON Merge Patch, history/rollback.