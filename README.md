# Mockingo CLI

This repository contains only the Go `mockingo` CLI and local tunnel agent.
The data-plane server lives in the separate
`github.com/project-mockingo/mockingo-gateway` repository.

```text
mockingo CLI -- OAuth access token --> https://api.mockingo.com
mockingo CLI <-- short-lived one-use tunnel ticket -- control plane
mockingo CLI -- ticket + protocol v1 --> wss://gateway.mockingo.com/v1/connect
public request --> https://<endpoint-name>.mockingo.click --> localhost
```

## Workflow

```bash
mockingo login
mockingo whoami
mockingo run --name spring-demo --http 8080
mockingo logout
mockingo version
```

`mockingo expose` is deprecated and remains available as an alias for
`mockingo run`, with the same options and behavior. It prints a deprecation
warning to stderr. Use `mockingo version` to print the CLI version without
logging in.

Login uses OAuth Authorization Code Flow with PKCE. Access and refresh tokens
are stored under service `mockingo`, account `oauth:<issuer>:<client-id>`, in
Windows Credential Manager, macOS Keychain, or Linux Secret Service. Owner-only
fallback file storage requires `--allow-insecure-storage` or
`MOCKINGO_ALLOW_FILE_CREDENTIALS=true`.

With no OAuth arguments, `mockingo login` retrieves the issuer, public client
ID, and scopes from `https://api.mockingo.com/.well-known/mockingo-agent.json`.
For a different deployment, pass `--api-url` or set `MOCKINGO_API_URL`; explicit
`--issuer`, `--client-id`, `--scopes`, `MOCKINGO_OAUTH_ISSUER`,
`MOCKINGO_OAUTH_CLIENT_ID`, and `MOCKINGO_OAUTH_SCOPES` values override fields
advertised by the control plane.

`mockingo run` sends the OAuth access token only to the Spring Boot control
plane. It validates returned Gateway URLs, sends the public-tunnel ticket only
to `/v1/connect`, and sends the separate dependency-capture ticket only to
`/v1/dependency-capture/connect`. Every reconnect requests a new backend
session and ticket; tickets are never persisted or logged.

Static gateway tokens, direct gateway endpoint CRUD, direct registration, and
legacy token login/expose modes are not supported.

## Dependency capture and replay

`mockingo run --name <endpoint> --http <port>` starts the public tunnel and,
by default, a loopback-only explicit dependency HTTP/S proxy on
`127.0.0.1:8899`. Use `--dependency-proxy=false` to opt out. To let a Docker
Desktop container reach the proxy, opt in to a non-loopback listener with
`--proxy-bind 0.0.0.0`, then configure the container to use
`http://host.docker.internal:8899`. Non-loopback binding prints a security
warning and should be used only on trusted development networks.

The command prints the proxy address and public local CA certificate path; it
never changes application environment variables, Docker networking,
operating-system proxy settings, or trust stores. See
[`docs/v6-dependency-capture.md`](docs/v6-dependency-capture.md) for setup,
security boundaries, passthrough, and HTTPS limitations. Enabled dependency
replays are delivered over the capture session and matched locally before any
origin connection; see
[`docs/v7-dependency-replay.md`](docs/v7-dependency-replay.md).

## Virtual endpoints

Endpoint origin mode is persistent cloud configuration; the CLI has no `--virtual` mode and never executes mocks. The control plane rejects `mockingo run` for a Virtual endpoint and directs the user to switch it to Local. When a connected Local endpoint is switched to Virtual, the Gateway closes the tunnel with `endpoint_virtualized`; that reason ends the run session without entering the normal reconnect loop. Other transient disconnects keep the existing reconnect behavior.

## Tunnel protocol

The public `tunnelprotocol` package in this module is the canonical Go
definition of the versioned wire contract shared with the private gateway.
Its on-wire version remains 1. The agent expects the external gateway URL
returned by the control plane; the production gateway is
`wss://gateway.mockingo.com/v1/connect`.

For sibling-repository development, use the parent `go.work`:

```text
workspace/
  go.work
  mockingo-agent/
  mockingo-gateway/
```

The gateway imports `github.com/project-mockingo/mockingo-agent/tunnelprotocol`.
No release `go.mod` contains a local filesystem `replace`.

## Build and verify

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/mockingo
make cross-build
```

Release artifacts contain only CLI binaries for Windows, Linux, and macOS.
Release builds embed their Git tag, and `make build` / `make cross-build`
embed the Git description (or an explicit `VERSION=vX.Y.Z` override).
Other builds report Go's embedded module version when available (including
`go install` with a module version), or `dev` when no version is available.
This repository has no gateway command, server routes, PostgreSQL code,
gateway Dockerfile, Caddy configuration, or gateway deployment pipeline.

Protocol v1 supports multiplexed buffered HTTP requests and responses with a
10 MiB body cap. Streaming, SSE, application WebSocket proxying, TCP/UDP
forwarding, and protocol v2 remain out of scope.
