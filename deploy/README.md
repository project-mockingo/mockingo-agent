# Linux server deployment checklist

1. Allocate an Elastic IP and attach it to the Linux/EC2 server.
2. In Route 53, point `A api.mockingo.click` and `A *.mockingo.click` to that address; add the equivalent `AAAA` records only if IPv6 is configured.
3. Attach the zone-scoped IAM role described in `docs/route53-dns.md` to EC2. If this is not EC2, provide short-lived AWS credentials through the standard credential chain.
4. Allow inbound TCP 80 and TCP/UDP 443. Do not expose 9090 or 5432.
5. Install a supported Docker Engine and Docker Compose plugin.
6. Clone the repository and enter `deploy/`.
7. Copy `env.example` to `.env`, set strong unique API and PostgreSQL secrets, set `ACME_EMAIL`, and verify the domain, scheme, API URL, region, and trusted proxy CIDRs.
8. Run `docker compose --env-file .env -f docker-compose.production.yml config --quiet`.
9. Run `docker compose --env-file .env -f docker-compose.production.yml build`.
10. Start PostgreSQL: `docker compose --env-file .env -f docker-compose.production.yml up -d postgres`.
11. Apply migrations: `docker compose --env-file .env -f docker-compose.production.yml run --rm mockingo-gateway migrate`.
12. Start the stack: `docker compose --env-file .env -f docker-compose.production.yml up -d`.
13. Check `docker compose --env-file .env -f docker-compose.production.yml ps` and logs. Confirm `https://api.mockingo.click/health/live` and `/health/ready` return success.
14. Run the cross-network smoke test in `docs/production-deployment.md`.
15. Back up the `postgres_data` and `caddy_data` volumes. Test restore procedures and OS/container security updates.

Do not commit `.env`, use per-endpoint DNS records, or bake AWS credentials into either image.
