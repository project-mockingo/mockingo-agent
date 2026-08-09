# CLI security boundaries

User authentication uses OAuth Authorization Code Flow with PKCE. OAuth access
tokens are sent only to the configured Spring Boot control-plane API. Refresh
tokens are held in platform credential storage (or the explicitly enabled
owner-only fallback file), never sent to the gateway.

Backend-issued tunnel tickets are sent only to the validated gateway
`/v1/connect` URL, used for one dial attempt, cleared from memory, and never
persisted or logged. Redirects are disabled for both API and local forwarding
clients where a redirect could cross a trust boundary.

The CLI contains no gateway internal token, backend callback token, Clerk
secret, ticket signing/verification key, PostgreSQL credential, gateway
read-only catalog, server route registration, or gateway deployment file.
Static management tokens, direct gateway endpoint CRUD, and direct tunnel
registration remain removed.
