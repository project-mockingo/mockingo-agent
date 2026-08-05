# Local development

## Prerequisites

- Go 1.23 or newer
- `make` is optional; every Make target maps to a documented Go command
- Any local HTTP server for manual testing

## Automated verification

```bash
go mod download
go fmt ./...
go vet ./...
go test ./...
go build ./cmd/mockingo
go build ./cmd/mockingo-gateway
```

The integration test creates random local listeners, runs the gateway and agent in process, forwards concurrent HTTP requests, and verifies the disconnected `502` state.

## Complete local demo

Terminal 1, the gateway:

```bash
MOCKINGO_BASE_DOMAIN=localhost \
MOCKINGO_PUBLIC_SCHEME=http \
MOCKINGO_DEV_TOKEN=development-token \
go run ./cmd/mockingo-gateway
```

Terminal 2, save credentials and expose an existing service:

```bash
go run ./cmd/mockingo login \
  --api-url http://localhost:9090 \
  --token development-token

go run ./cmd/mockingo expose --name demo --http 8080
```

Or let Mockingo start the application:

```bash
go run ./cmd/mockingo expose \
  --name spring-demo \
  --http 8080 \
  -- java -jar target/application.jar
```

Terminal 3, issue a public-side request through the local gateway:

```bash
curl -i -H "Host: demo.localhost" http://localhost:9090/hello?value=1
```

Stop the CLI with Ctrl+C. If it started the application, it also terminates the child process tree. If the gateway briefly disappears, leave the CLI running; it reconnects with exponential backoff while keeping the same hostname.

## Gateway environment

| Variable | Default | Purpose |
|---|---|---|
| `MOCKINGO_GATEWAY_ADDR` | `:9090` | Listen address |
| `MOCKINGO_BASE_DOMAIN` | `mockingo.click` | Hostname routing suffix |
| `MOCKINGO_DEV_TOKEN` | `development-token` | Development API bearer token |
| `MOCKINGO_PUBLIC_SCHEME` | `https` | Public URL and forwarded scheme |
