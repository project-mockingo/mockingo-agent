# Stage 3E.1 gateway production deployment

## Architecture

```text
Route 53
  |
  | gateway.mockingo.com
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

Caddy terminates HTTPS and WSS, preserves `Host`, and proxies over the internal Docker network. PostgreSQL stores durable transitional endpoint reservations. The gateway keeps legacy temporary sessions, consumed ticket JTIs, live sockets, and pending HTTP requests in memory. `api.mockingo.com` is the separately deployed Spring Boot control plane.

An endpoint and a tunnel session have different lifetimes. `spring-demo.mockingo.click` remains reserved after the CLI, PostgreSQL, gateway, or server restarts. A restart makes it offline because no socket is persisted. Running `mockingo expose --name spring-demo` creates a new temporary session for the same endpoint. If a session expires during retries, the CLI automatically registers another session and retains the public URL.

## Required environment

Copy `deploy/env.example` to `deploy/.env`, keep it out of version control, and replace every placeholder. Production startup validates HTTPS backend, JWKS, callback, and gateway URLs; non-empty callback/internal tokens; a stable instance ID; a strong legacy management token; and `DATABASE_URL`. Generate service tokens with a cryptographic password generator, for example `openssl rand -hex 32`.

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

`/health/live` checks only the process. `/health/ready` checks shutdown state, PostgreSQL/schema state, and that ticket authentication has usable cached JWKS. A later JWKS refresh failure does not evict cached keys. On shutdown the gateway stops accepting tunnels, gracefully closes sockets, unregisters them, sends bounded disconnected callbacks, drains HTTP, and closes the database pool.

## Operations and smoke test

Build or download the CLI, then:

```bash
mockingo login --api-url https://gateway.mockingo.com --token "$MOCKINGO_API_TOKEN"
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

Metrics are disabled by default in the process. Compose enables `/metrics` for internal-network scraping while Caddy explicitly blocks it publicly. Metrics cover active/total tunnels, ticket validation/replay, JWKS refresh, lifecycle callbacks, internal requests, registered endpoints, request duration, reconnects, and registration errors without identity labels.

## Limitations

Ticket replay and status are process-local, so multi-replica/global coordination is not implemented. The CLI still uses the legacy registration flow until Stage 3E.2. Streaming, SSE, application WebSockets, TCP forwarding, and protocol v2 remain unsupported.
