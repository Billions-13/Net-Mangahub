# Member 1 Test Checklist and Evidence Log

**Rule:** Mark a case PASS only after actually running it or obtaining an explicit passing test run. Keep evidence in the final report. The statements below distinguish earlier observed results from checks still required at final handoff.

## 1. Commands

```powershell
go test -v -count=1 ./cmd/backend
go test -count=1 ./...
```

Optional repeated TCP reliability check:

```powershell
go test -count=10 ./internal/tcp/server
```

These commands create temporary databases and listeners; they do not require the live Catalog/Backend processes to be running.

## 2. Recorded earlier in the development conversation

- **TCP module test**: shown passing (`PASS`, `ok`) after fixing an EOF/framing test case.
- **TCP repeated 10 times**: shown successful (`go test -count=10 ./internal/tcp/server`).
- **Full project test before the separate end-to-end test was added**: shown passing in the same terminal screenshot (`go test -count=1 ./...`).
- **Live backend smoke checks**: screenshot showed HTTP 200 for `GET /api/health`, `GET /api/manga`, and `GET /api/manga?q=one+piece`, with the expected health and Manga JSON outputs.

These are **development observations, not a claim that all final release tests have been rerun**. The latest added `cmd/backend/main_test.go` and final Git state must be verified again after Commit 11/12.

## 3. Final handoff verification matrix

| Test | Command / method | Expected result | Final observed |
|---|---|---|---|
| Compile all packages | `go test -count=1 ./...` | No FAIL | TO VERIFY |
| Backend integration | `go test -v -count=1 ./cmd/backend` | TestBackendEndToEnd PASS | TO VERIFY |
| TCP reliability | `go test -count=10 ./internal/tcp/server` | All iterations PASS | Previously PASS; rerun optional |
| Health | GET `/api/health` | HTTP 200 | Previously 200 |
| Manga list | GET `/api/manga` | HTTP 200; seeded records | Previously 200 |
| Manga search | GET `/api/manga?q=one%20piece` | HTTP 200, One Piece | Previously 200 |
| Register | POST `/api/auth/register` | 201; user object | TO VERIFY |
| Login | POST `/api/auth/login` | 200; JWT | TO VERIFY |
| Wrong password | POST `/api/auth/login` | 401 | TO VERIFY |
| Missing/invalid JWT | GET `/api/library` | 401 | TO VERIFY |
| Add library entry | POST `/api/library` | 201, version 1 | TO VERIFY |
| Duplicate library entry | repeat POST | 409 | TO VERIFY |
| Change progress | PUT `/api/library/one-piece/progress` | 200, version increments | TO VERIFY |
| Over-limit chapter | PUT progress with excessive value | 400; no corruption | TO VERIFY |
| Stale progress version | PUT with old `version` | 409 | TO VERIFY |
| User isolation | separate Alice and Bob users | No data leak | TO VERIFY |
| TCP auth/event | send JSON lines over TCP | Same-user receives event | TO VERIFY |
| TCP cross-user isolation | 2 accounts | Different account receives none | TO VERIFY |
| gRPC outage | stop Catalog then HTTP search | 503, not hang | TO VERIFY |
| Restart persistence | stop/restart both services | User/library/catalog persisted | TO VERIFY |

The automated `TestBackendEndToEnd` covers many negative cases, gRPC outage, TCP sessions, and SQLite persistence; **a passing test provides test evidence** for those cases, but live manual checks are still useful for the final demonstration.

## 4. Troubleshooting / fixes actually encountered

1. During Manga REST testing, requests returned HTTP `503` because the test gRPC dial target used the wrong resolver prefix. The test setup was corrected to `passthrough:///...`; the report should distinguish the test harness from the production Catalog target.
2. During TCP tests, `TestTCPFrameValidation` failed with `read TCP frame: EOF` on Windows. The TCP server's final error-frame close behavior was revised. Follow-up screenshots showed individual TCP test PASS, 10 repeated TCP test passes, and whole-project tests passing **at that stage**.

These two examples are appropriate **real debugging evidence** if the team explains the before/after behavior and includes authentic terminal output.

## 5. Evidence to save before the final report

- Screenshot: Catalog startup with count of 35 manga and gRPC listener.
- Screenshot: Backend startup with 8080 and 9090 listeners.
- Screenshot: test output for `go test -count=1 ./...`, preferably after final merge.
- Screenshot/record: two same-user TCP clients and a different-user client during a progress update.
- Screenshot/record: expected HTTP 503 after stopping the Catalog service.
- Screenshot/record: a persisted library row after process restart.

Only mark the evidence as collected after capturing it; do not make up request IDs, timestamps, passwords, or screenshots.
