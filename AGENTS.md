# AGENTS.md

devopsy-traefik is the Traefik reverse proxy for a devopsy server, run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli). Everything lives in
`.devopsy/`:

- `compose.yaml`: Traefik, socket-proxy and the optional acme-dns service
  (Compose profile `acmedns`).
- `.env.example`: the variables a server sets in `.devopsy/.env`.
- `dns.env.example`: the optional Cloudflare DNS-01 resolver, loaded from
  `.devopsy/dns.env` when it exists.
- `commands/`: devopsy custom commands (POSIX `sh`): `restart`, `acmedns`,
  `letsencrypt`.
- `mnt/letsencrypt/`: ACME storage, never committed except `.gitkeep`.

Read `README.md` for the user-facing behavior; keep it in sync with any change
to that behavior.

## Rules

- Configure Traefik with `TRAEFIK_*` environment variables in
  `compose.yaml`, not static config files.
- Anything a server may need to change is a variable with a default, and goes
  in `.env.example`.
- Keep the hardening: Traefik never mounts the Docker socket, socket-proxy
  stays read-only and only accepts the `traefik` container, both run
  unprivileged, the dashboard stays on localhost.
- Pin exact image versions. When upgrading Traefik, read the migration notes
  (https://doc.traefik.io/traefik/migrate/v3/) for every version in between.
  When upgrading socket-proxy, read its release notes.
- Changes must not break projects already routed by this Traefik: the
  `traefik-main` network name, the `letsencrypt1`, `acmedns` and
  `cloudflare` resolver names and the `web`/`websecure` entrypoints are a public interface.

## Checks

Run it locally on other ports with a test environment name, and route a
`traefik/whoami` container through it:

```sh
DEVOPSY_ENVIRONMENT=test DEVOPSY_HTTP_PORT=18080 DEVOPSY_HTTPS_PORT=18443 \
  DEVOPSY_API_PORT=127.0.0.1:18081 devopsy up -d --wait
```

For acme-dns, add `COMPOSE_PROFILES=acmedns`, a test
`DEVOPSY_ACMEDNS_DOMAIN` and `DEVOPSY_ACMEDNS_PORT=15353`, then query it with
`dig @127.0.0.1 -p 15353 <domain> SOA`.

Check all containers are healthy, `http://` redirects, `https://` reaches
whoami, and the Traefik log has no new warnings. Then `devopsy down`.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `build:`...).
