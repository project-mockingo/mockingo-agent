# Architecture

```text
mockingo CLI
  | Clerk OAuth access token
  v
api.mockingo.com (Spring Boot control plane)
  | short-lived, one-use tunnel ticket
  v
gateway.mockingo.com/v1/connect (Go data plane)
  | active in-memory session
  v
<name>.mockingo.click
```

## Responsibility split

`mockingo-backend` validates Clerk tokens, owns endpoint CRUD and ownership, performs Flyway migrations and PostgreSQL writes, creates tunnel sessions, and signs tickets.

`mockingo-gateway` verifies tickets with cached backend JWKS, enforces replay and active-session collision rules, maintains the live registry, proxies public HTTP, sends connected/disconnected/rejected callbacks, and serves internal status/disconnect APIs.

The CLI performs OAuth login, calls backend APIs with OAuth access tokens, obtains a fresh backend session for each connection/reconnect, and sends the tunnel ticket only to the backend-selected gateway URL.

The gateway retains `endpointId`, `sessionId`, verified `ownerUserId`, endpoint name/hostname, protocol, local port, protocol version, and connection time in memory. Owner identity is operational metadata only and is not used for endpoint CRUD or ownership decisions.

## Persistence transition

Spring Boot owns the schema and all writes. The gateway has a narrow SELECT-only endpoint catalog so an inactive known hostname returns `502 tunnel_offline` while an unknown hostname returns `404 endpoint_not_found`. Active traffic uses the in-memory hostname index and does not query PostgreSQL or the backend.

Gateway packages already aligned for repository extraction are `internal/gateway`, `internal/gateway/ticketauth`, `internal/gateway/backendcallback`, `internal/gatewayconfig`, `internal/endpoint` (transitional catalog), and `pkg/tunnelprotocol`. Extraction still requires deciding module boundaries, deployment ownership, shared wire-protocol versioning, and replacement/removal of the PostgreSQL catalog.
