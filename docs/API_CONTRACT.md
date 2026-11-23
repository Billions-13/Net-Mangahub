# MangaHub HTTP API Contract — Member 1

**State:** Implemented in Member 1 backend (`cmd/backend`), except for explicitly listed Member 2 extensions. The API is HTTP JSON via Gin, default `http://127.0.0.1:8080`.

## Common rules

- JSON request bodies require `Content-Type: application/json`.
- Private routes require `Authorization: Bearer <access_token>` from `/api/auth/login`.
- **Never send or trust a `user_id` field** when accessing private library data; the server derives it from the signed JWT.
- Authentication JWT: HS256; issuer `mangahub`; user ID in `sub`; expiry from `JWT_TTL_HOURS` (default 24h).
- Usernames: 3–32 ASCII letters, numbers, or underscores; uniqueness is case-insensitive.
- Passwords: 8–72 bytes, stored only as bcrypt hashes.
- For errors, the response generally has `{"error":"message"}`.
- HTTP API responses use the wrapped `user`, `manga`, `entry`, and `library` keys shown below. Do not assume top-level raw arrays.

## GET /api/health

Public. Returns `200` if backend SQLite responds:

```json
{"status":"ok","service":"backend"}
```

Backend SQLite unavailable: `503` with `{"status":"unavailable"}`. This check does not itself prove the Catalog gRPC server is available.

## POST /api/auth/register

Public. Register a new user; a second login request is required to receive a JWT.

Request:

```json
{"username":"alice_demo","password":"DemoPass123!"}
```

`201 Created`:

```json
{"user":{"id":1,"username":"alice_demo"}}
```

Typical errors: `400` invalid/missing username or password, `409` duplicate username, `500` unexpected backend error. The server never returns password hashes.

## POST /api/auth/login

Public. Use the same username/password used during registration.

Request:

```json
{"username":"alice_demo","password":"DemoPass123!"}
```

`200 OK` (example values; token is abbreviated):

```json
{
  "access_token":"<signed JWT>",
  "token_type":"Bearer",
  "expires_in":86400,
  "user":{"id":1,"username":"alice_demo"}
}
```

Typical errors: `400` malformed JSON, `401` incorrect username or password, `500` database/token generation failures. `expires_in` is in seconds.

## GET /api/manga?q=<search>

Public. Query is optional; searches title, author, genres, and ID. Empty `q` returns up to 100 titles sorted by name; a search with no matches returns an empty list.

Example request: `GET /api/manga?q=one%20piece`.

`200 OK` (shortened descriptive text):

```json
{"manga":[{"id":"one-piece","title":"One Piece","author":"Eiichiro Oda","genres":["Action","Adventure","Fantasy"],"status":"ongoing","total_chapters":1199,"description":"A young pirate and his crew set sail in search of a legendary treasure."}]}
```

`total_chapters` values are bundled seed baselines; they are not guaranteed to be the latest publisher releases. `q` is limited to 100 bytes. Errors: `400` query too long; `503` unavailable/invalid Catalog response or gRPC deadline.

## GET /api/manga/:id

Public. Example: `GET /api/manga/one-piece`.

`200 OK`:

```json
{"manga":{"id":"one-piece","title":"One Piece","author":"Eiichiro Oda","genres":["Action","Adventure","Fantasy"],"status":"ongoing","total_chapters":1199,"description":"A young pirate and his crew set sail in search of a legendary treasure."}}
```

`400` invalid ID; `404` not found; `503` Catalog service unavailable. The Manga model fields are `id`, `title`, `author`, `genres` (string array), `status`, `total_chapters`, and `description`.

## GET /api/library

**Bearer JWT required.** Returns only the authenticated user's entries; without any entries returns `{"library":[]}`.

`200 OK` example:

```json
{"library":[{"manga_id":"one-piece","status":"reading","current_chapter":25,"version":2,"updated_at":"2026-11-23T12:00:00.000Z"}]}
```

The timestamp above is illustrative, not a recorded server result. Errors: `401` missing/invalid/expired token; `503` library database unavailable.

## POST /api/library

**Bearer JWT required.** Adds a manga after checking the Catalog gRPC service.

Request:

```json
{"manga_id":"one-piece","status":"reading"}
```

`status` may be omitted; default is `plan-to-read`. Allowed values: `plan-to-read`, `reading`, `completed`.

`201 Created` example:

```json
{"entry":{"manga_id":"one-piece","status":"reading","current_chapter":0,"version":1,"updated_at":"2026-11-23T12:00:00.000Z"}}
```

Errors: `400` unknown/invalid JSON fields, invalid status or manga ID; `401` unauthorized; `404` Catalog manga ID missing; `409` already in personal library; `503` Catalog or SQLite unavailable.

## PUT /api/library/:mangaId/progress

**Bearer JWT required.** Update a manga already in this user's library; does not implicitly add missing records.

Example path: `/api/library/one-piece/progress`.

Request:

```json
{"current_chapter":25,"status":"reading","version":1}
```

- `current_chapter` is **required**, integer `0 <= current_chapter <= catalog.total_chapters`.
- `status` is optional (allowed values above).
- `version` is optional optimistic concurrency protection; if supplied, it must equal the current stored version.
- A successful change increments the server-side `version` by 1; an unchanged request leaves the version unchanged and sends no TCP progress event.

`200 OK` example:

```json
{"entry":{"manga_id":"one-piece","status":"reading","current_chapter":25,"version":2,"updated_at":"2026-11-23T12:00:00.000Z"}}
```

Errors: `400` negative/over-limit chapter, bad status/version/body; `401` missing/invalid token; `404` manga absent from user's library, or nonexistent in Catalog; `409` stale supplied version; `503` SQLite or Catalog failure. Private operations reject unexpected fields such as `user_id`.

## HTTP status reference

| Code | Meaning and examples |
|---|---|
| `200` | Successful read/login/progress update |
| `201` | User or library entry created |
| `400` | Invalid JSON, query, ID, status, or chapter |
| `401` | Missing/incorrect login credentials or invalid/expired JWT |
| `403` | Reserved for explicitly denied operations; current Member 1 public/private routes mainly use `401`, `404`, or `409` |
| `404` | Catalog manga not found or item absent from own library |
| `409` | Duplicate account/library item or stale version |
| `500` | Unexpected registration/login backend failure |
| `503` | Catalog unavailable/deadline, DB unavailable |

## Not implemented yet (Member 2)

- `POST /api/admin/manga/:id/release` — protected chapter-release trigger; should call gRPC `UpdateChapters`, then emit UDP notifications **only on success**.
- `GET /ws` — authenticated WebSocket discussion/chat route.

Do not call these paths to demonstrate Member 1 success before Member 2 implements them. The extension hook is `cmd/backend/extensions.go`.
