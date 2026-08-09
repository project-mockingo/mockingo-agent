# Backend tunnel-session callbacks

Ticket-authenticated lifecycle events are posted to `MOCKINGO_BACKEND_URL` with `Authorization: Bearer <MOCKINGO_BACKEND_CALLBACK_TOKEN>`. The backend configures the same value as `MOCKINGO_GATEWAY_CALLBACK_TOKEN`. It is separate from the token the backend uses to call the gateway internal API.

```text
POST /internal/v1/gateway/tunnel-sessions/{sessionId}/connected
POST /internal/v1/gateway/tunnel-sessions/{sessionId}/disconnected
POST /internal/v1/gateway/tunnel-sessions/{sessionId}/rejected
```

Bodies match the backend controller DTOs:

```json
{"endpointId":"e9949642-8b35-4247-ac5d-c076a463058d","gatewayInstanceId":"gateway-1","connectedAt":"2026-08-09T12:00:00Z"}
```

```json
{"endpointId":"e9949642-8b35-4247-ac5d-c076a463058d","disconnectedAt":"2026-08-09T12:30:00Z","reason":"client_closed"}
```

```json
{"endpointId":"e9949642-8b35-4247-ac5d-c076a463058d","rejectedAt":"2026-08-09T12:00:00Z","reason":"endpoint_already_connected"}
```

Times are UTC and each request carries a safe `X-Request-ID`. The HTTP client has bounded response reads, normal TLS verification, redirect rejection, connection pooling, per-request timeout, configurable attempts, exponential backoff, and jitter. Network failures, `5xx`, `408`, and `429` are retried; other `4xx` responses are definitive. Retry controls are `MOCKINGO_BACKEND_CALLBACK_TIMEOUT`, `MOCKINGO_BACKEND_CALLBACK_ATTEMPTS`, and `MOCKINGO_BACKEND_CALLBACK_BACKOFF`.

The gateway upgrades and atomically registers before sending `connected`. The tunnel remains temporarily registered during bounded retries. A definitive failure or exhausted retry budget closes and unregisters it with `backend_sync_failed`; no unreported tunnel is left active indefinitely.

Verified tickets rejected for endpoint/session collision, replay, unsupported protocol/version, or capacity cause `rejected`. Malformed, unsigned, or otherwise untrusted tokens never produce callbacks because their session identity is not trusted.

Cleanup unregisters before scheduling `disconnected`. Atomic registry removal ensures duplicate socket, proxy, shutdown, and internal-disconnect paths produce one logical callback. Central reason values include `client_closed`, `gateway_shutdown`, `heartbeat_timeout`, `protocol_error`, `internal_disconnect`, `public_proxy_error`, `backend_sync_failed`, and `replaced_not_allowed`. Every accepted tunnel has a backend session and follows this callback lifecycle.

`MOCKINGO_GATEWAY_INSTANCE_ID` must be stable for the running instance. Callback work is bounded during shutdown; terminal failures are logged without credentials and counted in metrics.
