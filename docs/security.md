# Stage 2A security model

Stage 2A is a single-owner service. Every management call uses `Authorization: Bearer <MOCKINGO_API_TOKEN>`. Production rejects missing, short, default, or obvious example tokens. The development token is considered only when `MOCKINGO_ENV=development` or `MOCKINGO_ALLOW_DEV_TOKEN=true`. Token comparisons use constant-time hash comparison.

Management and tunnel credentials are separate. A tunnel session token is 256 bits of cryptographic randomness, returned once, transmitted only in a WebSocket authorization header, and held only as a SHA-256 hash in gateway memory. Tokens never appear in URLs. Endpoint rows contain no credentials.

The gateway emits structured logs with method, host, URL path (never query parameters), status, duration, and peer IP. It does not log authorization headers, cookies, bodies, database URLs, AWS secrets, management tokens, or session tokens. Generated JSON errors use `application/json`, `Cache-Control: no-store`, and `X-Content-Type-Options: nosniff`.

Host routing accepts exactly one ASCII label under `MOCKINGO_BASE_DOMAIN`. It strips a syntactically valid optional port and rejects IPs, root domains, nested subdomains, internationalized names, malformed hosts, and reserved names such as `api`. Database unique constraints are authoritative for concurrent reservations.

Caddy is the only publicly published service. PostgreSQL and the gateway are reachable only on the internal Docker network. The gateway ignores caller-provided `Forwarded` and `X-Forwarded-*` headers unless its immediate peer belongs to `MOCKINGO_TRUSTED_PROXY_CIDRS`; it then propagates only sanitized values. Caddy preserves the original `Host` and handles HTTPS/WSS.

The CLI config directory and file use restrictive permissions (`0700` and `0600` on Unix). Protect the production server, `.env`, PostgreSQL backups, Caddy state, and the API token with normal host hardening and secret rotation practices. Deleting an endpoint closes its active socket, invalidates its temporary session, and removes the database row; disconnecting alone never deletes it.
