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
mockingo expose --name spring-demo --http 8080
mockingo logout
```

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

`mockingo expose` sends the OAuth access token only to the Spring Boot control
plane. It validates the returned gateway URL and sends the returned one-use
tunnel ticket only to `/v1/connect`. Every reconnect requests a new backend
session and ticket; tickets are never persisted or logged.

Static gateway tokens, direct gateway endpoint CRUD, direct registration, and
legacy token login/expose modes are not supported.

## Virtual endpoints

Endpoint origin mode is persistent cloud configuration; the CLI has no `--virtual` mode and never executes mocks. The control plane rejects `mockingo expose` for a Virtual endpoint and directs the user to switch it to Local. When a connected Local endpoint is switched to Virtual, the Gateway closes the tunnel with `endpoint_virtualized`; that reason ends the expose session without entering the normal reconnect loop. Other transient disconnects keep the existing reconnect behavior.

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
This repository has no gateway command, server routes, PostgreSQL code,
gateway Dockerfile, Caddy configuration, or gateway deployment pipeline.

Protocol v1 supports multiplexed buffered HTTP requests and responses with a
10 MiB body cap. Streaming, SSE, application WebSocket proxying, TCP/UDP
forwarding, and protocol v2 remain out of scope.
