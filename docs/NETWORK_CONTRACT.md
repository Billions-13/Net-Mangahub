# MangaHub Network Contract — Member 1 / Member 2 Handoff

## 1. Architecture and ports

```text
               HTTP JSON/Bearer JWT        Gin backend :8080
Member 2 TUI ---------------------------> Auth + Manga + Library API
        |                                         |
        +-- TCP JSON-lines :9090 ----------------+
        +-- UDP JSON datagrams :9091 [Member 2] -+
        +-- WebSocket /ws :8080 [Member 2] ------+
                                                  |
                                            Catalog gRPC :9092
                                                  |
                                             Catalog SQLite

        Backend SQLite: users + user_library (owned only by Backend process)
        Catalog SQLite: manga metadata (owned only by Catalog process)
```

Addresses are configured through `.env`: `HTTP_ADDR`, `TCP_ADDR`, `UDP_ADDR`, `GRPC_ADDR`, `GRPC_TARGET`. The first two listeners are part of the Backend process; the Catalog gRPC server is **another process**. Member 2 will add UDP and WebSocket to the Backend process, and a separate `cmd/tui` process.

## 2. REST/HTTP

The REST contract, request JSON, response wrappers, authorization and status codes are defined in [`API_CONTRACT.md`](API_CONTRACT.md). Private Library requests require `Authorization: Bearer <JWT>`. The user's ID comes from verified JWT claims, not client JSON. Cross-user library access is prohibited.

## 3. TCP progress synchronization (implemented server; client pending)

- Address: `TCP_ADDR` (`:9090` by default; use `127.0.0.1:9090` for local demo).
- Encoding: **UTF-8 JSON, one object followed by a newline (`\n`)** per frame. TCP is a byte stream, so do not assume 1 `Read` == 1 message.
- Maximum inbound frame: **8192 bytes**; initial authentication deadline **10 seconds**.
- Send **auth first**:

```json
{"type":"auth","token":"<JWT returned by HTTP login>"}
```

Server success:

```json
{"type":"authenticated","user_id":1}
```

If missing/invalid/expired token, server sends an error frame and closes the connection:

```json
{"type":"error","error":"invalid or expired token"}
```

The library HTTP handler publishes an event after its SQLite transaction commits a changed progress:

```json
{"type":"progress_updated","manga_id":"one-piece","chapter":25,"version":2}
```

The TCP Server pushes only to currently connected sessions authenticated as the **same** user. **It may also send to the originating session**; exact sender exclusion is not yet available because the HTTP request does not include a cross-protocol TCP session ID. The TUI should ignore duplicates with the same `manga_id` and `version`.

Optional ping/pong:

```json
{"type":"ping"}
```

```json
{"type":"pong"}
```

A client should handle disconnect, EOF, malformed frames, and reconnect. TCP pushes are not stored as a queue. **After reconnect, always call `GET /api/library` to retrieve authoritative state**. Slow clients may be disconnected to protect HTTP performance. See [`TCP_PROGRESS_CONTRACT.md`](TCP_PROGRESS_CONTRACT.md) for extra implementation details.

## 4. gRPC Catalog (implemented)

Protocol: protobuf over gRPC; the Catalog service runs independently at `GRPC_ADDR`, and the Gin backend uses `GRPC_TARGET`. Current proto file: `proto/manga.proto`; committed generated Go stubs: `proto/mangapb/manga.pb.go` and `proto/mangapb/manga_grpc.pb.go`.

Service: `mangahub.catalog.v1.CatalogService`.

| Unary RPC | Request | Response | Behavior |
|---|---|---|---|
| `SearchManga` | `query` string | `repeated Manga manga` | Case-insensitive SQLite search by title/author/genres/ID, max 100 results |
| `GetManga` | `id` string | `Manga manga` | Full metadata by exact ID |
| `UpdateChapters` | `id`, `total_chapters` int32 | `Manga manga` | New count must be nonnegative and **must not decrease** |

The `Manga` message contains `id`, `title`, `author`, repeated `genres`, `status`, `total_chapters`, `description`. `UpdateChapters` is available in Catalog now; **the protected HTTP admin-release route is not yet added**.

Selected gRPC status codes: `InvalidArgument` (invalid request), `NotFound` (unknown manga), `FailedPrecondition` (chapter decrease), `Unavailable` (DB/service failure), `DeadlineExceeded` (timeout), `Canceled` (request canceled). Gin maps Catalog unavailability/deadlines to HTTP `503`.

Gin uses **3-second RPC deadlines** for Manga and Library handlers. The Catalog startup performs seeding (only if the database is empty). The server registers gRPC health and reflection for diagnostics.

For regenerate-from-proto (only when `proto/manga.proto` changes):

```powershell
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
$env:Path += ";$(go env GOPATH)\bin"
protoc --proto_path=. --go_out=. --go_opt=module=mangahub --go-grpc_out=. --go-grpc_opt=module=mangahub proto/manga.proto
```

Do not hand-edit generated `.pb.go` files. Generated files must remain committed so the project builds without `protoc`.

## 5. Member 2 extension points (planned, not live)

- `cmd/backend/extensions.go`: hook for adding WebSocket, UDP listener, and protected admin release without replacing Member 1 core routes.
- **UDP** (default `:9091`): JSON per datagram with `subscribe`, `unsubscribe`, and `new_chapter` messages; Member 2 must define and document exact keys, authentication and behavior in a new UDP contract. UDP delivery is best-effort.
- **WebSocket** (`GET /ws`, same HTTP listener `:8080`): authenticated chat, rooms, join, send, broadcast, disconnect cleanup; Member 2 must define exact JSON frames, membership, and error behavior.
- **TUI** (`go run ./cmd/tui`): after login, retains JWT in session; uses the current HTTP/TCP contracts, and Member 2's UDP/WS contracts; network goroutines must update tview widgets via its UI event loop.

Do not assume specific fields for Member 2 UDP/WS messages until their final contract is added. Do not access either SQLite database directly from the TUI.

## 6. Security and failure behavior

- Both HTTP auth and TCP validate the **same JWT secret** and issuer `mangahub`.
- Library data is restricted by user ID from validated JWT; SQL queries use parameters.
- HTTP progress events follow the successful SQLite commit, not the initial request.
- The demo defaults are **not production security**: local gRPC uses insecure transport, and HTTP/TCP do not enable TLS. Bind to loopback for local demos and do not forward public router ports.
- If Catalog stops, HTTP manga/search/update requests must return a bounded controlled error, rather than hang; reconnect by restarting Catalog.
