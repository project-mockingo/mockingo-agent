# Tunnel ticket authentication

Stage 3E.1 adds the Spring control plane to the existing Go data plane without changing `mockingo expose` yet.

```text
CLI -> Spring Boot backend -> short-lived RS256 ticket
CLI -> Go gateway /v1/connect
    -> validate ticket against cached backend JWKS
    -> reserve endpointId + sessionId
    -> connected callback
    -> existing tunnel protocol v1
```

## Connection contract

The client sends a WebSocket upgrade to `wss://gateway.mockingo.com/v1/connect` with exactly one `Authorization: Bearer <ticket>` header. Tickets are never accepted in a URL, cookie, query parameter, or WebSocket subprotocol. The gateway accepts RS256 only and requires a protected `kid`.

The verified claims are `iss`, `aud`, `sub`, `jti`, `endpointId`, `endpointName`, `protocol`, `localPort`, `protocolVersion`, `iat`, `nbf`, and `exp`. The issuer, audience, protocol version, and clock skew are configured. Both `jti` (the backend tunnel-session UUID) and `endpointId` must be UUIDs; endpoint names use the existing DNS-label rules; only `http`, ports 1–65535, and protocol version 1 are supported.

The public hostname is always derived as `<endpointName>.mockingo.click`; a ticket cannot provide or override it. If PostgreSQL already contains that endpoint name with another endpoint ID, the gateway rejects the connection instead of binding identities across control planes.

## JWKS lifecycle

When ticket authentication is enabled, startup fetches `MOCKINGO_BACKEND_JWKS_URL` and fails if it cannot load a usable public RSA signing key. Responses are capped at 1 MiB and fetched with a timeout and normal TLS verification. Keys refresh periodically. An unknown `kid` causes one serialized immediate refresh, so concurrent requests do not stampede the backend. A failed refresh retains the last usable keys; readiness remains healthy while a usable cache exists.

## Ticket lifetime and replay protection

A ticket authorizes one connection establishment. Once accepted, the WebSocket may live beyond `exp`; reconnecting requires a new ticket. The process-local replay cache consumes `jti` before upgrade reservation and retains it until `exp` plus clock skew. Concurrent and post-disconnect reuse is rejected. The cache has a configured entry cap and expired-entry cleanup.

The replay cache is in memory. A gateway restart can forget JTIs consumed before the restart; the backend's short 30-second to 5-minute ticket TTL bounds this transitional Stage 3E.1 risk. Redis/global replay coordination is deliberately deferred.

## Compatibility and limitations

`MOCKINGO_LEGACY_TUNNEL_AUTH_ENABLED=true` preserves `/api/v1/tunnels/{id}/connect` for the current CLI and logs a token-free deprecation warning. Legacy and ticket sessions are tagged separately and neither may replace an active endpoint. The CLI migration is Stage 3E.2.

PostgreSQL remains for the transitional legacy management APIs and for persistent public `404 endpoint_not_found` versus `502 tunnel_offline` behavior. Active ticket tunnels route directly from the in-memory hostname index without a database or backend call per public request. The backend-issued claims, not PostgreSQL, authorize ticket connections.

One gateway process owns one active registry and replay cache. Batch status is instance-local; global multi-replica status, replay prevention, and routing require a later coordination layer.

## Configuration

| Variable | Default | Meaning |
|---|---:|---|
| `MOCKINGO_TICKET_AUTH_ENABLED` | `true` | Enable `/v1/connect` ticket authentication |
| `MOCKINGO_BACKEND_URL` | `https://api.mockingo.com` | Callback service base URL |
| `MOCKINGO_BACKEND_JWKS_URL` | backend well-known URL | Tunnel signing JWKS |
| `MOCKINGO_TUNNEL_TICKET_ISSUER` | backend URL | Required `iss` |
| `MOCKINGO_TUNNEL_TICKET_AUDIENCE` | `mockingo-gateway` | Required `aud` |
| `MOCKINGO_TUNNEL_PROTOCOL_VERSION` | `1` | Supported wire protocol |
| `MOCKINGO_JWKS_REFRESH_INTERVAL` | `15m` | Periodic key refresh |
| `MOCKINGO_JWKS_HTTP_TIMEOUT` | `5s` | JWKS request timeout |
| `MOCKINGO_TICKET_CLOCK_SKEW` | `5s` | Time-claim tolerance |
| `MOCKINGO_REPLAY_CACHE_MAX_ENTRIES` | `100000` | Process replay-cache bound |
| `MOCKINGO_LEGACY_TUNNEL_AUTH_ENABLED` | `true` | Transitional legacy tunnel flow |

Production validates all URLs as HTTPS. HTTP is accepted only with `MOCKINGO_ENV=development`.

## Troubleshooting

- Startup `load tunnel ticket JWKS`: verify backend reachability, TLS trust, URL, and that the JWKS contains RSA `sig` keys with non-empty `kid` and RS256 (or no conflicting algorithm).
- `invalid_tunnel_ticket`: check issuer, audience, signature key, UUID claims, time synchronization, and ticket TTL. The response intentionally does not expose which check failed.
- `endpoint_identity_conflict`: the gateway PostgreSQL row for the name has an ID different from the backend ticket.
- `tunnel_session_replayed`: obtain a new ticket; accepted tickets are not reusable.
- `backend_sync_failed`: connected callback retries were exhausted, so the gateway closed and unregistered the new tunnel.
