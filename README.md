# MangaHub — Manga Tracking System

Final project for **IT096IIU — Net-Centric Programming**. MangaHub lets users discover manga, maintain a private reading library, and synchronize progress across authenticated TCP clients. The finished group project also includes a terminal interface, chapter alerts, and real-time discussion; those modules belong to Member 2 and **are not implemented in the Member 1 handoff**.

## 1. Implementation status

| Component | Member | Status in this handoff |
|---|---|---|
| Go/Gin REST API, bcrypt, JWT | 1 | Implemented |
| Backend SQLite (users, user_library) | 1 | Implemented |
| Catalog SQLite and 35 bundled manga | 1 | Implemented |
| Independent gRPC Catalog Service | 1 | Implemented |
| TCP progress server and JWT handshake | 1 | Implemented |
| End-to-end backend integration test | 1 | Included; run locally before handoff |
| Go tview TUI / HTTP and TCP clients | 2 | Pending |
| UDP subscriptions / chapter notifications | 2 | Pending |
| WebSocket chat / rooms | 2 | Pending |
| Protected admin chapter-release API | 2 | Pending |

The 35 records are metadata only, not manga pages. Seed counts are demonstration baselines, not a live chapter feed. See `docs/MANGA_SOURCES.md`.

## 2. Requirements

- **Windows 10/11**, or another operating system supported by Go; sample commands below use PowerShell.
- **Go 1.25 or newer** (`go version`).
- **Git**, if cloning from GitHub.
- No MySQL, Java, Docker, cloud hosting, or Redis required.
- `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` are needed **only to regenerate** the checked-in protobuf code. They are not necessary for ordinary runs if generated files already exist.

## 3. Source layout

```text
cmd/backend/             Gin REST API + TCP server startup
cmd/catalog/             Independent gRPC Catalog process
cmd/tui/                 Reserved for Member 2
internal/auth/           Registration, login, JWT verification
internal/catalog/        gRPC Catalog implementation
internal/config/         Environment configuration
internal/database/       SQLite schema and embedded 35-manga seed
internal/httpapi/        Manga and Library REST handlers
internal/library/        Per-user SQLite library store and version checks
internal/tcp/server/     Authenticated newline-delimited JSON TCP server
proto/manga.proto        gRPC service contract
proto/mangapb/           Generated protobuf/go-grpc code
data/                    Runtime SQLite files (ignored by Git)
docs/                    Protocol contracts, tests, and handoff notes
```

## 4. First-time setup (PowerShell)

Open VS Code at the **repository root**, the folder containing `go.mod`.

```powershell
go version
go mod download
if (!(Test-Path .env)) { Copy-Item .env.example .env }
```

Set a **private random secret** in `.env` (leave `.env.example` unchanged):

```powershell
$bytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
$secret = [Convert]::ToBase64String($bytes)
(Get-Content .env) -replace '^JWT_SECRET=.*$', "JWT_SECRET=$secret" |
    Set-Content .env
```

Verify that `.env` contains `JWT_SECRET=` followed by the generated string, **not** `CHANGE_ME`. Do not paste that secret into screenshots, commits, issues, or reports. A different secret invalidates previously issued tokens.

Use `.env` for local overrides; the committed `.env.example` is only a template. The program will create `data/` and the SQLite files if needed.

## 5. Default ports and storage

| Setting | Default | Purpose |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Gin REST; Member 2 later adds WebSocket listener |
| `TCP_ADDR` | `:9090` | TCP progress synchronization |
| `UDP_ADDR` | `:9091` | Reserved for Member 2; not running yet |
| `GRPC_ADDR` | `:9092` | Catalog gRPC listener |
| `GRPC_TARGET` | `localhost:9092` | Backend-to-Catalog dial target |
| `BACKEND_DB_PATH` | `data/backend.db` | Users + per-user library |
| `CATALOG_DB_PATH` | `data/catalog.db` | Manga metadata |
| `JWT_TTL_HOURS` | `24` | Signed-token lifetime |
| `JWT_SECRET` | required in `.env` | Shared by HTTP auth and TCP server |

For local-only access, it is advisable to use `HTTP_ADDR=127.0.0.1:8080`, `TCP_ADDR=127.0.0.1:9090`, and `GRPC_ADDR=127.0.0.1:9092` in `.env`. The defaults using `:` may listen on multiple network interfaces. This is a **local demonstration**, not a production deployment with TLS.

## 6. Start the services

Use **separate VS Code terminals** and leave them open.

**Terminal 1 — Catalog:**

```powershell
go run ./cmd/catalog
```

The first startup should log `Catalog ready: 35 manga (35 newly seeded)`. Later runs should log `35 manga (0 newly seeded)` because the database is persistent.

**Terminal 2 — Backend:**

```powershell
go run ./cmd/backend
```

The backend starts HTTP (`8080`) and TCP (`9090`) in the **same process**. It calls Catalog over gRPC. Stop either service with **Ctrl+C** in its own terminal.

**Terminal 3 — TUI (after Member 2 implements it):**

```powershell
go run ./cmd/tui
```

The TUI command is the agreed final entry point; it **does not run** in the Member 1-only handoff.

## 7. Verify a running backend

With both Catalog and Backend running, open:

- Health: http://127.0.0.1:8080/api/health
- Catalog listing: http://127.0.0.1:8080/api/manga
- Search: http://127.0.0.1:8080/api/manga?q=one%20piece
- Details: http://127.0.0.1:8080/api/manga/one-piece

Example health JSON:

```json
{"service":"backend","status":"ok"}
```

**Register, log in, create a library entry, and update progress** in a third PowerShell terminal:

```powershell
$base = 'http://127.0.0.1:8080'
$username = 'demo' + (Get-Random -Minimum 100000 -Maximum 999999)
$credentials = @{ username = $username; password = 'DemoPass123!' } | ConvertTo-Json
$register = Invoke-RestMethod -Uri "$base/api/auth/register" -Method Post -ContentType 'application/json' -Body $credentials
$login = Invoke-RestMethod -Uri "$base/api/auth/login" -Method Post -ContentType 'application/json' -Body $credentials
$headers = @{ Authorization = "Bearer $($login.access_token)" }
$added = Invoke-RestMethod -Uri "$base/api/library" -Method Post -Headers $headers -ContentType 'application/json' -Body '{"manga_id":"one-piece","status":"reading"}'
$updated = Invoke-RestMethod -Uri "$base/api/library/one-piece/progress" -Method Put -Headers $headers -ContentType 'application/json' -Body '{"current_chapter":25,"status":"reading","version":1}'
$library = Invoke-RestMethod -Uri "$base/api/library" -Headers $headers
$register.user
$added.entry
$updated.entry
$library.library | Format-Table
```

Expected for a **new** entry: initial `current_chapter=0, version=1`; updated `current_chapter=25, version=2`. The account name varies by run. Use a fresh demo user to avoid HTTP `409` on duplicate inserts.

## 8. Run tests

Run from the repository root (existing server processes are not required for these tests):

```powershell
go test -v -count=1 ./cmd/backend
go test -count=1 ./...
```

All packages must report `ok` or `[no test files]`. Preserve the actual terminal output for the final report; do not claim tests passed unless they were run successfully. `go test -race ./...` is optional on an environment with a compatible C toolchain.

## 9. How the services communicate

```text
                        HTTP/JSON, Bearer JWT
Go TUI (Member 2)  -------------------------------> Gin Backend (:8080)
        |                                                 |
        +---------------- TCP JSON lines (:9090) ---------+
        |                                                 |
        +-- UDP (:9091), WebSocket (/ws) -- Member 2 ----+
                                                          |
                                                gRPC (:9092)
                                                          v
                                                    Catalog Service
                                                          |
                           backend.db           catalog.db (35 titles)
                           users/library         manga metadata
```

Each process owns its own SQLite file. The Gin backend **never directly queries the Catalog SQLite database**; catalog operations go through the actual gRPC client. HTTP library changes commit to backend SQLite before TCP progress events are published.

## 10. Troubleshooting

- **`protoc` not found:** install Protocol Buffers compiler and reopen the terminal; this matters only for regenerating code.
- **`go: no required module provides package ...`:** run `go mod tidy` and `go mod download` from the repository root.
- **`JWT_SECRET ... placeholder`:** replace the sample value in the private `.env` file with a random secret of at least 32 characters.
- **HTTP `503` on `/api/manga`:** ensure Catalog is running on the gRPC target, inspect both logs; calls have a bounded deadline instead of hanging.
- **Port already in use:** stop duplicate servers or select unused ports in `.env`; ensure `GRPC_ADDR` and `GRPC_TARGET` match.
- **HTTP `401`:** log in again and send `Authorization: Bearer <token>` to private routes.
- **HTTP `409`:** duplicate username, duplicate manga in a library, or a stale progress `version`.
- **No UDP/WS/TUI yet:** those are Member 2 deliverables, not missing Member 1 setup steps.
- **`404 /favicon.ico`:** harmless browser icon request; no favicon endpoint is implemented.
- **No manga reset after restart:** intentional; the bundled JSON is seeded only if the Catalog database is empty.

## 11. Security and repository hygiene

Commit `go.mod`, `go.sum`, `.env.example`, Go source, generated `proto/mangapb/*.go`, and the manga seed JSON. Never commit `.env`, SQLite runtime files (`data/*.db` and WAL/SHM variants), executable binaries, or passwords. This demo uses gRPC insecure transport on the local machine, not TLS; do not expose it to the internet unchanged.

## 12. Documentation

- `docs/API_CONTRACT.md` — current HTTP API shapes, auth rules, error statuses.
- `docs/NETWORK_CONTRACT.md` — TCP and gRPC messages, timeouts, integration boundaries.
- `docs/TCP_PROGRESS_CONTRACT.md` — detailed TCP Server implementation contract.
- `docs/MANGA_SOURCES.md` — manga seed references and data limitations.
- `docs/TEST_CHECKLIST.md` — verification plan and evidence tracking.
- `docs/MEMBER1_HANDOFF.md` — instructions to merge and hand off to Member 2.
- `docs/MEMBER1_REPORT_DRAFT.md` — Member 1's report material; **not** the final group `REPORT.pdf`.

## 13. Known limitations before Member 2 integration

- No `cmd/tui` user interface yet; browsing is through REST responses and automated tests.
- UDP alerts, `POST /api/admin/manga/:id/release`, and WebSocket chat are reserved integration points, not implemented endpoints.
- TCP progress events target all authenticated sessions of the same user. They **may include the initiating session**, because no cross-protocol origin-session ID is available yet; clients should de-duplicate by `(manga_id, version)`.
- The seed's chapter counts are demonstration values; the system does not refresh them automatically from a publisher.
- The final English `REPORT.pdf`, group demo, and `.zip` submission are completed after Member 2 has integrated TUI/UDP/WebSocket.
