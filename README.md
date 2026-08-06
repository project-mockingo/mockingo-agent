# Mockingo CLI

Mockingo exposes an HTTP service on your computer through a stable public hostname. Stage 2A adds persistent PostgreSQL endpoint reservations and a production Caddy/Route 53 deployment while retaining the outbound WebSocket tunnel and local-development workflow.

```text
Internet client -> HTTPS Caddy -> Mockingo Gateway -> WSS CLI -> 127.0.0.1:8080
                                      |
                                      +-> PostgreSQL endpoint reservations
```

## Build and test

Go 1.23 or newer is required.

```bash
make build
make test
make vet
make cross-build
```

PostgreSQL integration tests use `TEST_DATABASE_URL`:

```bash
TEST_DATABASE_URL='postgres://mockingo:test-password@localhost:5432/mockingo_test?sslmode=disable' make test-integration
```

## Public usage

```bash
mockingo login --api-url https://api.mockingo.click --token "$MOCKINGO_API_TOKEN"
mockingo expose --name spring-demo --http 8080 -- java -jar target/application.jar
curl https://spring-demo.mockingo.click/hello
```

Stopping `expose` takes the tunnel offline but does not release `spring-demo.mockingo.click`. Manage durable reservations with:

```bash
mockingo endpoints list
mockingo endpoints list --json
mockingo endpoints delete spring-demo
mockingo endpoints delete spring-demo --force
```

## Local development

```bash
export MOCKINGO_ENV=development
export MOCKINGO_BASE_DOMAIN=localhost
export MOCKINGO_PUBLIC_SCHEME=http
export MOCKINGO_API_PUBLIC_URL=http://localhost:9090
export MOCKINGO_DEV_TOKEN=development-token
go run ./cmd/mockingo-gateway
```

In another terminal:

```bash
go run ./cmd/mockingo login --api-url http://localhost:9090 --token development-token
go run ./cmd/mockingo expose --name spring-demo --http 8080 -- python3 -m http.server 8080
curl -H 'Host: spring-demo.localhost' http://localhost:9090/
```

Development mode uses an in-memory endpoint repository when `DATABASE_URL` is absent. Set `DATABASE_URL` and run `go run ./cmd/mockingo-gateway migrate` to exercise persistence locally.

## Production deployment

The production stack is in `deploy/` and contains PostgreSQL, the gateway, and a custom Caddy build with the Route 53 DNS module. Only Caddy publishes ports. Start with [deploy/README.md](deploy/README.md), then read [production deployment](docs/production-deployment.md), [Route 53 DNS](docs/route53-dns.md), and [security](docs/security.md).

## Current limits

Protocol v1 buffers request and response bodies and caps each at 10 MiB. It supports concurrent HTTP request multiplexing, but not streaming, SSE, application WebSocket proxying, TCP/UDP forwarding, remote folders, container execution, or a dashboard. Stage 2A is a single-owner API-token installation. Browser/OAuth identity, users, teams, and RBAC are planned for Stage 2B.
