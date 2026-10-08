# AGENTS.md

devopsy-template-traefik is the Traefik reverse proxy for a devopsy server, run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli) and released onto each
server like any devopsy project (`traefik/main` under the server's release
root: `releases/`, `current`, `shared/`). Everything lives in `.devopsy/`:

- `config.yaml`: `project: traefik` and the release steps; no targets,
  since servers are the operator's.

- `compose.yaml`: Traefik, socket-proxy, the optional acme-dns service
  (Compose profile `acmedns`), `init` (ownership of `mnt/`), and `jq` and
  `curl` (profile `tools`, for scripts only).
- `.env.example`: the variables a server sets in its `shared/.env`, with
  `devopsy @<server>-traefik --vars`.
- `commands/`: devopsy custom commands (POSIX `sh`, each with a
  `## Description:` line): `deploy` (what release and rollback run),
  `restart`, `acmedns`, `letsencrypt`, `proxies`, `domains`.
- `lib/facts` and `lib/retry`: what Traefik knows about hosts (JSON) and
  the retry router file, for `domains`. They were devopsy-cli's `domains`
  capability until October 2026: devopsy-cli now knows no proxy, and the
  whole report lives here.
- `lib/common.sh`: sourced by commands and lib's scripts (`in_traefik`,
  `jq`, `quiet`, `env_set`, `env_unset`).
- `mnt/` (on servers `shared/mnt`, never uploaded): `letsencrypt/` (ACME
  storage, 10001's), `acmedns/` (10001's), `dynamic/` (Traefik's file
  provider: the public wildcard, proxies, retries) and `proxies/`
  (`proxies.conf`, `proxies.env`).
- `plugins-local/`: local Traefik plugins (Yaegi: standard library only).
  `proxies` maps a request from a trusted proxy's ranges to the visitor's IP
  from that proxy's header. `devopsy proxies` keeps the proxies in
  `mnt/proxies/proxies.conf` and writes `mnt/dynamic/proxies.yaml`
  (middleware and ranges; changes need no restart) and
  `mnt/proxies/proxies.env` (plugin, websecure middleware, forwarded-headers
  trust for X-Forwarded-For and X-Real-Ip proxies), always the middleware
  first: an entrypoint referencing a missing middleware breaks every router.
  Unit-test plugins with `go test` in their directory.
- Everything a command writes at runtime lives in `mnt/` or `.env`: a file
  written elsewhere in `.devopsy/` lands in the release directory and is
  gone with the next release. Write `.env` through it (`env_set`, `cat >`),
  never `mv` over it: on servers it is a link to `shared/.env`.
- Containers run as `10001`, like every devopsy project's, never as the
  deploy user (in the docker group). `mnt/letsencrypt` and `mnt/acmedns`
  are 10001's, mode 700: scripts touch them only through `in_traefik`, a
  one-off Traefik container as 10001. The host needs no `jq` or `curl`: the
  `jq` service and Traefik's `wget` cover them.
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
  `traefik` network name, the `letsencrypt1`, `acmedns` and
  `cloudflare` resolver names, the `web`/`websecure` entrypoints, and the
  `devopsy.role=proxy` and `devopsy.export.WILDCARD_DOMAIN` labels projects
  import from are a public interface.

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
  It runs as 10001, so `mnt/acmedns` must be 10001's (the `init` service);
  otherwise registration fails with "no such table: records", which Traefik
  reports as EOF.
- Ownership: `shared/mnt` starts empty, Docker creates missing bind-mount
  directories as root, and the deploy user cannot chown to 10001. Hence
  `init` (the Traefik image as root, minimal capabilities, no network),
  which Traefik and acme-dns wait for. It runs `chown -R` on every start, so
  files moved in by hand are fixed too. `deploy` creates the host-written
  directories first, so they are the deploy user's.
- The `cloudflare` resolver is always defined, like `acmedns`: Traefik
  starts fine without a token (checked on v3.7.13) and only fails when a
  router uses it. Its token is `DEVOPSY_CLOUDFLARE_DNS_API_TOKEN` in `.env`;
  there is no `dns.env` any more.
- `deploy` saves values it detects (`DEVOPSY_DOCKER_GID`,
  `DEVOPSY_ACMEDNS_IP`) into `.env`, so a plain `devopsy up` later
  interpolates the same compose file.
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
- A wildcard requested before its DNS existed fails until a router changes:
  `devopsy @<server>-traefik domains --retry` asks again
  without a restart. A restart works too, and also requests HTTP-01
  certificates for routed hosts that had none; Traefik then serves those
  exact matches instead of the wildcard. Both are valid.
- `DEVOPSY_READ_TIMEOUT` is the websecure entrypoint's `readTimeout`: how long
  a request, body included, may take to arrive (Traefik v3 defaults to 60s,
  which cuts large uploads like registry layers). Entrypoints are static
  configuration, so projects cannot change it with labels; raise it per
  server, where a registry runs.
- The wildcard domain setting is `DEVOPSY_PROXY_WILDCARD_DOMAIN`, not the
  projects' `DEVOPSY_WILDCARD_DOMAIN`: with that name devopsy-cli computed a
  wildcard URL for Traefik itself. Projects get the value through the export label, read by
  devopsy-cli from the running container at each project release.
- `domains` checks from the server, not the operator's machine: DNS over
  HTTPS to 1.1.1.1, TLS to the server's own public IP (works on
  DigitalOcean), a request through what DNS returns, all in the `curl`
  service. It prints the `devopsy --probe` line for the outside view. The
  public IP comes from `DEVOPSY_PUBLIC_IP`, else 1.1.1.1's `/cdn-cgi/trace`
  (the default route's address is private behind NAT, as on OrbStack). The
  check script prints tab-separated lines that jq turns into JSON, and only
  names matching a host name reach it: hosts come from every project's
  rules, and one stray quote broke hand-built JSON for the whole server.
- Traefik's API (`TRAEFIK_API_INSECURE`, entrypoint `:8080`) listens on
  every network Traefik joins, `traefik` included: projects' containers
  can read every router. Read-only, and the published port stays on
  localhost, but unrelated clients share servers. Not fixed yet: an
  entrypoint cannot be bound to one Docker network's address.

## Releases

Servers get it with `devopsy @<server>:main --release` from a checkout, or
an operator's alias (`~/.config/devopsy/config.yaml`, `source:` naming the
checkout): servers are the operator's, never in this repository.
`config.yaml` gives `main` `mode: image` (only `.devopsy/` is uploaded) and
`deploy` for `--release` and `--rollback`. Settings go in the server's
`shared/.env` through `--vars`. No migration code: a breaking change to
`shared/` is moved by hand on each server.

The network projects join is fixed, `traefik`: one Traefik per host (the
`proxy` role refuses a second), so nothing varies, and it is the public
interface other projects name. Changing it (done once, from `traefik-main`)
fails the release while projects are still attached to the old one: compose
cannot remove a network with endpoints and leaves Traefik stopped.
Disconnect them and remove the old network first, release Traefik, then
`docker network connect` them to the new one until each project's own
release.

## Checks

```sh
cd .devopsy && docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:stable -s sh -x commands/* lib/facts lib/retry lib/common.sh
```

Run it locally on other ports, and route a `traefik/whoami` container
through it (network `traefik`):

```sh
DEVOPSY_HTTP_PORT=18080 DEVOPSY_HTTPS_PORT=18443 \
  DEVOPSY_API_PORT=127.0.0.1:18081 devopsy deploy
```

End to end, with a template importing from it: devopsy-cli's AGENTS.md,
Checks ("Roles and imports").

lib's scripts run directly with the project's environment loaded, for
example `env $(devopsy --env | grep -v '^#' | xargs) DEVOPSY_PROJECT_DIR=$PWD/.devopsy
sh .devopsy/lib/facts --all` (with the same test variables).
On macOS, OrbStack fakes ownership changes on bind mounts: test `init`, a
release and the commands on an OrbStack Linux machine.

For acme-dns, add `COMPOSE_PROFILES=acmedns`, a test
`DEVOPSY_ACMEDNS_DOMAIN` and `DEVOPSY_ACMEDNS_PORT=15353`, then query it with
`dig @127.0.0.1 -p 15353 <domain> SOA`.

Check all containers are healthy, `http://` redirects, `https://` reaches
whoami, and the Traefik log has no new warnings. Then `devopsy down`.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `build:`...).
