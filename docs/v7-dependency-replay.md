# V7 dependency replay

The existing command now provides both dependency capture and local replay:

```bash
mockingo capture --name integration
```

Configure the application manually to use the printed HTTP proxy and trust the
printed Capture CA for inspected HTTPS, exactly as in V6. Then:

1. exercise a real dependency call;
2. save the `DEPENDENCY` traffic interaction in Console;
3. choose **Replay dependency** and enable the created replay;
4. repeat the application call through the same local proxy.

The Agent matches scheme, canonical host, effective port, method, and exact
path. Query parameters are ignored. On a match it consumes the request body,
returns the configured safe response locally, and never resolves, dials, or
performs TLS with the real dependency. On a miss it follows the unchanged V6
origin forwarding path.

Textual dependency bodies encoded with gzip, deflate, or Brotli are decoded
only for the bounded inspection copy. The original encoded bytes and headers
continue unchanged to the application. Replays store the decoded body and do
not retain the origin `Content-Encoding` or `Content-Length` headers.

HTTPS matching happens per decrypted HTTP/1.1 request after CONNECT. Origin TLS
is opened lazily only for a miss, so replay works when DNS, VPN, TCP, or the
dependency itself is unavailable. A keep-alive connection may mix replayed and
real requests. `--passthrough-host` traffic remains encrypted and can never
match a replay.

The latest full snapshot is held atomically in memory. A temporary Gateway
disconnect does not clear it: loaded replays and origin misses continue to
work. There is intentionally no disk cache in V7, so restarting the Agent
while Mockingo Cloud is unavailable leaves replays unavailable until the
capture session reconnects.

Dependency Replay requires a running local Mockingo proxy. It is not an
always-on cloud Virtual Endpoint, and V7 adds no query/header/body matching,
templates, scenarios, delays, or fault injection.
