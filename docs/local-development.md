# Local development

Run PostgreSQL and the sibling Spring Boot backend first. The backend owns Flyway and creates endpoint/tunnel-session data. Give the gateway the same database with a SELECT-only role, backend/JWKS URLs, ticket issuer/audience, and distinct callback/internal tokens.

Example gateway environment:

```text
MOCKINGO_ENV=development
MOCKINGO_GATEWAY_ADDR=:9090
MOCKINGO_BASE_DOMAIN=localhost
MOCKINGO_GATEWAY_HOST=localhost
MOCKINGO_PUBLIC_SCHEME=http
MOCKINGO_BACKEND_URL=http://localhost:8080
MOCKINGO_BACKEND_JWKS_URL=http://localhost:8080/.well-known/mockingo-tunnel-jwks.json
MOCKINGO_TUNNEL_TICKET_ISSUER=http://localhost:8080
MOCKINGO_TUNNEL_TICKET_AUDIENCE=mockingo-gateway
MOCKINGO_BACKEND_CALLBACK_TOKEN=test-only-callback-token
MOCKINGO_GATEWAY_INTERNAL_TOKEN=test-only-internal-token
MOCKINGO_GATEWAY_INSTANCE_ID=gateway-local
DATABASE_URL=postgres://mockingo_gateway_reader:password@localhost:5432/mockingo?sslmode=disable
```

Then run:

```bash
go run ./cmd/mockingo-gateway serve
go run ./cmd/mockingo login
go run ./cmd/mockingo expose --expected-gateway-host localhost --allow-insecure-gateway --name spring-demo --http 8080
```

Reconnect requests a fresh backend tunnel session and ticket. There is no direct gateway registration or fallback authentication mode.
