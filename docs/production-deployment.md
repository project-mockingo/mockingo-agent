# Production deployment

Deploy Spring Boot as the control plane at `api.mockingo.com` and the Go gateway as the data plane at `gateway.mockingo.com`. Caddy routes only `/v1/connect`, `/internal/*`, and health endpoints on the gateway host; wildcard `*.mockingo.click` traffic goes to the public proxy. `api.mockingo.com` must route only to Spring Boot.

Copy `deploy/env.example` to an untracked `.env`. Configure HTTPS backend/JWKS/issuer URLs, audience, a stable gateway instance ID, distinct callback and internal service tokens, base/gateway domains, trusted proxy CIDRs, and a PostgreSQL reader URL. No user/static gateway token is configured.

Spring Boot runs Flyway. Do not run a gateway migration command: none exists. Provision the gateway role with only database connect, schema usage, and `SELECT (hostname)` on `endpoints`. The gateway performs `SELECT EXISTS` with a three-second timeout and makes no persistent writes.

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.production.yml config --quiet
docker compose --env-file deploy/.env -f deploy/docker-compose.production.yml up -d
```

Verify `/health/live`, `/health/ready`, a ticket-authenticated WebSocket, public online/offline/unknown behavior, internal status/disconnect with the correct service token, and generic rejection with any other token. Confirm old `/api/v1/tunnels` and `/api/v1/endpoints` paths return `404`.

Ticket replay and active status are process-local; multi-replica coordination is a later stage.
