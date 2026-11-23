# MangaHub — Member 1 Technical Report Material

**Status:** Draft sections for insertion into the group's English `REPORT.pdf`. **This is not a finished group report.** Member 2 must provide verified TUI, UDP, WebSocket, and complete end-to-end evidence; the group must review and fit all sections into approximately **4–5 pages of main content** (excluding cover/references/short appendix).

## 1. System Architecture — Member 1 contribution

MangaHub separates presentation, user-specific state, and catalogue metadata. The final application consists of three independently executable Go programs: a terminal client (`cmd/tui`), a Gin backend (`cmd/backend`), and a Catalog gRPC service (`cmd/catalog`). Member 1 implemented the backend and Catalog programs. The terminal client and the UDP/WebSocket features are assigned to Member 2 and were not part of the first-stage handoff.

```text
                     User interface — tview TUI (Member 2)
                       |       |        |          |
                    HTTP/JSON  TCP     UDP      WebSocket
                       |       |        |          |
                       +-------+--------+----------+
                               Gin Backend
                         HTTP 8080 / TCP 9090
                               |         |
                          backend.db     gRPC client
                          users/library       |
                                              v
                                     Catalog Service 9092
                                              |
                                          catalog.db
                                         manga metadata
```

The backend owns `users` and `user_library` in its SQLite file, including registration, password hashes, reading status, chapter progress, and concurrency versions. The Catalog process independently owns the `manga` table in another SQLite file. The Gin backend never opens the Catalog SQLite file directly; it reaches that data through unary gRPC methods. The starter catalogue contains 35 metadata-only manga records and is imported once when the Catalog table is empty. Thus both user data and catalogue data survive a process restart.

## 2. Why these technologies? — HTTP, gRPC and TCP

**HTTP with Gin:** HTTP exposes accessible JSON endpoints for registration, login, browsing, and personal libraries. Its request-response model and HTTP status codes make invalid requests, unauthorized access, duplicates, and service outages testable. Gin middleware validates JWTs so private library requests use the user identity established by the server rather than a `user_id` supplied in JSON. A limitation is that HTTP by itself does not push spontaneous reading-progress changes to a connected terminal client.

**gRPC with protobuf:** Gin communicates with a separately launched Catalog service using protobuf messages and unary `SearchManga`, `GetManga`, and `UpdateChapters` RPCs. This boundary makes it explicit which process owns manga metadata. The backend sets a bounded deadline on Catalog calls. If the Catalog process stops, the HTTP layer translates the failure to a controlled `503` rather than waiting indefinitely. The tradeoff is an additional process, code generation, and the need to handle service availability.

**TCP:** A persistent TCP connection is used to push progress updates between sessions authenticated with the same JWT as HTTP. Each frame is newline-delimited JSON because TCP is a byte stream and does not preserve application message boundaries. After SQLite commits a changed chapter, the backend publishes `progress_updated` with the manga ID, chapter, and version to sessions of the same user. Slow or disconnected clients are removed. TCP provides reliable ordered byte delivery while connected, but it does not automatically replay events after reconnection; a reconnecting client must fetch its library via HTTP.

## 3. Implementation and verification — Member 1

Member 1 implemented password hashing with bcrypt, JWT login and middleware, parameterized SQLite queries, and per-user library isolation. Library entries enforce unique `(user_id, manga_id)`, reading states (`plan-to-read`, `reading`, `completed`), and a server-generated version. A request with an outdated expected version receives `409`, and reading progress exceeding the Catalog chapter count receives `400`. A successful changed update increments its version and emits the TCP event only after the database commit.

The test suite includes unit and integration coverage for HTTP, authentication, SQLite persistence, Catalog gRPC, and TCP framing/isolation. An end-to-end test (`cmd/backend/main_test.go`) starts temporary Gin, gRPC, TCP, and SQLite components to exercise the cross-protocol flow. Actual test results must be pasted from the final run. Development evidence shown earlier included passing TCP tests, ten repeated TCP test runs, and a full package test run **before the end-to-end test file was added**; later screenshots showed successful live health, manga list, and One Piece search endpoints. These observations must not be reported as evidence of unexecuted final tests. See `docs/TEST_CHECKLIST.md`.

**Example test table to finish with real evidence:**

| Case | Expected | Actual evidence |
|---|---|---|
| Search One Piece via Gin → gRPC → SQLite | 200 with manga metadata | Observed in live screenshot |
| Register and login | 201 and 200 with JWT | Capture final run |
| Two same-user TCP clients | Both receive progress event | Capture final run/test |
| Another user's TCP client | Receives no private event | Capture final run/test |
| Stop Catalog during search | Bounded 503 | Capture final run/test |
| Restart database | User/library/manga remain | Capture final run/test |

## 4. Challenges, reflection and contribution

**Actual technical problem:** During development, TCP frame validation failed with `read TCP frame: EOF`: the receiving test client encountered a closed connection before reading the expected error response. The server's terminal error-frame shutdown sequence was revised to perform a more orderly TCP close. In a subsequent terminal screenshot, the targeted test passed, ten successive TCP module test runs passed, and the full package test run at that time passed. This demonstrates that a passing isolated test is insufficient until network framing and shutdown behavior are also verified. Include real before/after terminal images or output from those runs rather than invented log lines.

**Additional actual development issue:** An HTTP Manga API test produced `503` because a temporary gRPC test target was not configured with the correct resolver path. The test harness used `passthrough:///...` to connect to its in-process test listener, while the running backend used the configured Catalog target. The lesson is to separate test-harness connectivity problems from service design problems.

**Member 1 contribution:** backend startup and routing; SQLite user and catalog schemas; 35-title metadata seed; bcrypt/JWT; Manga REST API; private Library API and versioned progress; Catalog gRPC server/client; authenticated TCP progress server; tests and communication contracts. **Member 2 contribution (to be completed):** tview interface, HTTP/TCP clients, UDP notifications, WebSocket chat, protected release trigger, final cross-protocol integration, and shared report/demo.

## 5. AI interaction record — draft based on actual project collaboration

The course requires three meaningful AI interactions and at least one suggestion that was **checked and revised or rejected**. The following are candidate entries grounded in the work described in the conversation. The team should cross-check them against chat history and the actual Git diffs before submission; do not present code suggestions as validated without testing.

1. **Catalogue preparation:** AI helped draft a metadata-only manga seed and sources document; the developer inspected the bundled set and ran database tests. The seed design stores data persistently in SQLite rather than treating the JSON seed as the live database.
2. **gRPC test troubleshooting:** AI guidance was used to revise a temporary gRPC test resolver after HTTP requests unexpectedly returned `503`. The developer then reran the Manga API tests. Record the exact verified test result if available.
3. **TCP framing/shutdown debugging:** AI proposed a corrected TCP error-frame shutdown approach after the `EOF` failure. This suggestion was checked against `TestTCPFrameValidation`, ten repeated TCP runs, and the whole-project test run; the results shown were passing at that stage. This is the required example of a proposal **verified and modified** following observed behavior.

## 6. Known limitations and next integration tasks

The first-stage backend is not the complete user-facing project. A TUI, UDP subscriptions and chapter alerts, WebSocket discussion rooms, and the admin release endpoint are pending Member 2 implementation. The TCP server currently publishes to all sessions of the same authenticated user, including potentially the initiating session; a TUI can use `manga_id` and `version` to ignore duplicate UI updates. The metadata seed is a demonstration snapshot and does not fetch live release information. The final demonstration should run `cmd/catalog`, `cmd/backend`, and `cmd/tui` as separate processes and provide real evidence of all five protocols.
