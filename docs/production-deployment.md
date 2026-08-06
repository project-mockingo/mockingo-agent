# Stage 2A production deployment

## Architecture

```text
Route 53
  |
  | api.mockingo.click
  | *.mockingo.click
  v
Elastic IP / public server
  |
  v
Caddy :443
  |
  v
Mockingo Gateway :9090
  |               |
  |               v
  |           PostgreSQL
  |
  | WSS tunnel
  v
Mockingo CLI
  |
  v
Local application
```

Caddy terminates HTTPS and WSS, preserves `Host`, and proxies over the internal Docker network. PostgreSQL stores only durable endpoint reservations. The gateway keeps temporary tunnel sessions, hashed session tokens, live sockets, and pending HTTP requests in memory.

An endpoint and a tunnel session have different lifetimes. `spring-demo.mockingo.click` remains reserved after the CLI, PostgreSQL, gateway, or server restarts. A restart makes it offline because no socket is persisted. Running `mockingo expose --name spring-demo` creates a new temporary session for the same endpoint. If a session expires during retries, the CLI automatically registers another session and retains the public URL.

## Required environment

Copy `deploy/env.example` to `deploy/.env`, keep it out of version control, and replace every placeholder. Production startup fails unless `MOCKINGO_API_TOKEN` is at least 32 characters, non-example, and HTTPS plus `DATABASE_URL` are configured. Generate the token with a cryptographic password generator, for example `openssl rand -hex 32`.

`MOCKINGO_TRUSTED_PROXY_CIDRS` must cover the immediate Caddy container address range, not the public internet. The Compose example uses a private Docker network, so `172.16.0.0/12` is a practical default; narrow it if the server uses a fixed subnet.

## Migrations and startup

Migrations are explicit and protected with a PostgreSQL advisory lock. The gateway refuses to serve if the schema is behind.

```bash
cd deploy
cp env.example .env
# edit .env
docker compose --env-file .env -f docker-compose.production.yml build
docker compose --env-file .env -f docker-compose.production.yml up -d postgres
docker compose --env-file .env -f docker-compose.production.yml run --rm mockingo-gateway migrate
docker compose --env-file .env -f docker-compose.production.yml up -d
docker compose --env-file .env -f docker-compose.production.yml ps
```

`/health/live` checks only the process. `/health/ready` checks shutdown state, PostgreSQL, and schema version. Compose waits for PostgreSQL and gateway readiness. Containers use `unless-stopped` and named volumes (`postgres_data`, `caddy_data`, and `caddy_config`). On shutdown the gateway rejects registrations, closes tunnels so pending requests fail, drains HTTP, and closes the database pool.

## Operations and smoke test

Build or download the CLI, then:

```bash
mockingo login --api-url https://api.mockingo.click --token "$MOCKINGO_API_TOKEN"
mockingo expose --name smoke-test --http 8080 -- python3 -m http.server 8080
```

From a different network:

```bash
curl -v https://smoke-test.mockingo.click/
mockingo endpoints list
mockingo endpoints delete smoke-test --force
```

Restart validation:

1. Register `restart-test` and confirm a public request succeeds.
2. Stop the CLI; the hostname must return JSON `502 tunnel_offline`.
3. Restart `mockingo-gateway`; the hostname must remain `tunnel_offline`.
4. Run `expose` again; it must report the same hostname and forward successfully.
5. Restart PostgreSQL and the whole server; repeat the offline/online checks.
6. Delete the endpoint; its hostname must return JSON `404 endpoint_not_found`.
7. Register the same name again; it must succeed with a newly created endpoint ID.

Metrics are disabled by default in the process. Compose enables `/metrics` for internal-network scraping while Caddy explicitly blocks the public `/metrics` route. Metrics include active tunnels, registered endpoints, request totals/duration, reconnects, and registration errors without endpoint/session labels.

## Limitations

Stage 2A uses one management API token. It does not implement browser login, OAuth, user registration, multiple users, teams, RBAC, refresh tokens, billing, TCP/UDP, files, remote deployment, container execution, Kubernetes, or a dashboard. Streaming, SSE, and application WebSockets remain unsupported. Stage 2B will introduce browser/OAuth authentication; it is intentionally absent here.
