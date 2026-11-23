# MangaHub — Member 1 Handoff Checklist

**Owner:** Member 1 — Backend, Databases, gRPC Catalog, TCP Progress Server.

**Handoff target:** Member 2 — tview TUI, TCP client, UDP notifications, WebSocket chat, admin release, end-to-end integration. Start Member 2 coding **only after this checklist is satisfied**.

## 1. What Member 1 delivers

- `cmd/catalog/main.go` — independent Catalog gRPC startup, SQLite and conditional 35-title seed.
- `cmd/backend/main.go` — Gin HTTP + TCP server startup, shared auth settings and Catalog client.
- `cmd/backend/extensions.go` — explicit Member 2 integration hook.
- `internal/config/` and `.env.example` — configuration; keep private `.env` out of Git.
- `internal/database/` and embedded `manga_seed.json` — SQLite initialization and seed.
- `internal/catalog/`, `proto/` — 3 unary Catalog RPCs and generated Go stubs.
- `internal/auth/` — registration, login, bcrypt, JWT middleware.
- `internal/httpapi/` and `internal/library/` — manga search/detail, per-user library, versioned reading progress.
- `internal/tcp/server/` — authenticated, user-isolated JSON-line progress notifications.
- `cmd/backend/main_test.go` and package tests — integration and negative-case tests.
- `README.md`, API and network contracts, sources list, and this handoff document.

## 2. Verification required before merging

Run from repository root:

```powershell
go fmt ./...
go test -v -count=1 ./cmd/backend
go test -count=1 ./...
```

Check all packages report `ok` or `[no test files]`. In separate terminals, start:

```powershell
go run ./cmd/catalog
```

```powershell
go run ./cmd/backend
```

Then verify these HTTP endpoints and status codes (HTTP clients cannot prove gRPC directly, but search uses the actual Catalog RPC):

| Case | Expected |
|---|---|
| `GET /api/health` | 200; `status=ok` |
| `GET /api/manga` | 200; 35 bundled items in current seed |
| `GET /api/manga?q=one%20piece` | 200; One Piece returned |
| Register/login | 201 then 200 with JWT |
| Add same library manga twice | 201 then 409 |
| Update progress `25` with version `1` | 200, chapter 25 and version 2 |
| Bad/absent token on Library | 401 |
| Two TCP connections same user | Both receive correct progress event |
| Different user's TCP connection | Receives no private event |
| Stop Catalog and search | 503 within bounded time |
| Restart both processes | Previously saved users, library, and manga persist |

Do not report a specific test case as observed until it was actually executed or confirmed by a passing test. Record real output/screenshots; do not fabricate results. Use `docs/TEST_CHECKLIST.md`.

## 3. Git/GitHub transition (UI-first)

1. In VS Code, verify the current branch is **`mem1`** and Source Control has **no uncommitted changes**.
2. Make the final Member 1 commit (message **`Docs`**) and click **Sync Changes / Push**.
3. Open the GitHub repository page. Set branch **`mem1`** and choose **Contribute → Open pull request** (or **Compare & pull request** if offered).
4. Verify **base: `main`** and **compare: `mem1`**. Review the changed files, especially to confirm `.env` and `data/*.db` are **not included**.
5. Create and merge the pull request **only after passing verification**. Confirm the latest changes are now on `main`.
6. Member 2 opens/clones the repository in VS Code, checks out `main`, pulls the merged changes, then creates their own branch (e.g. `feature/member2`). Member 2 should not continue working on `mem1`.
7. If there are merge conflicts, resolve them **before** announcing handoff; do not overwrite working code without comparing changes.

This workflow avoids simultaneous edits to the same core files.

## 4. Exactly where Member 2 works

| Path | Member 2 action |
|---|---|
| `cmd/tui/`, `internal/tui/` | Implement all tview screens and keyboard navigation |
| `internal/tcp/client/` | Implement TCP auth, newline framing, reconnection and UI updates |
| `internal/udp/` | Implement UDP subscriber/server and chapter notifications |
| `internal/websocket/` | Implement authenticated room chat and hub |
| `internal/admin/` | Implement protected admin chapter-release flow |
| `cmd/backend/extensions.go` | Attach only new UDP/WS/admin components; preserve core routes |
| `README.md`, `docs/`, `REPORT.pdf` | Extend with actual Member 2 features, tests and evidence |

Member 2 must **not** access Catalog SQLite directly from TUI; use Gin HTTP, which calls gRPC. They should avoid modifying `internal/auth`, `internal/library`, `internal/catalog`, `proto/`, and `internal/tcp/server` except when a diagnosed integration bug is agreed upon.

## 5. Existing extension boundaries and limitations

- `cmd/backend/extensions.go` currently returns a no-op shutdown callback: **UDP/WS/admin are not live**.
- `POST /api/admin/manga/:id/release` and `GET /ws` are reserved but not registered yet.
- The TCP event may reach the initiating session in addition to other same-user sessions; TUI de-duplicates by `(manga_id, version)`.
- There is no public/cloud deployment requirement. Test locally with two or three terminal sessions.
- On restart, seed JSON does **not** overwrite `catalog.db` when it is nonempty.
- There are 35 manga in the initial embedded catalogue, spanning more than four genres. Contents are metadata only.

## 6. Member 2 acceptance criteria

At handoff, Member 2 should be able to:

- Run `go run ./cmd/catalog` and `go run ./cmd/backend` without changing source code.
- Register/log in, browse manga, add to library and update progress using REST/JWT.
- Authenticate a TCP client with the same login JWT and receive JSON line events after progress updates.
- Read `docs/API_CONTRACT.md`, `docs/NETWORK_CONTRACT.md`, and `docs/TCP_PROGRESS_CONTRACT.md` to implement clients without guessing field names.
- See code/tests committed to **main**, with no local secrets or SQLite runtime files pushed.

## 7. Group final deliverables (not part of this handoff)

Member 2 integrates TUI, UDP, WS, chapter-release demo and completes end-to-end test evidence. Both members consolidate the **English 4–5 page `REPORT.pdf`**, demonstrate all five protocols, capture 3 meaningful AI interaction records with at least one verified revision/rejection, and package `GroupXX_MangaHub.zip` for the course deadline. Do not submit the Member 1-only codebase as if it satisfied all final F01–F06 demonstrations.
