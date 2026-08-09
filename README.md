# Mockingo CLI and gateway

This repository contains the Go CLI/agent and the Go data-plane gateway.

```text
mockingo CLI -- Clerk OAuth --> api.mockingo.com -- tunnel ticket --> gateway.mockingo.com
gateway.mockingo.com -- active tunnel --> <name>.mockingo.click
```

Spring Boot is the only control plane. It validates Clerk access tokens, owns endpoint CRUD and ownership, writes PostgreSQL, creates tunnel sessions, and signs short-lived tunnel tickets. The gateway verifies those tickets, keeps the active in-memory registry, proxies public traffic, sends lifecycle callbacks, and exposes authenticated internal status/disconnect APIs.

## CLI workflow

```bash
mockingo login
mockingo expose --name spring-demo --http 8080
mockingo logout
```

`mockingo login` uses OAuth Authorization Code Flow with PKCE. Access and refresh tokens are stored under service `mockingo`, account `oauth:<issuer>:<client-id>`, in Windows Credential Manager, macOS Keychain, or Linux Secret Service. Owner-only fallback file storage requires `--allow-insecure-storage` or `MOCKINGO_ALLOW_FILE_CREDENTIALS=true`.

`mockingo expose` sends the OAuth access token only to the configured Spring Boot API. It validates the returned gateway hostname and sends the returned one-use tunnel ticket only to `/v1/connect`. Reconnect always requests a new backend session and ticket.

If no OAuth session is available, the CLI reports:

```text
You are not signed in to Mockingo.

Run:
  mockingo login
```

## Removed legacy authentication

Static gateway tokens, direct gateway endpoint CRUD, direct tunnel registration, and static-token CLI login/expose modes are no longer supported. Removed flags are rejected as unknown options. Old token fields in config files are ignored and are dropped on the next login, logout, or other config save; valid OAuth metadata and OAuth credentials are preserved. Logout also removes the configured OAuth credential and cleans old config fields without printing their values.

## Gateway routes

The gateway exposes only:

| Route | Responsibility |
| --- | --- |
| `GET /v1/connect` | WebSocket connection authenticated by one backend-issued ticket |
| `GET /health/live` | Liveness |
| `GET /health/ready` | Readiness, JWKS, and catalog connectivity |
| `GET /metrics` | Optional operator metrics; normally blocked at the public edge |
| `POST /internal/v1/tunnels/status` | Backend batch status lookup |
| `POST /internal/v1/tunnels/{endpointId}/disconnect` | Backend-requested idempotent disconnect |
| `https://*.mockingo.click/*` | Public tunnel proxy |

All other requests on `gateway.mockingo.com`, including old management paths, return `404`.

## Transitional PostgreSQL catalog

The gateway temporarily reads endpoint existence from PostgreSQL only to preserve public `404 endpoint_not_found` versus `502 tunnel_offline` semantics. It does not own migrations, create/update/delete endpoints, evaluate ownership, or open long transactions. The query is bounded and uses `SELECT EXISTS` by hostname.

The gateway database role should have only:

```sql
GRANT CONNECT ON DATABASE mockingo TO mockingo_gateway_reader;
GRANT USAGE ON SCHEMA public TO mockingo_gateway_reader;
GRANT SELECT (hostname) ON TABLE endpoints TO mockingo_gateway_reader;
```

Complete PostgreSQL removal requires a separate backend-to-gateway endpoint catalog design and is intentionally deferred.

## Development and verification

Configure a development backend/JWKS endpoint and the two distinct service credentials, then run:

```bash
go run ./cmd/mockingo-gateway serve
go run ./cmd/mockingo login
go run ./cmd/mockingo expose --expected-gateway-host localhost --allow-insecure-gateway --name spring-demo --http 8080
```

Common checks:

```bash
go test ./...
go test -race ./...
go vet ./...
make cross-build
docker build -f deploy/Dockerfile.gateway -t mockingo-gateway:local .
```

See [deployment documentation](docs/production-deployment.md), [ticket authentication](docs/tunnel-ticket-auth.md), [backend callbacks](docs/backend-callbacks.md), and [internal APIs](docs/gateway-internal-api.md).

## Current protocol limits

Protocol v1 supports multiplexed HTTP requests with request and response bodies capped at 10 MiB. Streaming, SSE, application WebSocket proxying, TCP/UDP forwarding, protocol v2, and multi-replica active-session coordination remain out of scope.
