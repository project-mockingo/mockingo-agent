# Security

User authentication is Clerk OAuth Authorization Code Flow with PKCE. OAuth access tokens are sent only to Spring Boot. Tunnel tickets are backend-issued RS256 JWTs sent only to the validated gateway `/v1/connect` URL and consumed once. The gateway restricts algorithm and `kid`, caches public JWKS, and validates issuer, audience, time, endpoint/session claims, protocol, and replay state.

Two service credentials remain intentionally separate:

- `MOCKINGO_GATEWAY_INTERNAL_TOKEN`: Spring Boot authenticates to gateway internal APIs.
- `MOCKINGO_BACKEND_CALLBACK_TOKEN`: the gateway authenticates lifecycle callbacks to Spring Boot.

Both use exact Bearer syntax. Internal-token comparison is constant-time. Neither credential is accepted as a user credential or tunnel ticket.

Gateway logs include safe request/session identifiers and never authorization headers, OAuth tokens, tickets, service credentials, database URLs, cookies, or bodies. Errors use JSON, `Cache-Control: no-store`, and `X-Content-Type-Options: nosniff`.

The gateway database role is read-only and limited to endpoint hostname existence. Spring Boot owns all schema migrations and writes.

## Removed legacy authentication

Static management/API tokens, gateway-issued session secrets, direct tunnel registration, and gateway endpoint CRUD are removed from normal builds and configuration. Old public routes return `404`; a static value presented to `/v1/connect` receives the same generic `invalid_tunnel_ticket` response as any invalid ticket.
