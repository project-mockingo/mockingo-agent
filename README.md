# Mockingo CLI

Mockingo exposes an HTTP service running on your computer through a stable public hostname. The CLI starts an optional child process, waits for its local port, and opens an outbound WebSocket connection to the Mockingo gateway. No Dockerfile, router port forwarding, or inbound firewall rule is required.

This repository contains the first working MVP: the `mockingo` CLI, a small reference `mockingo-gateway`, the shared versioned tunnel protocol, and automated tests.

## MVP scope

The MVP supports saved token configuration, one HTTP port per CLI session, optional local process startup, stable name-based hostnames, concurrent buffered HTTP forwarding, clean process-tree shutdown, and tunnel reconnection with exponential backoff.

It does not deploy applications, run containers, forward arbitrary TCP/UDP or application WebSockets, stream uploads or Server-Sent Events, provide a browser UI, or implement a distributed production control plane.

## Build from source

Go 1.23 or newer is required.

```bash
git clone https://github.com/mockingo/mockingo-cli.git
cd mockingo-cli
go build -o bin/mockingo ./cmd/mockingo
go build -o bin/mockingo-gateway ./cmd/mockingo-gateway
```

On Windows, use `bin/mockingo.exe` and `bin/mockingo-gateway.exe`. `make build` performs the same build on systems with Make.

## Run a local gateway

Use development-friendly hostname and scheme settings:

```bash
MOCKINGO_GATEWAY_ADDR=:9090 \
MOCKINGO_BASE_DOMAIN=localhost \
MOCKINGO_PUBLIC_SCHEME=http \
MOCKINGO_DEV_TOKEN=development-token \
go run ./cmd/mockingo-gateway
```

PowerShell equivalent:

```powershell
$env:MOCKINGO_GATEWAY_ADDR = ':9090'
$env:MOCKINGO_BASE_DOMAIN = 'localhost'
$env:MOCKINGO_PUBLIC_SCHEME = 'http'
$env:MOCKINGO_DEV_TOKEN = 'development-token'
go run ./cmd/mockingo-gateway
```

Save the CLI configuration:

```bash
mockingo login --api-url http://localhost:9090 --token development-token
```

Expose an application already listening on port 8080:

```bash
mockingo expose --name demo --http 8080
```

Or start and expose a Java application while preserving every argument boundary:

```bash
mockingo expose \
  --name spring-demo \
  --http 8080 \
  -- java -jar target/application.jar
```

Optional flags are `--cwd`, repeatable `--env KEY=VALUE`, `--startup-timeout`, `--request-timeout`, and `--verbose`.

Windows batch files are launched through `cmd.exe /C` automatically:

```powershell
mockingo expose --name demo --http 8080 --cwd .\application -- .\start-server.cmd --port 8080
```

Test the local hostname route without DNS configuration:

```bash
curl -H "Host: demo.localhost" http://localhost:9090/hello
```

## Architecture

The gateway routes a public request by its `Host` header, sends a versioned JSON message through the CLI's outbound WebSocket, and waits for the matching response ID. The CLI can handle multiple requests concurrently but always forwards to the user-selected `127.0.0.1` port. See [docs/architecture.md](docs/architecture.md) for protocol and lifecycle details and [docs/local-development.md](docs/local-development.md) for a complete local walkthrough.

## Security notes

- API and session tokens are never printed. The saved config is mode `0600` on Unix.
- Session tokens are cryptographically random, scoped to one registration, sent in an authorization header, and never placed in a URL.
- The development gateway uses one environment-provided API token and in-memory state. It is a reference server, not a hardened multi-tenant service.
- TLS is intentionally external. A production deployment must place the gateway behind a TLS-terminating reverse proxy and preserve the original `Host` header.
- Anyone who knows the development API token can register names. There is no RBAC, rate limiting, audit log, or abuse prevention in this MVP.

## Current limitations

Protocol version 1 buffers each request and response body in memory and limits each to 10 MiB. Streaming uploads, streaming responses, SSE, and application WebSocket upgrades are unsupported. Registrations are in memory, disconnected sessions may reconnect for five minutes, and a gateway restart loses all registrations. Windows process shutdown uses the best practical built-in process-tree mechanisms available without CGO.

Planned work after validating the MVP includes streaming protocol frames, stronger identity and authorization, durable/distributed routing state, observability and rate limiting, production gateway deployment guidance, and application WebSocket support.

## Development

```bash
make test
go fmt ./...
go vet ./...
go test ./...
```

Tests require no internet access or external infrastructure after Go modules are downloaded.
