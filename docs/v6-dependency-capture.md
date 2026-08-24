# V6 dependency capture

Dependency capture observes HTTP and HTTPS calls made by a local application
while always forwarding them to the real dependency through the developer
machine's DNS, VPN, and network stack. It is capture-only: saved dependency
interactions are never matched or replayed.

## Start and configure

1. Sign in and start a session:

   ```bash
   mockingo capture --name integration
   ```

2. Configure the application manually to use the printed proxy for both HTTP
   and HTTPS (by default `http://127.0.0.1:8899`).
3. For inspected HTTPS, trust the printed `ca.crt` in that application's
   runtime trust store. Do not install `ca.key`; the private key remains local
   and is never printed or uploaded.
4. Exercise the application and inspect **Dependencies** in the endpoint's
   existing Traffic view. Dependency traffic can be saved like incoming
   traffic.

Choose another loopback port with `--proxy-port 9000`. An illustrative JVM
configuration is:

```text
-Dhttp.proxyHost=127.0.0.1 -Dhttp.proxyPort=8899
-Dhttps.proxyHost=127.0.0.1 -Dhttps.proxyPort=8899
```

Mockingo does not inject these options, launch the application, or modify Java,
Node.js, Python, .NET, WSO2, OS, browser, or trust-store configuration.

## HTTPS and passthrough

The proxy intercepts CONNECT locally and advertises HTTP/1.1. The Agent still
validates the real dependency certificate with the normal upstream trust
chain; interception never enables insecure upstream TLS.

Certificate-pinned applications and mTLS are not interceptable in V6. Tunnel
those exact hosts without decryption:

```bash
mockingo capture --name integration \
  --passthrough-host auth.company.com \
  --passthrough-host secure.partner.com
```

Passthrough preserves the real TLS identity and emits no invented HTTP
request/response data.

## Capture and privacy behavior

Request and response bodies stream to their real destinations. Textual capture
copies are bounded to 256 KiB and 512 KiB respectively; exceeding a limit marks
only the preview as truncated. Binary, multipart, and encoded/compressed body
previews remain unavailable without changing the traffic.

Before an event leaves the machine, the Agent redacts Authorization,
Proxy-Authorization, Cookie, Set-Cookie, and common secret query parameters.
Request and response bodies may still contain sensitive application data; V6
does not attempt generic JSON or XML body-secret detection.

Completed events enter a bounded in-memory queue. A full queue or unavailable
Mockingo Cloud drops telemetry and never blocks or fails the dependency call.
The CA persists under the OS user configuration directory so it need not be
trusted again for every session. Mockingo never installs it automatically.
