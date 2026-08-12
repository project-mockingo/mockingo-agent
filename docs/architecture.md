# CLI architecture

```text
mockingo CLI
  | OAuth access token
  v
api.mockingo.com (Spring Boot control plane)
  | short-lived, one-use tunnel ticket
  v
gateway.mockingo.com/v1/connect (external Go data plane)
  | tunnel protocol v1
  v
local HTTP target
```

The CLI owns OAuth/PKCE, secure credentials, control-plane API calls, tunnel
session acquisition, gateway URL validation, WebSocket client behavior, local
HTTP forwarding, reconnect/backoff, optional child-process lifecycle, and
terminal output.

The separate gateway owns ticket verification, JWKS, replay protection, active
registries, wildcard public routing, callbacks, internal APIs, PostgreSQL
catalog reads, health, metrics, and shutdown. Shared wire DTOs, validation, and
limits come from the public
`github.com/project-mockingo/mockingo-agent/tunnelprotocol` package.
