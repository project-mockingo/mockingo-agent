# Gateway deployment

1. Deploy the Spring Boot backend and let it own Flyway/schema changes.
2. Provision a gateway PostgreSQL role in the backend-owned database with only `CONNECT`, schema `USAGE`, and `SELECT (hostname)` on `endpoints`. This Compose file intentionally does not create or migrate PostgreSQL.
3. Configure wildcard DNS/TLS for `*.mockingo.click` and the service hostname `gateway.mockingo.com`.
4. Copy `env.example` to `.env` and set distinct callback/internal tokens, backend/JWKS/issuer URLs, reader `DATABASE_URL`, instance ID, domains, proxy CIDRs, AWS region, and ACME email.
5. Run `docker compose --env-file .env -f docker-compose.production.yml config --quiet` and then `up -d`.

The gateway contains no migration command or endpoint write path. Do not commit `.env` or bake service/database/AWS credentials into images.
