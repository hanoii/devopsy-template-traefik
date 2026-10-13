# AGENTS.md

devopsy-template-traefik is the Traefik reverse proxy for a devopsy server, run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli) and released onto each
server like any devopsy project (`traefik/main` under the server's release
root: `releases/`, `current`, `shared/`). Everything lives in `.devopsy/`:

- `config.yaml`: `project: traefik`, its one environment `main` and the
  release steps; no servers, which are the operator's.

- `compose.yaml`: Traefik, socket-proxy, the optional acme-dns service
  (Compose profile `acmedns`), the optional `maintenance` page (profile
  `maintenance`, turned on by `DEVOPSY_MAINTENANCE_PAGE=true` in
  `lib/common.sh`; its nginx config and page in `maintenance/`), `init`
  (ownership of `mnt/`), and `jq`, `curl` and `sqlite` (profile `tools`,
  for scripts only).
- `.env.example`: the variables a server sets in its `shared/.env`, with
  `devopsy @<server>-traefik --vars`.
- `commands/`: devopsy custom commands (POSIX `sh`, each with a
  `## Description:` line): `deploy` (what release and rollback run),
  `restart`, `acmedns`, `letsencrypt`, `proxies`, `domains`, `forget`.
- `lib/facts` and `lib/retry`: what Traefik knows about hosts (JSON) and
  the retry router file, for `domains`. devopsy-cli knows no proxy: the
  whole report lives here.
- `lib/common.sh`: sourced by commands and lib's scripts (`in_traefik`,
  `jq`, `quiet`, `env_set`, `static_env`).
- `mnt/` (on servers `shared/mnt`, never uploaded): `letsencrypt/` (ACME
  storage, 10001's), `acmedns/` (10001's), `dynamic/` (Traefik's file
  provider: the public wildcard, proxies, hold, maintenance, retries),
  `proxies/` (`proxies.conf`, `forwarded-ips`), `maintenance/` (an
  optional page) and `traefik.env` (static settings, see below).
- `plugins-local/`: local Traefik plugins (Yaegi: standard library only).
  `proxies` maps a request from a trusted proxy's ranges to the visitor's IP
  from that proxy's header. `devopsy proxies` keeps the proxies in
  `mnt/proxies/proxies.conf` and writes `mnt/dynamic/proxies.yaml`
  (middleware and ranges; changes need no restart). `hold` holds requests
  while a backend answers 502/503/504 (README, "Holding requests");
  `deploy` always writes `mnt/dynamic/hold.yaml` (`hold_middleware`, its
  `maxWait` from `DEVOPSY_HOLD`, 0 passes through) and routers opt in with
  `hold@file` in their labels: nothing is held without it, and the file
  never goes away, since a router naming a missing middleware is dropped.
  It used to sit on the websecure entrypoint, holding every site; moved
  off because every response through the plugin loses its flushes (below),
  held or not. The proxies middleware does sit on the entrypoint, whose
  middleware list is static: one writer, `static_env`, called by `deploy`
  and `proxies`, composes `mnt/traefik.env` (the middleware, the proxies
  plugin and the forwarded-headers trust for X-Forwarded-For and X-Real-Ip
  proxies) and restarts Traefik only when it changed. Middleware files are
  written before the env names them and removed only once the running
  Traefik no longer does: an entrypoint referencing a missing middleware
  breaks every router. The file used to be `mnt/proxies/proxies.env`,
  which `static_env` replaces; an older release rolled back to puts
  `hold@file` back on the entrypoint, which works. Unit-test plugins with
  `go test` in their directory, and under Yaegi (`yaegi test`, see Checks).
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
fit together, and `../devopsy/ROADMAP.md` the open ideas, like automatic
certificate retries and a network per project.

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
- Hence `devopsy forget`: it only queues domains (`mnt/forget.pending`),
  and `forget_apply` (lib/common.sh), called by `deploy`, `restart` and
  `forget --now` right before their `up`, stops Traefik and edits the
  stores. The host has no jq and the Traefik image none either: one Traefik
  container prints every store as one JSON object, the `jq` service turns
  it into tab-separated lines (what was removed, each changed file's new
  content on one line), and a second Traefik container writes them, through
  the files so they keep mode 600, which Traefik insists on. Three
  containers, so Traefik is down a few seconds; a first pass before
  stopping it skips the stop when nothing matches. Private keys only travel
  in pipes and shell variables, never to the host's disk. Forgotten
  registrations then leave acme-dns's database too (`records` and `txt`,
  by subdomain): its image has no sqlite and its API no way to remove one,
  hence the `sqlite` service (profile `tools`: Python's module from the
  official image, as 10001, no network). acme-dns reads the database on
  every request, so it keeps running.
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
- Behind an HTTP proxy, Docker gives containers the client's proxy settings
  (`~/.docker/config.json`, `proxies`), and busybox's `wget` (Traefik's,
  acme-dns's) ignores `no_proxy`: calls to the container's own addresses
  pass `-Y off`. Fetches from the internet keep the proxy.
- The `cloudflare` resolver checks propagation through
  `DEVOPSY_DNS_RESOLVERS`, the host's first (`127.0.0.11`, Docker's embedded
  DNS, on every network Traefik joins), then 1.1.1.1: lego's `dnsQuery`
  asks the next resolver on an error or an empty answer. Then the zone's
  authoritative nameservers, directly on port 53. Servers behind firewalls
  that block outbound DNS skip all checks (`DEVOPSY_DNS_SKIP_CHECKS`) and
  wait (`DEVOPSY_DNS_PROPAGATION_DELAY`): lego looks the challenge name up
  (following CNAMEs) before writing the record, so the host's resolver
  caches NXDOMAIN and the recursive check fails until its negative TTL
  ends (seen behind a corporate resolver).
- The `cloudflare` resolver is always defined, like `acmedns`: Traefik
  starts fine without a token (checked on v3.7.13) and only fails when a
  router uses it. Its token is `DEVOPSY_CLOUDFLARE_DNS_API_TOKEN` in `.env`.
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
- The `traefik` network is shared by every routed container: one of them,
  compromised, reaches the others' ports directly, past Traefik's
  middlewares. Accepted while projects on a server trust each other and
  none relies on a middleware for access control (the recipes do not; only
  their web service joins it). The design for a network per project, tested,
  is in the roadmap. What a taken web container cannot reach: the socket
  proxy and acme-dns (their own networks) and Traefik's API (403).
- The socket proxy allows only what Traefik's Docker provider calls
  (version, list, inspect, events): `containers/.*` also let through
  `export`, `archive`, `logs` and `attach` of every container. Inspect still
  shows every container's environment; Traefik needs it for labels.
- Traefik runs read-only and without capabilities: Docker lets containers
  bind 80 and 443 without `NET_BIND_SERVICE`, and Traefik only writes to
  the mounted `/letsencrypt` (no `plugins-storage` for local plugins;
  checked on v3.7.13).
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
  DigitalOcean; validity from the verified handshake's verbose line, and
  every request sends `X-Devopsy-Hold: no`, so `hold` never keeps one
  for a paused site), a request through what DNS returns, all in the `curl`
  service. It prints the `devopsy --probe` line for the outside view. The
  public IP comes from `DEVOPSY_PUBLIC_IP`, else 1.1.1.1's `/cdn-cgi/trace`
  (the default route's address is private behind NAT, as on OrbStack). The
  check script prints tab-separated lines that jq turns into JSON, and only
  names matching a host name reach it: hosts come from every project's
  rules, and one stray quote broke hand-built JSON for the whole server.
- Traefik's API and dashboard (entrypoint `traefik`, `:8080`) listen on
  every network Traefik joins, `traefik` included, so they are not
  insecure: `api_access` (lib/common.sh, after every `up` in `deploy` and
  `restart`) writes a router with an `ipAllowList` of localhost (`exec`,
  for `lib/facts`) and the gateways of Traefik's networks (the published
  port, through docker-proxy or NAT), read from the running container.
  Docker 29 prints a missing gateway as `invalid IP`, and one bad range
  makes the router invalid: hence the filter. Checked on Debian 13 with
  Docker 29: host 200, a container on `traefik` 403.

- `hold` and Yaegi: Traefik runs plugins in Yaegi, which hands a
  plugin's ResponseWriter wrapper to Go code as a fixed set of interfaces:
  ResponseWriter plus Hijacker, never Flusher (yaegi v0.16.1,
  `stdlib/wrapper-composed.go`; traefik/traefik#10269). Websockets work
  (checked on v3.7.13); flushes are lost, so a response through the
  catcher, the first try included, only goes out as Go's buffer fills
  (BigPipe pages arrive in 4 KB pieces). Hence event streams and gRPC
  skip holding, and routers opt in. Not the way out: http-wasm plugins
  cannot call the next handler twice, so no retry loop. The plugin's own
  tests pass under `yaegi test` too, skipping the flush one.
- `TRAEFIK_EXPERIMENTAL_ABORTONPLUGINFAILURE=true`: a local plugin that
  fails to load (a Yaegi parse error) stops Traefik instead of being
  skipped. Skipped, the entrypoint's middleware list still names
  `proxies`, and every router naming `hold@file` is dropped, while the
  container looks healthy;
  stopped, it restarts in a loop, `up --wait` fails and the release
  rolls back. Checked on v3.7.13 with a broken hold.go.
- `hold` decides on the first status, like Traefik's `errors` middleware;
  a 502/503/504 with `Retry-After` passes at once (maintenance pages say
  they are down on purpose). A 503 is retried for any method; 502 and
  504 only for GET, HEAD and OPTIONS (`safeMethod`): the backend may have
  acted on a POST before the connection broke or the timeout passed.
  Every try is a `Clone` of the original request: what follows rewrites
  headers and the path in place. `X-Devopsy-Hold: no` skips holding
  (`skipHeader`): tools looking at a site. Instances nest through a value in the
  request's context: a router's instance (labels, with a page) takes over
  and the entrypoint's passes its answers through, otherwise the page
  would be held again. Statuses default in `New`, not `CreateConfig`:
  Traefik decodes a configured list over the default element by element.
- Bodies are buffered up to `maxBody` for retries, in Traefik's memory per
  held request; bigger ones get one try (their start replayed), and so do
  bodies of unknown length (`ContentLength < 0`, chunked): a client
  streaming one may wait for the answer before it ends it, and reading
  it all first would wait with it.
- `docker pause` alone does not hold. On Docker 29.9 a paused container's
  health is `unhealthy` at once (no event), and stays so after unpause
  until its next check; Traefik ignores pause and unpause events, but any
  other container event on the host (a clone stopping a database, a
  deploy) makes it re-read the containers and drop the routers of
  unhealthy ones: the maintenance page, or 404, instead of a wait. Hence
  `TRAEFIK_PROVIDERS_DOCKER_ALLOWEMPTYSERVICES=true`: a router whose
  containers are all unhealthy or `starting` stays, with an empty service
  answering 503, which `hold` retries until the container is healthy.
  Checked on OrbStack: a paused app plus an event was held 4 s, then 200.
  The cost: an unhealthy container of any project answers 503 (held on
  routers with `hold@file`) instead of 404.
  `docker stop` and `down` remove the container from the list, so its
  router goes either way (Traefik 404, nothing to hold): only another
  router can answer, the server-wide maintenance router (`priority: 1`, a
  `HostRegexp` under the wildcard domain) or a project's own at 2 or more.
- The maintenance service has no Traefik labels: with a service label and
  no router, the Docker provider makes a default router with a Host rule,
  and the default resolver would request a certificate for it. The file
  provider routes to its network alias, `devopsy-maintenance`, which
  Docker's DNS keeps current across restarts. Its profile comes from
  `DEVOPSY_MAINTENANCE_PAGE` through `lib/common.sh`, so every command's
  `up` keeps it; a plain `devopsy up` without it leaves it running, and
  `deploy` removes it (`rm -s -f maintenance`, which finds the service by
  name with its profile off) when turned off.

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

Plugins: `go test` in each plugin's directory, then the same tests under
Traefik's interpreter (Yaegi is stricter than Go), from `plugins-local`:

```sh
go install github.com/traefik/yaegi/cmd/yaegi@v0.16.1   # Traefik's version
GOPATH=$PWD GO111MODULE=off yaegi test github.com/hanoii/devopsy-traefik/hold
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
whoami, and the Traefik log has no new warnings. For `hold` (the whoami's
router labeled `traefik.http.routers.whoami.middlewares=hold@file`): `docker pause`
the whoami, a curl waits and answers after `docker unpause`; whoami's
`/health` answers what a POST to it set (`curl -d 503 -X POST .../health`),
so a GET of it is held until a POST sets 200. With a test
`DEVOPSY_PROXY_WILDCARD_DOMAIN` and `DEVOPSY_MAINTENANCE_PAGE=true`, a
stopped whoami under it, or any other host there, gets the maintenance
page (`curl --resolve`). Then `devopsy down`.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `build:`...).
