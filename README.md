# confezy

Feature flag + JSON config servisi. Tek executable, SQLite (WAL), gömülü HTMX admin paneli.
CGO yok — statik binary, sorunsuz cross-compile. Çalışma anında dışarıya hiçbir istek yok:
HTMX, CSS ve şablonlar binary'ye gömülüdür.

## Kurulum

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o confezy .

# Başka bir platforma:
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o confezy .
```

## Çalıştırma

```bash
# 1) Admin hesabı (şifre terminalden sorulur, ekrana yazılmaz)
./confezy admin-create -username admin -db ./data.db

# Şifreyi değiştirmek için:
./confezy admin-create -username admin -db ./data.db -reset

# 2) Sunucu
./confezy serve -port 8080 -db ./data.db
```

Panel: <http://localhost:8080/ui/login>

`serve` bayrakları: `-port` (varsayılan 8080), `-host` (varsayılan tüm arayüzler), `-db`
(varsayılan `./data.db`). Migration'lar her açılışta otomatik uygulanır.

## Kavramlar

```
Project
└── Environment (proje açılınca "prod" otomatik oluşur)
    ├── Feature Flags   (bool)
    ├── JSON Configs    (herhangi bir geçerli JSON)
    └── API Keys        (read / write / admin)
```

Flag ve config key formatı: `^[a-z0-9_]{1,64}$`
Proje ve environment slug formatı: `^[a-z0-9][a-z0-9_-]{0,62}$`

Her flag ve config'in kendi `version`'ı vardır ve her güncellemede 1 artar. Ayrıca her
environment'ın görünmez bir `updated_at` damgası vardır; altındaki herhangi bir değişiklikte
güncellenir ve client API'nin `ETag`'ini besler. Damga her yazmada **kesin olarak artar**
(`MAX(now, updated_at + 1)`) — aksi halde aynı saniye içindeki iki yazma aynı ETag'i üretir
ve yoklayan istemci değişikliği hiç göremezdi.

## API key'ler

Format: `ff_{scope}_{env-slug}_{24 karakter}` — örn. `ff_read_prod_a1b2c3d4e5f6g7h8i9j0k1l2`

Key **yalnızca oluşturulduğu anda bir kez** gösterilir; veritabanında SHA-256 hash'i ve ilk
12 karakteri saklanır. Key bir environment'a bağlıdır, bu yüzden istemci proje/environment
bilgisi göndermez — sadece başlığı gönderir:

```
X-App-Key: ff_read_prod_a1b2c3d4e5f6g7h8i9j0k1l2
```

| Scope | Yetki |
|---|---|
| `read` | Client API (GET) |
| `write` | Client API + Management API |
| `admin` | `write` ile aynı (v0.1) |

Panelden iptal edilen (revoke) key anında `401` almaya başlar.

## Client API

Read key yeterlidir. Tüm cevaplar `ETag` taşır; `If-None-Match` ile tekrar sorulduğunda
değişiklik yoksa gövdesiz `304` döner.

```http
GET /v1/snapshot            → { "flags": {...}, "configs": {...} }
GET /v1/flags               → { "flags": { "new_checkout": true, ... } }
GET /v1/flags/{key}         → { "key", "enabled", "version" }
GET /v1/configs             → { "configs": { "payment_rules": {...}, ... } }
GET /v1/configs/{key}       → { "key", "value", "version" }
```

```bash
curl -H "X-App-Key: $KEY" http://localhost:8080/v1/snapshot
```

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

### Önerilen istemci pattern'i

1. Açılışta `/v1/snapshot` çek; cevabı **ve** `ETag`'i lokalde sakla.
2. Periyodik olarak (örn. 60 sn) veya uygulama foreground'a döndüğünde `If-None-Match`
   başlığıyla tekrar sor.
3. `304` → hiçbir şey yapma. `200` → yeni snapshot'ı kaydet.
4. Servise ulaşılamazsa: lokal cache'i kullan; o da yoksa koddaki güvenli default'a düş.

```bash
# İlk istek
curl -D- -H "X-App-Key: $KEY" http://localhost:8080/v1/snapshot
# ETag: "1786814026"

# Sonraki yoklama → değişiklik yoksa 304, gövde boş
curl -o /dev/null -w '%{http_code}\n' \
  -H "X-App-Key: $KEY" -H 'If-None-Match: "1786814026"' \
  http://localhost:8080/v1/snapshot
```

## Management API

Write (veya admin) key gerekir. Key'in bağlı olduğu environment üzerinde çalışır.

```http
POST   /v1/manage/flags               { "key", "enabled", "description?" }
PUT    /v1/manage/flags/{key}         { "enabled", "description?", "expectedVersion" }
DELETE /v1/manage/flags/{key}         { "expectedVersion" }

POST   /v1/manage/configs             { "key", "value", "description?" }
PUT    /v1/manage/configs/{key}       { "value", "description?", "expectedVersion" }
DELETE /v1/manage/configs/{key}       { "expectedVersion" }
```

Kurallar:

- `expectedVersion` PUT ve DELETE'te zorunludur. `DELETE` için gövde yerine
  `?expectedVersion=2` query parametresi de kullanılabilir.
- Uyuşmazsa `409` döner ve cevap kaydın **güncel halini** `current` altında taşır.
- Config `value` kaydedilmeden önce doğrulanır; geçersiz JSON `400` alır.
- Başarılı yazma `version`'ı +1 yapar ve aynı transaction'da environment damgasını günceller.
- Hata formatı her yerde aynı:

```json
{ "error": { "code": "version_conflict", "message": "..." } }
```

Kodlar: `unauthorized`, `forbidden`, `not_found`, `invalid_request`, `already_exists`,
`version_conflict`, `internal_error`.

```bash
curl -X POST -H "X-App-Key: $WRITE_KEY" -H 'Content-Type: application/json' \
  -d '{"key":"new_checkout","enabled":true,"description":"Yeni ödeme akışı"}' \
  http://localhost:8080/v1/manage/flags

curl -X PUT -H "X-App-Key: $WRITE_KEY" -H 'Content-Type: application/json' \
  -d '{"enabled":false,"expectedVersion":1}' \
  http://localhost:8080/v1/manage/flags/new_checkout
```

## Admin paneli

Session cookie ile korunur; Management API'den bağımsızdır (API'ler saf JSON döner, panel
HTML fragment'ı). Sayfalar:

```
/ui/login                    Giriş
/ui/projects                 Proje listesi + yeni proje
/ui/p/{slug}                 Environment listesi + yeni environment
/ui/p/{slug}/{env}/flags     Flag listesi, toggle switch'ler
/ui/p/{slug}/{env}/configs   Config listesi + JSON editör
/ui/p/{slug}/{env}/keys      API key listesi, oluştur / iptal et
```

Dark/light tema sağ üstteki düğmeden; tercih `localStorage`'da tutulur ve sayfa boyanmadan
önce uygulanır. Başka birinin değişikliğinin üzerine yazmayı `expectedVersion` engeller:
çakışmada satır kırmızıya döner ve "Sayfa güncel değil, yenileyin" uyarısı çıkar.

## Mimari notlar

- **İki ayrı `*sql.DB`**: okuma havuzu 8 bağlantı, yazma tarafı tek bağlantı. Yazmalar
  uygulama seviyesinde serialize olur, `SQLITE_BUSY` görülmez.
- PRAGMA'lar DSN üzerinden her bağlantıya uygulanır:
  `journal_mode(WAL)`, `busy_timeout(10000)`, `synchronous(NORMAL)`, `foreign_keys(ON)`.
- Flag/config değişikliği ve environment damgasının güncellenmesi **tek transaction**.
- Admin şifresi argon2id, API key'ler SHA-256 ile saklanır.
- Session cookie `HttpOnly` + `SameSite=Lax`; HTTPS üzerinden gelen isteklerde `Secure`.

## Proje yapısı

```
confezy/
├── main.go                  CLI: serve, admin-create
├── embed.go                 templates/ ve static/ gömme
├── internal/
│   ├── db/                  bağlantılar, migration runner, tüm sorgular
│   ├── model/               domain struct'ları + validasyon
│   ├── auth/                argon2id şifre, API key + scope, session
│   ├── httpx/               ortak JSON cevap/hata zarfı
│   ├── api/                 client.go, manage.go, etag.go
│   └── ui/                  HTMX panel handler'ları
├── templates/               base, sayfalar, partials (fragment'lar)
└── static/                  htmx.min.js, app.css
```

## v0.2'ye ertelenenler

Webhook + HMAC + retry/delivery log, rate limiting, key rotate, import/export, SSE,
tag/filtreleme, JSON Merge Patch, history/rollback.
