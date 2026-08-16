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
mockingo expose --name shop --http 8080 --wiremock ./examples/hybrid-wiremock/wiremock
mockingo expose --name shop --http 8080 --openapi ./examples/hybrid-openapi/partial-api.yaml
mockingo mock --name weather --wiremock ./examples/wiremock-weather
mockingo mock --name weather --openapi ./examples/openapi-weather/openapi.yaml
mockingo logout
```

## Local mocking

`mockingo mock` compiles WireMock JSON mappings or an OpenAPI 3.x document into
an immutable local mock engine, serves it on an ephemeral `127.0.0.1` port, and
publishes it through the existing authenticated HTTP tunnel. Mock sources stay
on the local machine; neither the control plane nor gateway receives them.

Exactly one source is required:

```bash
mockingo mock --name weather --wiremock ./wiremock
mockingo mock --name weather --openapi ./openapi.yaml
```

Unmatched requests return a JSON `404 mock_not_found`; they are not forwarded
to another local service. See [docs/mocking.md](docs/mocking.md),
[docs/wiremock-compatibility.md](docs/wiremock-compatibility.md), and
[docs/openapi-mocking.md](docs/openapi-mocking.md).

## Hybrid Expose

`mockingo expose` optionally composes the same local mock engine with the real
application already listening on `--http`:

```bash
mockingo expose --name shop --http 8080 --wiremock ./wiremock
mockingo expose --name shop --http 8080 --openapi ./partial-api.yaml
```

Matching WireMock routes or compiled OpenAPI operations return a mock response
directly from the agent. Every unmatched request is forwarded exactly once to
`127.0.0.1:8080`, with its method, path, query, headers, and body preserved.
`--wiremock` and `--openapi` are mutually exclusive; neither is required, so
plain `mockingo expose --name shop --http 8080` remains pure forwarding.

```text
Public Request
      |
Mockingo Tunnel
      |
Agent mock matcher
   +-- MATCH ------> mock response
   +-- NO MATCH ---> localhost:8080
```

This differs deliberately from standalone `mockingo mock`, where an unmatched
request returns `404 mock_not_found` and no local application is involved.
Mock files, match details, and the selected `MOCK`/`FORWARD` route stay local;
the control plane, gateway, and protocol v1 carry ordinary HTTP data only.

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
