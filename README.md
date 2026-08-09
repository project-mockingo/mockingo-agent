# Mockingo CLI

Mockingo exposes an HTTP service on your computer through a stable public hostname. The CLI authenticates to the Spring Boot control plane with Clerk OAuth Authorization Code Flow and PKCE, receives a short-lived tunnel ticket, and uses that ticket for one connection attempt to the Go gateway.

```text
mockingo CLI -- Clerk OAuth access token --> api.mockingo.com (Spring Boot)
mockingo CLI <-- one-use tunnel ticket ----- api.mockingo.com
mockingo CLI -- WSS + tunnel ticket -------> gateway.mockingo.com/v1/connect
Internet client --> https://<name>.mockingo.click --> Gateway --> CLI --> 127.0.0.1
```

## Build and test

Go 1.25 or newer is required.

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
mockingo login
mockingo whoami

mockingo expose --name spring-demo --http 8080 -- java -jar target/application.jar
curl https://spring-demo.mockingo.click/hello
mockingo logout
```

`mockingo expose` requires OAuth login. It calls `POST https://api.mockingo.com/api/v1/tunnel-sessions` without sending an owner identity; the backend derives ownership from the OAuth subject. The returned endpoint is visible to the same user at `https://app.mockingo.com/endpoints`.

Tunnel tickets are short-lived, held only in memory, sent only as a WebSocket `Authorization: Bearer` header, and consumed after one dial attempt. A lost connection causes the CLI to wait with bounded exponential backoff, refresh OAuth when necessary, request a new session ID and ticket, and reconnect. The public URL remains stable.

The deprecated static gateway flow remains available only through explicit opt-in:

```bash
mockingo expose --legacy --name spring-demo --http 8080
```

OAuth and legacy credentials stay separate. Normal expose never falls back to a legacy token after an OAuth failure.

## Clerk OAuth public-client setup

Create an OAuth application named `Mockingo CLI` in the Clerk Dashboard and configure it as a public client with PKCE required. Do not create or embed a client secret. Register this exact redirect URI:

```text
http://127.0.0.1:53682/oauth/callback
```

Clerk OAuth application configuration uses exact redirect URI strings and currently does not document RFC 8252 variable-port matching. Mockingo therefore uses the fixed port `53682` by default and fails clearly if it is occupied. `--callback-port 0` explicitly requests an OS-assigned port and is suitable only when that exact generated URI is accepted/registered by your authorization-server configuration; the CLI never silently changes ports.

Set the Clerk Frontend API URL as the issuer, not `mockingo.click` and not the Clerk Backend API URL. Mockingo retrieves and validates `<issuer>/.well-known/oauth-authorization-server`, requiring the authorization-code and refresh-token grants, PKCE `S256`, and token endpoint authentication method `none`.

Default configuration:

```text
MOCKINGO_API_URL=https://api.mockingo.com
MOCKINGO_OAUTH_ISSUER=https://teaching-wolf-20.clerk.accounts.dev
MOCKINGO_OAUTH_CLIENT_ID=p7M2nCmzjbRO88ns
MOCKINGO_OAUTH_SCOPES=openid profile email offline_access
MOCKINGO_OAUTH_CALLBACK_HOST=127.0.0.1
MOCKINGO_OAUTH_CALLBACK_PORT=53682
MOCKINGO_OAUTH_CALLBACK_PATH=/oauth/callback
MOCKINGO_LOGIN_TIMEOUT=5m
MOCKINGO_EXPECTED_GATEWAY_HOST=gateway.mockingo.com
MOCKINGO_TUNNEL_PROTOCOL_VERSION=1
MOCKINGO_RECONNECT_ENABLED=true
MOCKINGO_RECONNECT_INITIAL_DELAY=1s
MOCKINGO_RECONNECT_MAX_DELAY=30s
MOCKINGO_LEGACY_EXPOSE_ENABLED=false
```

`mockingo login` uses Mockingo's built-in production OAuth configuration. CLI flags override environment variables, which override saved non-secret configuration and built-in defaults. The `--issuer` and `--client-id` overrides remain available for local development and testing. Other useful login flags are `--api-url`, `--callback-port`, `--no-browser`, and `--force`.

The browser flow generates a new high-entropy state, nonce, and PKCE verifier for every attempt. After consent, the CLI exchanges the one-time code at the discovered token endpoint and calls `GET https://api.mockingo.com/api/v1/me`. Credentials are saved only after the backend confirms `clerk_oauth` authentication.

Access and refresh tokens are stored under service `mockingo`, account `oauth:<issuer>:<client-id>`, using Windows Credential Manager, macOS Keychain, or Linux Secret Service. Only non-secret metadata is stored in the Mockingo config file. If the OS credential store is unavailable, fallback file storage requires explicit `--allow-insecure-storage` (or `MOCKINGO_ALLOW_FILE_CREDENTIALS=true`), emits a warning, and uses an owner-only file. Tokens are refreshed centrally before expiry; rotated refresh tokens are saved atomically.

`mockingo whoami --json` emits only the API URL, stable user ID, and authentication method. `mockingo logout` removes local OAuth credentials and metadata, is idempotent, and does not sign the browser out of `app.mockingo.com`. A standards-based revocation endpoint is used best-effort only when discovery advertises one.

### OAuth troubleshooting

- **Redirect URI mismatch:** register exactly `http://127.0.0.1:53682/oauth/callback` and keep the configured host, port, and path identical.
- **Callback port occupied:** stop the local process using port `53682` or register and configure another exact port. No alternate port is chosen automatically.
- **Browser does not open:** copy the complete URL printed by `mockingo login`, or use `--no-browser`.
- **Consent denied:** rerun `mockingo login`; no credentials are saved.
- **Invalid state:** close the page and restart login. The callback is rejected to prevent request forgery.
- **Token exchange failure:** restart login to obtain a new code; the CLI deliberately does not retry an ambiguous code exchange.
- **Backend rejects token:** confirm the public OAuth client and issuer match the backend's Clerk configuration. Rejected tokens are not persisted.
- **Credential store unavailable:** start the platform credential service, or explicitly opt into owner-only fallback storage with `--allow-insecure-storage`.
- **Expired refresh token:** run `mockingo login --force`. Invalid refresh credentials are removed locally.

### Expose troubleshooting

- **Not signed in / expired CLI session:** run `mockingo login`; normal expose never falls back to legacy authentication.
- **Endpoint name unavailable:** choose another name. The CLI does not disclose the other owner.
- **Backend unavailable:** transient network failures and HTTP 502/503/504 are retried with capped backoff during reconnect.
- **Gateway unavailable:** temporary dial, DNS, timeout, and gateway 5xx failures obtain a new backend session and ticket before retrying.
- **Tunnel session pending / previous session closing:** leave the CLI running; it retries `tunnel_session_pending` and `endpoint_already_connected` without terminal spam.
- **Unsupported protocol version:** use protocol v1 and ensure CLI, backend, and gateway versions agree.
- **Local port unavailable:** start the application on the selected port or pass a startup command after `--`.
- **Untrusted gateway URL:** production requires `wss://gateway.mockingo.com/v1/connect`. Local `ws://localhost` or `ws://127.0.0.1` requires an explicit expected host and `--allow-insecure-gateway`.
- **Repeated reconnect failure:** use `--verbose` for token-free status details and verify backend, gateway, DNS, TLS, and local application health.

Stopping `expose` takes the tunnel offline but does not release `spring-demo.mockingo.click`. Manage durable reservations with:

```bash
mockingo endpoints list
mockingo endpoints list --json
mockingo endpoints delete spring-demo
mockingo endpoints delete spring-demo --force
```

## Local development

The following minimal gateway setup is the deprecated legacy-only demo:

```bash
export MOCKINGO_ENV=development
export MOCKINGO_BASE_DOMAIN=localhost
export MOCKINGO_PUBLIC_SCHEME=http
export MOCKINGO_API_PUBLIC_URL=http://localhost:9090
export MOCKINGO_DEV_TOKEN=development-token
export MOCKINGO_TICKET_AUTH_ENABLED=false
go run ./cmd/mockingo-gateway
```

The normal local flow requires a local Spring Boot backend and a gateway with ticket authentication enabled. Configure the backend connect URL as `ws://127.0.0.1:9090/v1/connect` and the gateway's backend/JWKS/callback settings as described in [local development](docs/local-development.md), then run:

```bash
go run ./cmd/mockingo login --api-url http://localhost:8081 --issuer "$LOCAL_CLERK_ISSUER"
go run ./cmd/mockingo expose --name spring-demo --http 8080 \
  --api-url http://localhost:8081 \
  --expected-gateway-host 127.0.0.1 \
  --allow-insecure-gateway \
  -- python3 -m http.server 8080
curl -H 'Host: spring-demo.localhost' http://localhost:9090/
```

For the transitional legacy-only demo, configure the old token with `mockingo login --api-url http://localhost:9090 --token development-token` and add `--legacy` to expose. This compatibility path is deprecated.

Development mode uses an in-memory endpoint repository when `DATABASE_URL` is absent. Set `DATABASE_URL` and run `go run ./cmd/mockingo-gateway migrate` to exercise persistence locally.

## Production deployment

The production stack is in `deploy/` and contains PostgreSQL, the gateway, and a custom Caddy build with the Route 53 DNS module. Only Caddy publishes ports. Start with [deploy/README.md](deploy/README.md), then read [production deployment](docs/production-deployment.md), [Route 53 DNS](docs/route53-dns.md), and [security](docs/security.md).

Gateway integration references: [tunnel ticket authentication](docs/tunnel-ticket-auth.md), [gateway internal API](docs/gateway-internal-api.md), and [backend callbacks](docs/backend-callbacks.md).

## Manual OAuth and tunnel verification

1. Configure the Clerk public OAuth application and exact loopback redirect URI above.
2. Start PostgreSQL, the Spring Boot backend with CLI OAuth validation, the ticket-enabled gateway, the frontend with `/oauth-consent`, and a local HTTP application on port 8080.
3. Run `mockingo login`, `mockingo whoami`, then `mockingo expose --name spring-demo --http 8080`.
4. Verify the endpoint belongs to the signed-in Clerk user, appears in the frontend, connects through `gateway.mockingo.com`, and forwards public HTTP requests.
5. Restart the gateway and verify expose requests a new session/ticket and returns to Connected; stop expose and verify Offline.
6. Test endpoint reuse, a second user's name-unavailable response, OAuth refresh, expired login, and explicit `--legacy` mode.
7. Confirm captured output contains no OAuth token, refresh token, ticket, or Authorization header.
8. Run `mockingo logout`; verify local credentials are gone while the frontend browser session remains signed in.
9. Build and test Windows, Linux, and macOS targets. Automated tests use local fake authorization/token/backend/gateway servers and never require production services.

## Current limits

Protocol v1 buffers request and response bodies and caps each at 10 MiB. It supports concurrent HTTP request multiplexing, but not streaming, SSE, application WebSocket proxying, TCP/UDP forwarding, remote folders, container execution, or multi-port expose. Transitional legacy gateway management/authentication and its PostgreSQL support remain for removal in later stages.
