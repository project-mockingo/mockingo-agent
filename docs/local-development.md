# Local development

Use the parent three-repository workspace while the protocol module is being
prepared for its first remote tag:

```bash
cd ..
go work sync
cd mockingo-cli
go test ./...
go run ./cmd/mockingo login
go run ./cmd/mockingo expose --expected-gateway-host localhost --allow-insecure-gateway --name spring-demo --http 8080
```

Run the Spring Boot control plane and sibling `mockingo-gateway` separately.
Reconnect requests a fresh backend tunnel session and ticket. The CLI does not
start, configure, or deploy the gateway server.
