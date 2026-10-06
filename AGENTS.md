# AGENTS.md

devopsy-traefik is the Traefik reverse proxy for a devopsy server, run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli). Everything lives in
`.devopsy/`:

- `compose.yaml`: Traefik, socket-proxy and the optional acme-dns service
  (Compose profile `acmedns`).
- `.env.example`: the variables a server sets in `.devopsy/.env`.
- `dns.env.example`: the optional Cloudflare DNS-01 resolver, loaded from
  `.devopsy/dns.env` when it exists.
- `commands/`: devopsy custom commands (POSIX `sh`, each with a
  `## Description:` line): `restart`, `acmedns`, `letsencrypt`, `proxies`.
- `mnt/letsencrypt/`: ACME storage, never committed except `.gitkeep`.
- `mnt/dynamic/`: Traefik file provider directory; `dynamic.example/` has
  templates for it, like the public wildcard certificate.
- `plugins-local/`: local Traefik plugins (Yaegi: standard library only).
  `proxies` maps a request from a trusted proxy's ranges to the visitor's IP
  from that proxy's header. `devopsy proxies` keeps the proxies in
  `proxies.conf` and writes `mnt/dynamic/proxies.yaml` (middleware and ranges;
  changes need no restart) and `proxies.env` (plugin, websecure middleware,
  forwarded-headers trust for X-Forwarded-For and X-Real-Ip proxies), always
  the middleware first: an entrypoint referencing a missing middleware breaks
  every router. It migrates the first, Cloudflare-only version
  (`cloudflare-proxy.env`). Unit-test plugins with `go test` in their
  directory.
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

## Design decisions and gotchas

The workspace README (`../devopsy/README.md` locally) describes how the repos
fit together, and `../devopsy/ROADMAP.md` the open ideas, like `devopsy
forget` and the Cloudflare real client IP plugin.

- `letsencrypt1` uses HTTP-01, not TLS-ALPN: it also works behind a CDN
  proxy, and Traefik's ACME challenge router has top priority, ahead of the
  HTTPS redirect.
- Traefik only requests or retries a certificate when a router's
  configuration changes. Recreating an identical container does not count:
  take the site down and up to retry.
- Traefik checks all certificates in its store, from any resolver, and a
  wildcard covers a host: per-host HTTP-01 requests stop once the public
  wildcard exists.
- Traefik renews every stored certificate, routed or not, and only reads its
  ACME files at startup, writing its in-memory copy back on changes. Editing
  `mnt/letsencrypt/*.json` takes a stop or restart around the edit.
- The same holds for acme-dns registrations (`acme-dns-accounts.json`):
  lego loads them once. That is why there is no "pre-register a domain"
  command: it would need a Traefik restart per domain.
- acme-dns: binds the public IP only (`DEVOPSY_ACMEDNS_IP`), because
  systemd-resolved holds 127.0.0.53:53 on many cloud images, and Docker
  cannot publish 0.0.0.0:53 then. Its log level is `warn`, not `warning`.
  It runs as `DEVOPSY_UID`, so `mnt/acmedns` must exist owned by that user
  (shipped with `.gitkeep`); otherwise registration fails with
  "no such table: records", which Traefik reports as EOF.
- Cloudflare does not allow NS and A records on the same name, hence the
  nameserver `ns-<acme-dns domain>`. The A record must not be proxied.
- The proxies plugin changes the request's remote address; Traefik then
  builds X-Forwarded-For from it (verified on v3.7.13). Traefik strips
  X-Forwarded-For and X-Real-Ip from untrusted peers before middlewares run,
  hence the forwarded-headers trust for proxies using them. Test locally by
  adding a proxy for Docker's gateway range (`192.168.0.0/16` on OrbStack)
  and sending its header through a whoami.
- The wildcard router matches one reserved name only, so other hosts keep the
  usual 404 instead of the noop service's 418.

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
