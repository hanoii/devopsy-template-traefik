# AGENTS.md

devopsy-template-traefik is the Traefik reverse proxy for a devopsy
server, run with [devopsy-cli](https://github.com/hanoii/devopsy-cli) and
released onto each server like any devopsy project (`traefik/prod` under
the server's release root: `releases/`, `current`, `shared/`). It owns
everything about routing and certificates; devopsy-cli knows no proxy.

This file holds the rules, the decisions with their reasons, and the
findings a later session must not have to rediscover. How a script works
is in its comments.

## Layout

Everything lives in `.devopsy/`:

- `config.yaml`: `project: traefik`, its one environment `prod` and the
  release steps; no servers, which are the operator's.
- `compose.yaml`: Traefik, socket-proxy, the optional acme-dns service
  (profile `acmedns`), and `jq`, `curl` and `sqlite` (profile `tools`,
  for scripts only).
- `.env.example`: the variables a server sets in its `shared/.env`, with
  `devopsy @<server>-traefik --vars`.
- `commands/` (POSIX `sh`, each with a `## Description:` line):
  `check` (a release's prepare step: the settings), `deploy` (what
  release, rollback and reload run), `restart`, `acmedns`,
  `letsencrypt`, `proxies`, `domains`, `forget`, and `setup`, the only
  one run locally (`## Run: local`, `## Needs: target`: `devopsy
  @<server>:prod setup`, or an alias, the address as typed in
  `DEVOPSY_COMMAND_TARGET`).
- `lib/facts` and `lib/retry`: what Traefik knows about hosts (JSON) and
  the retry router file, for `domains`. `lib/common.sh`: sourced by
  commands and lib's scripts (`in_traefik`, `jq`, `quiet`, `env_set`,
  `hold_middleware`, `static_env`, `api_access`, `forget_apply`).
- `mnt/` (on servers `shared/mnt`, never uploaded): `letsencrypt/` (ACME
  storage), `acmedns/`, `dynamic/` (Traefik's file provider: the public
  wildcard, the API's router, `Forwarded`'s removal, proxies, the
  tunnel's trust, the hold, retries), `proxies/`
  (`proxies.conf`, `forwarded-ips`), `traefik.env` (static settings)
  and `forget.pending`.
- `plugins-local/`: local Traefik plugins (Yaegi: standard library
  only). `proxies` maps a request from a trusted proxy's ranges to the
  visitor's IP from that proxy's header; `hold` holds visitors of the
  sites naming `hold@file` through their app's stop.

## Rules

- **Projects already routed must keep working:** the `traefik` network
  name, the `le-http01`, `le-dns01-selfhosted` and `le-dns01-cloudflare`
  resolver names,
  the `web`/`websecure`/`tunnel` entrypoints, the `tunnel` network and
  its address `http://traefik.tunnel:8081`, and the `devopsy.role=proxy` and
  `devopsy.export.BASE_DOMAIN` labels projects import from are a
  public interface. Why: other projects name them in their own compose
  files, released on their own schedule.
- **Everything a command writes at runtime lives in `mnt/` or `.env`.**
  Why: a file written elsewhere in `.devopsy/` lands in the release
  directory and is gone with the next release. Write `.env` through it
  (`env_set`, `cat >`), never `mv` over it: on servers it is a link to
  `shared/.env`.
- **Containers run as `10001`,** like every devopsy project's, never as
  the deploy user, who is in the docker group. `mnt/letsencrypt` and
  `mnt/acmedns` are 10001's (devopsy gives them, with everything in
  them, before the run step: `devopsy.mnt.owner` on `traefik` and
  `acmedns`), their files mode 600: scripts touch them only through
  `in_traefik`, a one-off Traefik container as 10001. The host needs no
  `jq`, `curl` or `sqlite`: the `tools` services and Traefik's `wget`
  cover them.
- **Configure Traefik with `TRAEFIK_*` variables in `compose.yaml`,** not
  static config files. Anything a server may need to change is a
  variable with a default, and goes in `.env.example`.
- **The entrypoints' middleware lists have one writer:**
  `static_env` (`lib/common.sh`, called by `deploy` and `proxies`)
  composes `mnt/traefik.env` (the `Forwarded` removal, the proxies
  middleware and plugin, the forwarded-headers trust for
  X-Forwarded-For and X-Real-Ip proxies, the tunnel's trust and the
  default entrypoints) and restarts Traefik
  only when it changed. Why one writer:
  entrypoints are static configuration. Middleware files are written
  before the env names them and removed only once the running Traefik
  no longer does: an entrypoint naming a missing middleware breaks
  every router. `devopsy proxies` keeps the proxies in
  `mnt/proxies/proxies.conf` and writes `mnt/dynamic/proxies.yaml`
  (middleware and ranges; changes need no restart).
- **Keep the hardening:** Traefik never mounts the Docker socket,
  socket-proxy stays read-only and only accepts the `traefik`
  container, both run unprivileged, the dashboard stays on localhost.
  Every service has a fixed non-root UID (10001; socket-proxy's
  65534, in the socket's group), `read_only`, `cap_drop: [ALL]` and
  `no-new-privileges`, adding back only a capability a stated reason
  needs (none today); a new service gets all four, and an image we
  build would have no setuid binaries or file capabilities it does not
  need. The `jq` and `curl` tools get 10001 too, though their images
  default to root and UID 100: both work as 10001 with a read-only
  root (jq 1.8.1 and curl 8.15.0, checked), and a tool as root, even
  without capabilities, reads files the deploy user cannot. A tool
  whose image only works as root would be the one exception: say why
  here.
- **Float images on the series upstream patches, bump minors by
  hand.** `traefik:v3.7` gets upstream's fixes with every release (each
  one pulls its images again); a minor brings migration notes and a new
  Yaegi for the plugins, so moving to the next one is a commit: read the
  notes (https://doc.traefik.io/traefik/migrate/v3/) for every version
  in between. Never `latest`, never a major tag. What publishes no minor
  tag stays an exact pin (socket-proxy, whose allow-list flags are
  security settings: read its release notes when upgrading; acme-dns;
  jq and curl). A pinned name changes with a bump, so a rollback across
  one goes back exactly; a floating name goes forward on a rollback
  (devopsy-cli's README, "Choosing tags").
- **`setup` keeps no state:** it drives the other commands through its
  target, setting values with `--vars set` from its environment, never
  as arguments.
- **No migration code:** a breaking change to `shared/` is moved by
  hand on each server (Releases).

## Decisions and gotchas

### Certificates

- **`le-http01` uses HTTP-01, not TLS-ALPN:** it also works behind a
  CDN proxy, and Traefik's ACME challenge router has top priority,
  ahead of the HTTPS redirect.
- **Traefik requests or retries a certificate only when a router's
  configuration changes.** Recreating an identical container does not
  count: take the site down and up to retry. A wildcard requested
  before its DNS existed fails until then: `devopsy @<server>-traefik
  domains --retry` asks again without a restart (`lib/retry`). A
  restart works too, and also requests HTTP-01 certificates for routed
  hosts that had none; Traefik then serves those exact matches instead
  of the wildcard. Both are valid.
- **Any stored certificate covers a host:** Traefik checks all
  certificates in its store, from any resolver, and a wildcard covers a
  host, so per-host HTTP-01 requests stop once the public wildcard
  exists.
- **Traefik reads its ACME files only at startup** and writes its
  in-memory copy back on changes, and it renews every stored
  certificate, routed or not. Editing `mnt/letsencrypt/*.json` takes a
  stop or restart around the edit. lego likewise loads acme-dns
  registrations (`acme-dns-accounts.json`) once, which is why there is
  no "pre-register a domain" command: it would need a Traefik restart
  per domain.
- **`devopsy forget` only queues** (`mnt/forget.pending`); `forget_apply`
  (`lib/common.sh`), called by `deploy`, `restart` and `forget --now`
  right before their `up`, stops Traefik and edits the stores. Neither
  the host nor the Traefik image has jq: one Traefik container prints
  every store as one JSON object, the `jq` service turns it into
  tab-separated lines, and a second Traefik container writes them,
  through the files so they keep mode 600, which Traefik insists on.
  Traefik is down a few seconds; a first pass skips the stop when
  nothing matches. Private keys only travel in pipes and shell
  variables, never to the host's disk. Forgotten registrations also
  leave acme-dns's database (`records` and `txt`, by subdomain): its
  image has no sqlite and its API no way to remove one, hence the
  `sqlite` service (Python's module from the official image, as 10001,
  no network). acme-dns reads the database on every request, so it
  keeps running.
- **The wildcard router matches one reserved name only,** so other
  hosts keep the usual 404 instead of the noop service's 418.

### acme-dns and DNS resolvers

- **acme-dns binds the public IP only** (`PROXY_ACMEDNS_IP`):
  systemd-resolved holds 127.0.0.53:53 on many cloud images, and Docker
  cannot publish 0.0.0.0:53 then. Its log level is `warn`, not
  `warning`. It runs as 10001, so `mnt/acmedns` must be 10001's (the
  `devopsy.mnt.owner` label); otherwise registration fails with "no such table:
  records", which Traefik reports as EOF.
- **Cloudflare does not allow NS and A records on the same name,** hence
  the nameserver `ns-<acme-dns domain>`. The A record must not be
  proxied.
- **`le-dns01-cloudflare` checks propagation through
  `PROXY_DNS_RESOLVERS`,** the host's first (`127.0.0.11`, Docker's
  embedded DNS, on every network Traefik joins), then 1.1.1.1: lego's
  `dnsQuery` asks the next resolver on an error or an empty answer.
  Then the zone's authoritative nameservers, directly on port 53.
  Servers behind firewalls that block outbound DNS skip all checks
  (`PROXY_DNS_SKIP_CHECKS`) and wait
  (`PROXY_DNS_PROPAGATION_DELAY`). Why: lego looks the challenge name
  up (following CNAMEs) before writing the record, so the host's
  resolver caches NXDOMAIN and the recursive check fails until its
  negative TTL ends (seen behind a corporate resolver).
- **`le-dns01-cloudflare` is always defined,** like
  `le-dns01-selfhosted`:
  Traefik starts fine without a token (checked on v3.7.13) and only
  fails when a router uses it. Its token is
  `PROXY_CLOUDFLARE_DNS_API_TOKEN` in `.env`.

### Settings and ownership

- **devopsy gives `mnt/letsencrypt` and `mnt/acmedns` to 10001,**
  through `devopsy.mnt.owner=10001:10001` on `traefik` and `acmedns`:
  `shared/mnt` starts empty, Docker creates missing bind-mount
  directories as root, and the deploy user cannot chown to 10001. Before
  the run step devopsy creates each entry (mode 755) and gives it, and
  everything in it, to 10001, on every release, so files moved in by
  hand are fixed too. `deploy` creates only the host-written directories
  (`dynamic`, `proxies`), so they are the deploy user's.
- **No `init` service, because both programs create their files as they
  need them** (checked 2026-10-23 on Traefik v3.7.13 and acme-dns v2.0.2,
  as 10001 with `cap_drop: [ALL]` and `read_only`, in a 755 directory
  owned by 10001): Traefik creates `acme-le-http01.json` and the other resolvers'
  files with mode 600 at startup (`local_store.go` opens them with 0600);
  lego creates `acme-dns-accounts.json` with 0600 (goacmedns' `File`,
  lego v4.35); acme-dns creates `acme-dns.db` with mode 600. The old
  `init` also forced 700 on the directories and 600 on the files; the
  directories are 755 now (new servers; an existing 700 stays), which
  exposes only file names, since every file inside is 600. One thing
  `init` did that nothing does now: Traefik refuses an ACME file with
  any group or other permission (`permissions 644 for
  /letsencrypt/acme-le-http01.json are too open, please use 600`, logged as an
  error while the container stays healthy, and that resolver is skipped,
  so no certificates are requested). A file restored from a backup or
  copied by hand with a wider mode is the only way there; the fix is
  `chmod 600` in `in_traefik`. Everything devopsy-template-traefik's own
  scripts write keeps 600 (`umask 077`, `cp -p`, writes through the
  file, `mv`). A fresh named volume is no test of this: Docker copies
  the image's directory ownership into an empty volume at each mount
  unless it is `nocopy`.
- **`deploy` saves values it detects** (`PROXY_DOCKER_GID`,
  `PROXY_ACMEDNS_IP`) into `.env`, so a plain `devopsy up` later
  interpolates the same compose file.
- **Names are `PROXY_*`: the proxy role's contract and this template's
  settings** (decided 2026-10-26): never `DEVOPSY_*`, which are
  devopsy-cli's own and made these look like devopsy's, nor `TRAEFIK_*`,
  which Traefik reads as its static configuration. A name means one fact
  on both sides: `PROXY_BASE_DOMAIN` is this server's setting, exported
  as `BASE_DOMAIN` (which said nothing as `WILDCARD_DOMAIN`: the domain
  every environment's default host goes under) and imported by sites
  under the same name (a target may override it there);
  `PROXY_CERTRESOLVER` is the default here and a site's own choice
  there; `PROXY_DOMAINS` is a site's. Sites get the base domain through
  the export label, read by devopsy-cli from the running container at
  each project release. No migration: a server's keys are renamed by
  hand.
- **Resolvers are named by CA, ACME challenge and, for DNS-01, who
  answers it** (`le-http01`, `le-dns01-selfhosted`,
  `le-dns01-cloudflare`; decided 2026-10-27, from `letsencrypt1`,
  `acmedns` and `cloudflare`, which named a CA, a program and a
  provider): what a site chooses is how its domain proves control, and a
  second CA (a fallback) adds names without renaming these, since a
  rename costs every certificate (a resolver's store is its own file,
  `acme-<resolver>.json`). The ACME settings carry the CA's prefix too
  (`PROXY_LE_ACME_EMAIL`, `PROXY_LE_ACME_CASERVER`): another CA brings
  its own email and EAB key. What runs acme-dns keeps its software's
  name (the `acmedns` profile, service and command, `PROXY_ACMEDNS_*`).
  `check_settings` (`lib/common.sh`) refuses a resolver Traefik lacks,
  or a DNS-01 one, the default or the wildcard's, without what answers
  it (acme-dns's profile, Cloudflare's token): a router naming one gets
  no certificate, which Traefik only logs. It runs as the release's
  `prepare` step (`check`), before the switch: in `deploy`, the run
  step, a refusal came after it, and the restart of the release before,
  reading the same `shared/.env`, refused too (seen on a test machine,
  2026-10-27); `deploy` runs it as well, for a plain `devopsy deploy`.
  Dashed names work in `TRAEFIK_CERTIFICATESRESOLVERS_LE-HTTP01_...`
  (checked on v3.7, 2026-10-27).
- **A router with no `tls` label gets the entrypoint's resolver,**
  `PROXY_CERTRESOLVER` (its `websecure` copy, when it names no
  entrypoint); one with `tls.certresolver=` empty, or `tls=true`, gets
  none and no certificate at all (checked on v3.7, 2026-10-27). So a
  site that leaves the choice to the server must have no resolver label,
  not an empty one.
- **`PROXY_READ_TIMEOUT` is the websecure entrypoint's
  `readTimeout`:** how long a request, body included, may take to
  arrive. Traefik v3 defaults to 60s, which cuts large uploads like
  registry layers, and entrypoints are static configuration, so
  projects cannot change it with labels: raise it per server, where a
  registry runs.
- **Behind an HTTP proxy, calls to a container's own addresses pass
  `-Y off`:** Docker gives containers the client's proxy settings
  (`~/.docker/config.json`, `proxies`), and busybox's `wget` (Traefik's,
  acme-dns's) ignores `no_proxy`. Fetches from the internet keep the
  proxy.

### Proxies, plugins and Yaegi

- **The proxies plugin changes the request's remote address,** and
  Traefik then builds X-Forwarded-For from it (verified on v3.7.13).
  Traefik strips X-Forwarded-For and X-Real-Ip from untrusted peers
  before middlewares run, hence the forwarded-headers trust for proxies
  using them. Test locally by adding a proxy for Docker's gateway range
  (`192.168.0.0/16` on OrbStack) and sending its header through a
  whoami.
- **Yaegi loses flushes through a wrapped writer:** it hands a plugin's
  ResponseWriter wrapper to Go code as a fixed set of interfaces,
  ResponseWriter plus Hijacker, never Flusher (yaegi v0.16.1,
  `stdlib/wrapper-composed.go`; traefik/traefik#10269). Websockets work
  (checked on v3.7.13); a response written through the wrapper only
  goes out as Go's buffer fills. `proxies` never wraps the writer.
  http-wasm plugins cannot call the next handler twice.
- **Yaegi differs from Go in ways only `yaegi test` shows** (v0.16.1,
  2026-10-28): a `for` statement's variable is per iteration, so an
  assignment to it in the body never reaches the next one (`for page :=
  1; ; { ... page = next }` read the first page forever); and a map
  index on the left of a multiple assignment stores nothing
  (`m[k], err = f()`). Write `page := 1; for {` and assign through a
  plain variable.
- **A plugin that fails to load stops Traefik**
  (`TRAEFIK_EXPERIMENTAL_ABORTONPLUGINFAILURE=true`). Why: skipped, the
  entrypoint's middleware list still names `proxies`, and every router
  is dropped while the container looks healthy; stopped, it restarts in
  a loop, `up --wait` fails and the release rolls back. Checked on
  v3.7.13 with a broken plugin.
- **Changed plugins need a new container** (`devopsy.plugins`, their
  checksum, which deploy keeps in `PROXY_PLUGINS_SUM`): Traefik loads
  them when it starts, and a release changing only a plugin neither
  recreated nor restarted it, so the old one ran on (seen on a test
  machine, 2026-10-28, where a `devopsy restart` did not load the new one
  either).
- **For a request from a proxy, the plugin leaves the forwarded headers
  as Traefik makes them for an untrusted peer:** X-Forwarded-Host from
  the request's Host, -Port by Traefik's own rule (the Host's port, else
  443 for https and wss, else 80), -Uri, -Method, -Prefix, the TLS
  client certificate's two and X-Real-Ip removed (X-Real-Ip set again
  from the proxy's header). Only X-Forwarded-For and -Proto are the
  proxy's. Why: an entrypoint trusting the proxy (the tunnel's, a load
  balancer's `X-Forwarded-For`) keeps every X-Forwarded-* it passes on,
  and Cloudflare passes its visitor's own (it overwrites only the
  proto), so a visitor through the tunnel set the host Drupal builds
  links on (verified through a real tunnel). Cost: a load balancer's own
  X-Forwarded-Host and -Port are replaced too.
- **A visitor's `Forwarded` is removed on every entrypoint that reaches
  apps, for every request** (`forwarded@file`, a headers middleware
  `static_env` writes, first on `websecure` and `tunnel`): Traefik never removes RFC 7239's header (checked on v3.7,
  2026-10-28), trusted peer or not, and Traefik itself never sets it, so
  whatever reaches an app was written by a visitor or a proxy. An app
  trusting Traefik as its reverse proxy takes its host and proto
  otherwise (Drupal reads it by default, and answered a 500 when it
  disagreed with X-Forwarded-Host; Symfony when configured to). Decided over removing
  it in the plugin alone (requests from proxies only) and over leaving it
  to each app. Cost: a proxy in front that speaks only `Forwarded` loses
  it, which Traefik never read for the client IP anyway. Not on `web`,
  which only redirects (a project would need a router above the
  redirect's priority to reach an app there): its internal routers, the
  redirect and ACME's, are built before the file provider loads, so with
  `forwarded@file` every start logged both failing, and for that second
  they did (seen on a test machine, 2026-10-28). `proxies@file` and
  `tunnel@file` have no internal routers on their entrypoints.

### Network and security

- **The `traefik` network is shared by every routed container**
  (accepted while projects on a server trust each other and none relies
  on a middleware for access control; the templates do not, and only
  their web service joins it): one of them, compromised, reaches the
  others' ports directly, past Traefik's middlewares. What it cannot
  reach: the socket proxy and acme-dns (their own networks) and
  Traefik's API (403).
- **On `traefik`, a service alias resolves to any project's
  container:** Docker's DNS answers `app` with any container carrying
  that alias there, so a service on it that proxies to a sibling must
  use the network-scoped name, `app.<project>_default`
  (devopsy-template-drupal11's `alwaysup`; seen resolving to another
  site's app while its own was stopped).
- **The socket proxy allows only what Traefik's Docker provider calls**
  (version, list, inspect, events): `containers/.*` also let through
  `export`, `archive`, `logs` and `attach` of every container. Inspect
  still shows every container's environment; Traefik needs it for
  labels.
- **Traefik runs read-only and without capabilities:** Docker lets
  containers bind 80 and 443 without `NET_BIND_SERVICE`, and Traefik
  only writes to the mounted `/letsencrypt` (no `plugins-storage` for
  local plugins; checked on v3.7.13).
- **The API and dashboard are allowed from localhost and the gateways
  only** (`api_access` in `lib/common.sh`, after every `up` in `deploy`
  and `restart`): the `traefik` entrypoint (`:8080`) listens on every
  network Traefik joins, `traefik` included, so a router with an
  `ipAllowList` of localhost (`exec`, for `lib/facts`) and the gateways
  of Traefik's networks (the published port, through docker-proxy or
  NAT), read from the running container, guards it. Docker 29 prints a
  missing gateway as `invalid IP`, and one bad range makes the router
  invalid: hence the filter. Checked on Debian 13 with Docker 29: host
  200, a container on `traefik` 403.

### The domains report

- **`domains` checks from the server, not the operator's machine:** DNS
  over HTTPS to 1.1.1.1, TLS to the server's own public IP (works on
  DigitalOcean; validity from the verified handshake's verbose line),
  and a request through what DNS returns, all in the `curl` service. It
  prints the `devopsy --probe` line for the outside view: a server
  never drives local devopsy.
- **The public IP comes from `PROXY_PUBLIC_IP`,** else 1.1.1.1's
  `/cdn-cgi/trace`: the default route's address is private behind NAT,
  as on OrbStack.
- **Only names matching a host name reach the check script,** whose
  tab-separated lines jq turns into JSON. Why: hosts come from every
  project's rules, and one stray quote broke hand-built JSON for the
  whole server.
- **Cloudflare's proxy is recognized by `cf-ray` on the HTTPS answer,
  else on plain HTTP:** its free edge certificate covers one level
  below the zone only, so a proxied `*.prod1.example.com` fails the
  handshake at the edge while HTTP still answers.

### Cloudflare Tunnel

Decided 2026-10-28, with the connectors' template
(devopsy-template-cloudflared).

- **The connectors are a template of their own, with instances,** one
  per Cloudflare account (`@vm1:own/prod`), never services here. Why: a
  tunnel belongs to an account (a zone of another account cannot point
  at it: error 1033, as when no connector is up), compose cannot make a service per token, and
  devopsy already runs several installs of one project. Here only the
  other end, always there.
- **The tunnel end is static and always there:** the `tunnel`
  entrypoint (`:8081`, plain HTTP, never published) and the `tunnel`
  network (fixed name, only Traefik and the connectors on it). Why: a
  tunnel added or removed later is a release of its own, with no
  Traefik restart and nothing down; only `PROXY_TUNNEL_ONLY` is a later
  static change.
- **Never the shared `traefik` network for the connectors:** trust is by
  the `tunnel` network's subnet, and a project on `traefik` reaching
  `:8081` is not in it, so it cannot claim a visitor's IP or `https`.
  The connectors address Traefik as `traefik.tunnel`, the
  network-scoped name (Network and security).
- **The trust is read from Docker by `static_env`:** the subnet from
  `docker network inspect tunnel` (`tunnel_subnet`), written as the
  tunnel entrypoint's forwarded-headers trust (so `X-Forwarded-Proto:
  https` survives: without it Traefik sets `http` and Drupal builds
  `http://` URLs or loops) and as `mnt/dynamic/tunnel.yaml`, the
  proxies plugin with one entry (`CF-Connecting-IP` from that subnet),
  a middleware of its own (`tunnel@file`) so `devopsy proxies` never
  touches it. A first `up` makes the network, so the `static_env` after
  it restarts Traefik once; a network made again with another subnet
  is caught the same way.
- **Default entrypoints are always explicit:** `PROXY_TUNNEL_ONLY=true`
  makes `tunnel` the only `asDefault` entrypoint, otherwise `web` and
  `websecure` are. Why: with none marked, Traefik attaches routers to
  every entrypoint, so adding `tunnel` would have put every direct site
  behind any tunnel on the server. A site on a mixed server names it
  (`PROXY_ENTRYPOINTS=tunnel` in the site templates), without a
  resolver: a router with one is a TLS router, which never matches the
  tunnel's plain HTTP. `check_settings` refuses any other value of
  `PROXY_TUNNEL_ONLY` (a typo would leave every site on the open ports).
- **Tunnel only keeps 80 and 443 published, on loopback:** `setup` sets
  `PROXY_HTTP_PORT=127.0.0.1:80` and `PROXY_HTTPS_PORT=127.0.0.1:443`,
  the provider's firewall closes them too. Why not conditional `ports:`:
  compose cannot leave an entry empty.
- **No checks of the wildcard's resolver on a tunnel-only server:** a
  certificate requested there is unused, not harmful (lean checks), and
  `setup` sets `PROXY_WILDCARD_CERTRESOLVER=none`.
- **`domains` treats a host whose routers are on `tunnel` only as
  tunneled** (`method: tunnel` in `lib/facts`): no handshake with the
  server's IP (no port), the request through Cloudflare with its error
  code from the body (`error code: 1033`, what Cloudflare sends curl),
  and the connectors from `docker network inspect tunnel`, by their
  compose health (the connectors' healthcheck is `cloudflared tunnel
  ready`). A 1033 is both "no connector" and "a record in another
  account than the tunnel's" (verified: no 1014 for a tunnel), so the
  next step tells them apart by whether this server's connectors are
  healthy.
- **`setup` prints the Cloudflare side, with values, and calls no
  API:** creating the tunnel, its route, the DNS record and the zone
  settings stay the operator's, in the account that owns them.

### Holding requests and stopped containers

- **Holding is a plugin off the response path, for the sites that ask**
  (`hold@file`, `hold_middleware`, decided 2026-10-28). Why: drupal11's
  `alwaysup` answered only once Traefik dropped the app's router, and a
  stopping app closes its port before that (FrankenPHP on SIGTERM;
  Traefik reads containers on `die` and `health_status` events only), so
  nearly every stop gave one 502; projects without such a router got the
  404. The first `hold` plugin (removed 2026-10-13) failed on two
  measured counts, which this one avoids by construction: it retried
  its `next`, the chain built for the configuration the request entered,
  so it never followed a recreate or a dropped router; and it wrapped
  the writer, which loses flushes under Yaegi (above). This one never
  calls `next` and never carries a site's response: each probe is a new
  request through Traefik's own entrypoint (so through its current
  routers), and the visitor gets a 307 to the same URL once a router
  answers, which the browser follows into the site on the compiled
  path. The 307 comes only on that evidence, never on a timeout, so it
  cannot loop (but for an `ipAllowList` refusing 127.0.0.1: README).
- **The 502 comes through the errors middleware, per router:** it is
  compiled code on the response path (a match only fetches from the
  hold service, Traefik itself on the loopback `hold` entrypoint, where
  the `hold` router runs the plugin) and passes everything else through
  unbuffered. It gives the client the original status or a fixed
  rewrite, never the error service's, and copies its headers: so
  `statusRewrites` 502 to 307 and the hold's `Location`. Per router, not
  on the entrypoints (decided with the user: opt in, never out): on an
  entrypoint the rewrite would turn every site's 502 into a 307, so a
  site opting out could not get its 502 back. `X-Devopsy-Hold: no` gets
  the hold's answer at once with `X-Devopsy-Hold: down` (`domains`).
  Only 502: a site's own 503 is its answer.
- **The router gone: a catch-all with a memory from the API.** Traefik
  builds every middleware again on each dynamic configuration change, so
  the plugin's `New` is the event: a catch-all instance has
  `/api/http/routers` read then, and 0.25, 1 and 3 s later (one reader;
  a change starts them over), and remembers the literal `Host()`s of
  routers naming `hold@file`, with their entrypoints (the errors path
  probes through one of them: a tunneled site has only `tunnel`).
  Measured on 3.7.16: the API shows the new configuration about when
  `New` runs, sometimes a moment after (two reads 1 ms apart disagreed,
  and a stop got 404s until the next read), so the memory is a
  difference between reads (present before, absent now: gone), and a
  request for a host the last read had waits while those reads are due
  (up to 5 s: a host with only a path router pays it for its 404 right
  after a change). Package-level state survives the rebuilds. A host
  present is never held by the catch-all (a host with only a path
  router keeps its 404 elsewhere); one gone is
  held for 5 minutes, then the 404 again, so a removed environment does
  not say "back in a moment" for long. Rejected: polling, and Docker
  events (the provider's decisions redone from labels; the API is the
  truth about routers).
- **Neither `allowNonRunning` nor `ALLOWEMPTYSERVICES` is set:**
  `traefik.docker.allowNonRunning=true` keeps a stopped container's
  router with an empty service, and
  `TRAEFIK_PROVIDERS_DOCKER_ALLOWEMPTYSERVICES=true` keeps an unhealthy
  or starting one's; neither helps a request already waiting, and with
  nothing retrying, both only turn 404 into 503.
- **A paused container loses its router at Traefik's next read:**
  Traefik 3.7 filters any container whose status is not `running`
  (`keepContainer`, health aside). It ignores pause and unpause events,
  but any start, die or health_status event on the host (a sync
  stopping a database, a deploy) makes it re-read, and the router comes
  back only at the next such event after the unpause, which a container
  without a healthcheck never produces by itself (checked on OrbStack).
  On Docker 29.9 a paused container with a healthcheck is also
  `unhealthy` at once, and stays so after the unpause until its next
  check, which is the event that brings it back. `docker stop` and
  `down` remove the container from the list, so its router goes either
  way: only another router can answer, a project's own at priority 2
  or more.
- **No server-wide maintenance page:** one (an nginx at priority 1 for
  every host under the wildcard domain) was removed: it answered only
  for environments that were down or removed, and only on wildcard
  URLs, never the custom domains visitors use; a removed environment
  saying "back soon" forever is worse than a 404. The hold's page shows
  only for a host that asked and had a router within 5 minutes.

## Releases

- **Released from a checkout or an operator's alias:** `devopsy
  @<server>:prod --release`, or an alias in
  `~/.config/devopsy/config.yaml` whose `source:` names the checkout;
  servers are the operator's, never in this repository. `config.yaml`
  gives `prod` `mode: image` (only `.devopsy/` is uploaded), `check` then
  `deploy` for `--release`, and `deploy` for `--rollback`. Settings go in the
  server's `shared/.env` through `--vars`.
- **The network projects join is fixed, `traefik`:** one Traefik per
  host (the `proxy` role refuses a second), so nothing varies, and it
  is the public interface other projects name. Changing it fails the
  release while projects are still attached to the old one: compose
  cannot remove a network with endpoints and leaves Traefik stopped.
  Disconnect them and remove the old network first, release Traefik,
  then `docker network connect` them to the new one until each
  project's own release.

## Checks

```sh
cd .devopsy && docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:stable -s sh -x commands/* lib/facts lib/retry lib/common.sh
```

Plugins: `go test` in each plugin's directory, then the same tests under
Traefik's interpreter (Yaegi is stricter than Go), from `plugins-local`:

```sh
go install github.com/traefik/yaegi/cmd/yaegi@v0.16.1   # Traefik's version
GOPATH=$PWD GO111MODULE=off yaegi test github.com/hanoii/devopsy-traefik/proxies
GOPATH=$PWD GO111MODULE=off yaegi test github.com/hanoii/devopsy-traefik/hold
```

Run it locally on other ports, and route a `traefik/whoami` container
through it (network `traefik`):

```sh
PROXY_HTTP_PORT=18080 PROXY_HTTPS_PORT=18443 \
  PROXY_API_PORT=127.0.0.1:18081 devopsy deploy
```

Check all containers are healthy, `http://` redirects, `https://`
reaches whoami, and the Traefik log has no new warnings. Then `devopsy
down`.

lib's scripts run directly with the project's environment loaded, for
example `env $(devopsy --env | grep -v '^#' | xargs)
DEVOPSY_PROJECT_DIR=$PWD/.devopsy sh .devopsy/lib/facts --all` (with the
same test variables).

For acme-dns, add `COMPOSE_PROFILES=acmedns`, a test
`PROXY_ACMEDNS_DOMAIN` and `PROXY_ACMEDNS_PORT=15353`, then query it
with `dig @127.0.0.1 -p 15353 <domain> SOA`.

On macOS, OrbStack fakes ownership changes on bind mounts: test ownership,
a release and the commands on an OrbStack Linux machine. End to end,
with a template importing from it: on an OrbStack machine, as
devopsy-cli's AGENTS.md describes at the end of its Checks.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `build:`...).
