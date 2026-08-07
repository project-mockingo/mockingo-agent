# Mockingo CLI

Mockingo exposes an HTTP service on your computer through a stable public hostname. The CLI authenticates to the Mockingo control plane with Clerk OAuth Authorization Code Flow and PKCE. The existing static gateway credential remains temporarily available for `expose` until tunnel-ticket integration lands.

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
export MOCKINGO_OAUTH_ISSUER=https://your-instance.clerk.accounts.dev
export MOCKINGO_OAUTH_CLIENT_ID=client_your_public_oauth_client_id
mockingo login
mockingo whoami

# Temporarily required only by the current Stage 2A tunnel path:
mockingo login --api-url https://gateway.mockingo.com --token "$MOCKINGO_API_TOKEN"
mockingo expose --name spring-demo --http 8080 -- java -jar target/application.jar
curl https://spring-demo.mockingo.click/hello
mockingo logout
```

`mockingo login --token` is deprecated. OAuth credentials and legacy tunnel credentials are stored separately; OAuth access tokens are never sent to the Go gateway. Backend-issued tunnel tickets are deferred to the next stage.

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
MOCKINGO_OAUTH_ISSUER=<Clerk Frontend API URL>
MOCKINGO_OAUTH_CLIENT_ID=<public OAuth client ID>
MOCKINGO_OAUTH_SCOPES=openid profile email offline_access
MOCKINGO_OAUTH_CALLBACK_HOST=127.0.0.1
MOCKINGO_OAUTH_CALLBACK_PORT=53682
MOCKINGO_OAUTH_CALLBACK_PATH=/oauth/callback
MOCKINGO_LOGIN_TIMEOUT=5m
```

CLI flags override environment variables, which override saved non-secret configuration and built-in defaults. Useful login flags are `--api-url`, `--issuer`, `--client-id`, `--callback-port`, `--no-browser`, and `--force`.

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

## Manual OAuth verification

1. Configure the Clerk public OAuth application and exact loopback redirect URI above.
2. Start the frontend with `/oauth-consent` and the Spring Boot control plane with CLI OAuth validation.
3. Run `mockingo login`, approve consent, then run `mockingo whoami` in the same and a restarted terminal.
4. Force token expiry and verify refresh, then test denied consent, invalid state, and an occupied callback port.
5. Run `mockingo logout`; verify local credentials are gone while the frontend browser session remains signed in.
6. Build and test Windows, Linux, and macOS targets. Automated tests use local fake authorization/token/backend servers and never require real Clerk credentials.

## Current limits

Protocol v1 buffers request and response bodies and caps each at 10 MiB. It supports concurrent HTTP request multiplexing, but not streaming, SSE, application WebSocket proxying, TCP/UDP forwarding, remote folders, container execution, or a dashboard. OAuth currently covers CLI login state and authenticated control-plane access only; `expose` still uses the explicitly configured deprecated static gateway token until backend-issued tunnel tickets are implemented.
