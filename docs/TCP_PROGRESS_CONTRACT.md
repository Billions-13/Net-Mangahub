# MangaHub TCP Progress Contract (Member 1)

## Endpoint and owner

- Default address: `127.0.0.1:9090` for local demo; configurable via `TCP_ADDR` when wiring `cmd/backend`.
- Backend ownership: `internal/tcp/server/`.
- Frontend ownership: TUI client to be implemented by Member 2.
- Frame encoding: **one UTF-8 JSON object followed by `\n` per TCP message**. TCP is a stream, not a message/datagram protocol.
- Maximum inbound frame size: 8192 bytes. First frame must arrive within 10 seconds.

## Authentication

The first line sent by the TUI client must be:

```json
{"type":"auth","token":"<JWT from POST /api/auth/login>"}
```

On success, the server replies:

```json
{"type":"authenticated","user_id":1}
```

On failure, the server replies with `{"type":"error","error":"invalid or expired token"}` or `{"type":"error","error":"authentication required"}`, then disconnects. A JWT must be signed using the same `JWT_SECRET`, issuer `mangahub`, HS256, and a valid expiry. The user must still exist in Backend SQLite.

## Progress event

After `PUT /api/library/:mangaId/progress` commits a changed chapter/status, the Library HTTP handler invokes the injected `library.ProgressNotifier`:

```json
{"type":"progress_updated","manga_id":"one-piece","chapter":25,"version":2}
```

The TCP server broadcasts the event only to TCP connections authenticated as that user. The event contains no private `user_id`. The `version` comes from SQLite and increments on successful changes; unchanged requests emit no event.

**Origin limitation:** The current `library.ProgressEvent` does not carry an originating TCP session identifier. Consequently, the initiating device may receive its own event in addition to other sessions of the same user. The TUI should suppress redundant UI updates by comparing `manga_id` and `version`. Exact origin-session exclusion requires an explicit cross-protocol session identifier agreed upon during integration. It is not correct to exclude an arbitrary connection.

## Keepalive and failure handling

The client may send `{"type":"ping"}\n` and receives `{"type":"pong"}\n`.

Unknown or malformed messages are rejected and the connection is closed. Closed/slow TCP clients are removed and cannot block HTTP progress updates. The TCP server does not persist events; after reconnect, the TUI must refresh the canonical library state with `GET /api/library`.

## Wiring at Commit 10

Construct one TCP server using the Backend SQLite connection and the same JWT secret as `auth.NewHandler`:

```go
tcpServer, err := server.NewServer(backendDB, cfg.JWTSecret)
if err != nil { /* handle error */ }
listener, err := net.Listen("tcp", cfg.TCPAddr)
if err != nil { /* handle error */ }
go func() { /* handle tcpServer.Serve(listener) error */ }()
// Inject tcpServer into httpapi.NewLibraryHandler(backendDB, catalogClient, timeout, tcpServer).
// Call tcpServer.Shutdown(ctx) before closing backendDB on process exit.
```

This is an integration example, not executable standalone code. Full startup wiring belongs to `cmd/backend/main.go` (Commit 10).
