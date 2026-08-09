# Stage 3E.4 cleanup audit

This document records the pre-removal inventory, match classification, and verification for the control-plane/data-plane cleanup.

## Ownership confirmation

The sibling `../mockingo-backend` repository was inspected read-only. Its Flyway `V1` migration owns the endpoint table baseline, later migrations add endpoint ownership/metadata and tunnel sessions, `EndpointService` performs owned create/list/get/update/delete operations, and `TunnelSessionService` reserves/reuses endpoints, persists sessions, and invokes `TunnelTicketService` to sign the gateway ticket. No backend or frontend files were modified.

## Match classification

Removed from production code and configuration:

- CLI token login fields/flags and direct gateway endpoint list/delete commands.
- Expose compatibility flag/environment switch, direct registration client, gateway-issued session secrets, and re-registration state machine.
- Gateway management/API token settings, development-token defaults, authentication middleware, management DTOs/handlers, direct tunnel registration/connect paths, and endpoint CRUD routes.
- Gateway endpoint create/list/get/delete repository methods, migration runner/command/files, schema-version readiness check, and write-oriented integration fixtures.
- Active-tunnel authentication-method enum/field, old metrics labels, and callback exclusions.
- Deployment/CI/Compose/Caddy examples and routes for the removed surface.

Replaced:

- The write-capable endpoint repository became a bounded `EndpointCatalog`-equivalent interface with `ExistsByHostname`, `Ping`, and `Close` only.
- PostgreSQL access became one `SELECT EXISTS` lookup used only when no live hostname is present.
- Registration/CRUD tests became negative `404` route tests, invalid-ticket tests, OAuth-only flag/config migration tests, and SELECT-only catalog tests.

Retained temporarily:

- `DATABASE_URL`, `pgx`, and `internal/endpoint` remain only for public unknown/offline semantics. The production role must have endpoint-hostname SELECT permission only.
- `internal/auth.BearerMatches` remains for the distinct backend-to-gateway internal API credential and continues constant-time comparison.
- Backend callback and gateway internal tokens remain distinct service credentials.
- Ticket JWT/JWKS/replay code, the active session registry, wire protocol v1, public proxy, callbacks, graceful shutdown, and reconnect remain unchanged in responsibility.

Documentation-only history and isolated removal fixtures:

- The phrase `Removed legacy authentication` explains the breaking removal without publishing an old token format.
- Tests containing `legacyApiUrl`, `legacyToken`, removed environment names, or removed route strings verify safe migration, ignored settings, unknown flags, `404`, and `401`. Literal values are explicitly test-only.
- OAuth `TokenAuthMethods` identifiers describe authorization-server metadata and are unrelated to the removed gateway credential flow.

No other production match for the requested removed names remains.

## Credential/config cleanup

Old JSON fields are ignored by decoding into the OAuth-only schema. Login/config saves rewrite only current fields. Logout deletes OAuth credentials, clears OAuth metadata, and rewrites or removes the config so ignored token fields disappear without being decoded or printed. Repository history shows the old static credential was stored only in `config.json`; it was not written to the OS keyring or OAuth fallback directory, so no ambiguous shared keyring record needs destructive deletion.

## Route inventory

Service-host routes are `/v1/connect`, `/health/live`, `/health/ready`, optional `/metrics`, `/internal/v1/tunnels/status`, and `/internal/v1/tunnels/{endpointId}/disconnect`. Valid wildcard hosts enter the public proxy. Every other service-host path returns the standard `404` response.

## Verification record

Completed on 2026-08-09:

- `gofmt`, `go mod tidy`, `go test ./...`, and `go vet ./...` passed.
- `go test -race ./...` passed in the official Go 1.25.7 Debian container. The local Windows attempt could not start because its race build had no C compiler.
- CLI builds passed for Windows amd64, Linux amd64, and macOS amd64.
- Gateway builds passed for Windows amd64 and Linux amd64.
- The production gateway Docker image built successfully.
- Docker Compose configuration resolved successfully from `deploy/env.example`.
- The Route 53-enabled Caddy image built and `caddy validate` accepted `deploy/Caddyfile`.
- Automated tests cover OAuth-only expose/session issuance, fresh-ticket reconnect, ticket-authenticated proxy traffic, concurrent/replay/collision behavior, callbacks, internal status/disconnect, shutdown, removed routes, removed flags, config migration, logout cleanup, static-ticket rejection, and unknown/offline catalog behavior.
- Repository scans found no production endpoint write SQL, migration execution, removed token environment variable, gateway-issued session credential, or authentication-method enum.

A live interactive Clerk/browser/frontend exercise was not run from this workspace because no production OAuth session or deployed stack was supplied. The automated local HTTP/WebSocket/JWKS/backend harness covers the same CLI/backend/gateway data path without external credentials. The production runbook in `docs/production-deployment.md` lists the remaining live smoke checks.

## Repository extraction readiness

Ready to move after module/import cleanup: `cmd/mockingo-gateway`, `internal/gateway`, `internal/gateway/ticketauth`, `internal/gateway/backendcallback`, `internal/gatewayconfig`, `internal/database` (pool opening only), `internal/endpoint` (transitional catalog), and `pkg/tunnelprotocol`.

Before extraction, decide the new Go module path, protocol package ownership/versioning, image/deployment ownership, CI/release pipeline, and the later endpoint-catalog replacement that permits complete PostgreSQL removal. Repository extraction itself is outside Stage 3E.4.
