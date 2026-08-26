# V6 dependency capture

Dependency capture observes HTTP and HTTPS calls made by a local application
while always forwarding them to the real dependency through the developer
machine's DNS, VPN, and network stack. Saved interactions were not matched in
the V6 baseline. V7 adds local replay through the same proxy; see
`v7-dependency-replay.md`.

## Start and configure

1. Sign in and start a session:

   ```bash
   mockingo expose --name integration --http 8080
   ```

   This starts both the public tunnel and dependency proxy. Use
   `--dependency-proxy=false` to disable the default proxy.

2. Configure the application manually to use the printed proxy for both HTTP
   and HTTPS (by default `http://127.0.0.1:8899`).
3. For inspected HTTPS, trust the printed `ca.crt` in that application's
   runtime trust store. Do not install `ca.key`; the private key remains local
   and is never printed or uploaded.
4. Exercise the application and inspect **Dependencies** in the endpoint's
   existing Traffic view. Dependency traffic can be saved like incoming
   traffic.

Choose another proxy port with `--proxy-port 9000`. An illustrative JVM
configuration is:

```text
-Dhttp.proxyHost=127.0.0.1 -Dhttp.proxyPort=8899
-Dhttps.proxyHost=127.0.0.1 -Dhttps.proxyPort=8899
```

Mockingo does not inject these options or modify Java, Node.js, Python, .NET,
WSO2, OS, browser, or trust-store configuration. `expose` can launch only the
explicit command supplied after `--`; that child still requires manual proxy
and CA configuration.

## Applications in Docker

The safe default listener is `127.0.0.1`, which is reachable only from the host.
To let a Docker container reach the dependency proxy, explicitly bind it to all
IPv4 host interfaces:

```bash
mockingo expose --name integration --http 8080 --proxy-bind 0.0.0.0
```

The Agent distinguishes the socket bind address from client addresses in its
output. Configure applications on the host with `127.0.0.1:8899` and Docker
Desktop applications on Windows or macOS with
`host.docker.internal:8899` for both HTTP and HTTPS. Binding outside loopback
allows any process or machine that can reach that host address to use the
proxy, so use it only on trusted development networks. Choose another port with
`--proxy-port 9000` when needed.

The dependency proxy is a required part of the default `expose` startup. If its
listener cannot bind, `expose` fails before opening the public tunnel. Use
another proxy port or `--dependency-proxy=false`; it never continues in an
ambiguous tunnel-only state.

Native Linux Docker may require an explicit host-gateway mapping:

```bash
docker run --add-host=host.docker.internal:host-gateway ...
```

For Docker Compose:

```yaml
services:
  app:
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

Mockingo does not inspect Docker, modify container networking, or inject proxy
environment variables. HTTPS inspection also remains manual: mount or copy the
printed public `ca.crt` into the container and add it to the application's trust
store. Never copy or mount the private `ca.key`. For example:

```yaml
services:
  app:
    volumes:
      - ./mockingo-ca.crt:/tmp/mockingo-ca.crt:ro
```

For WSO2 EI 6.6.0 running in Docker, run:

```bash
mockingo expose --name wso2-demo --http 8280 --proxy-bind 0.0.0.0
```

Configure WSO2 outbound HTTP/S with `proxyHost = host.docker.internal` and
`proxyPort = 8899`. For HTTPS, import the public Mockingo Capture CA into the
WSO2/JVM outbound truststore. No WSO2-specific Agent setup is performed.

To manually verify the complete Docker path, make an HTTP request through the
container proxy and confirm the real dependency and a `DEPENDENCY` Traffic item
both appear. Then trust the CA in the container and repeat with HTTPS. With an
enabled Dependency Behavior, repeat the matching request and confirm the local
replay is returned without contacting the real backend.

## HTTPS and passthrough

The proxy intercepts CONNECT locally and advertises HTTP/1.1. The Agent still
validates the real dependency certificate with the normal upstream trust
chain; interception never enables insecure upstream TLS.

Certificate-pinned applications and mTLS are not interceptable in V6. Tunnel
those exact hosts without decryption:

```bash
mockingo expose --name integration --http 8080 \
  --passthrough-host auth.company.com \
  --passthrough-host secure.partner.com
```

Passthrough preserves the real TLS identity and emits no invented HTTP
request/response data.

## Capture and privacy behavior

Request and response bodies stream to their real destinations. Textual capture
copies are bounded to 256 KiB and 512 KiB respectively; exceeding a limit marks
only the preview as truncated. Textual gzip, deflate, and Brotli bodies are
decoded only for this bounded copy while the application receives the original
encoded stream. Binary, multipart, unsupported encodings, and oversized
encoded bodies remain metadata-only.

Before an event leaves the machine, the Agent redacts Authorization,
Proxy-Authorization, Cookie, Set-Cookie, and common secret query parameters.
Request and response bodies may still contain sensitive application data; V6
does not attempt generic JSON or XML body-secret detection.

Completed events enter a bounded in-memory queue. A full queue or unavailable
Mockingo Cloud drops telemetry and never blocks or fails the dependency call.
The CA persists under the OS user configuration directory so it need not be
trusted again for every session. Mockingo never installs it automatically.
