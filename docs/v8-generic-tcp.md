# V8 generic TCP runtime

`mockingo expose` owns one endpoint runtime. Choose exactly one ingress mode:

```bash
mockingo expose --name demo --http 8080
mockingo expose --name activemq1 --tcp 61616
```

TCP mode connects every public connection only to `127.0.0.1:<local-port>`.
Clients cannot select another target. The transport is a binary byte stream: it
does not parse, buffer, record, or mock protocol messages.

For a service published from Docker, expose it on the host first:

```bash
docker run --rm -p 61616:61616 <broker-image>
mockingo expose --name activemq1 --tcp 61616
```

Use the public address printed by the CLI, for example
`tcp://activemq1.mockingo.click:31247`. The port belongs to the endpoint and
does not change when the Agent reconnects.

## TCP dependencies

TCP dependency definitions are delivered over the existing dependency-capture
session. The Agent opens each enabled local listener and forwards directly to
its configured target:

```text
application -> Agent listener -> real TCP dependency
```

Payload bytes never traverse Mockingo Cloud. OPEN and CLOSED/FAILED metadata is
uploaded through a bounded best-effort queue; a cloud outage can lose telemetry
but does not interrupt the local forwarding path.

Listeners default to `127.0.0.1`. Use `0.0.0.0` only on a trusted network: it
makes the proxy reachable from other hosts. When the application is in Docker
and the Agent runs on the host, use a host address reachable from the container,
such as `host.docker.internal` where supported. `127.0.0.1` inside a container
refers to that container, not the host.

The runtime uses 32 KiB frames and bounded per-connection queues, preserves
half-closes, and closes ingress connections when the tunnel is lost. TCP
dependency forwarding remains local and therefore continues during tunnel loss.
