# devopsy-template-traefik

The Traefik setup for a devopsy server: one per Docker host, routing every
project on it, with automatic Let's Encrypt certificates. It is run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli).

- Traefik v3 with the Docker provider. Containers are only routed if they opt
  in with `traefik.enable=true`.
- Traefik never mounts the Docker socket. It reads a filtered, read-only API
  through [socket-proxy](https://github.com/wollomatic/socket-proxy).
- Optionally, sites only through a Cloudflare Tunnel: no port open but
  SSH (devopsy-template-cloudflared).
- HTTP redirects to HTTPS. Certificates use the HTTP-01 challenge by default,
  with DNS-01 through the server's own acme-dns or Cloudflare.
- Headers that alias others (`X_Forwarded_For` for `X-Forwarded-For`) are
  dropped, so PHP backends cannot be fooled by them.
- Traefik and acme-dns run as UID 10001, like every devopsy project's
  containers, never as the deploy user, with no capabilities, a read-only
  root filesystem and `no-new-privileges`. devopsy gives them their `mnt`
  directories (`devopsy.mnt.owner`). The dashboard listens on localhost
  only.
- Released like any devopsy project: upgrades and settings changes are
  releases, and a bad one rolls back.
- The host needs Docker and devopsy only: commands use Traefik's own
  `wget` and the jq, curl and Python images (profile `tools`: sqlite is
  Python's module, curl the `curlimages/curl` image).

## Using this template

A starting point to own, not a dependency: start a project from it on
GitHub ("Use this template"), or clone it and keep this repository as a
remote (`upstream`) to pull its changes when you choose. Nothing updates
your copy, or what runs on your servers, but your own release.

The other templates (whoami, drupal11, registry) route through it and
import its base domain, so a change here can break them: keep its
interface (`devopsy.role=proxy`, the `BASE_DOMAIN` export, the
`traefik` network, the `le-http01`, `le-dns01-selfhosted` and
`le-dns01-cloudflare` resolvers, the `web`, `websecure` and `tunnel`
entrypoints, the `tunnel` network), or change the sites with it.

## Setup

A server needs Docker, a deploy user in the `docker` group, and
[devopsy](https://github.com/hanoii/devopsy-cli).
[devopsy-server](https://github.com/hanoii/devopsy-server) sets that up on
Debian. Then, from your checkout:

```sh
devopsy @vm1:prod setup   # vm1: the server's SSH destination
```

It asks for the settings (email, wildcard URLs and their certificates,
or a Cloudflare Tunnel, the resolver sites use by default, Cloudflare's
proxy), writes them with `--vars`, releases, prints the DNS
records to create (the wildcard's DNS only, never proxied; for a tunnel,
the Cloudflare steps and its proxied record), checks them
with `domains`, and adds the alias below under a name you give. It
defaults to production certificates; answer no for staging. Each step is
a devopsy command, printed before it runs; run it again to change a
setting, like the wildcard's resolver. The rest of this section is the
same by hand.

Its one environment, `prod`, is the same on every server:
`devopsy @<server>:prod` from your checkout (`traefik/prod` under the
server's release root, compose project `traefik-prod`). Add an alias per
server to your user config, with `source:` naming your checkout: releases
run only from there, and every other command works from any directory.

```yaml
# ~/.config/devopsy/config.yaml
aliases:
  vm1-traefik: {source: ~/src/devopsy-template-traefik, to: "devopsy@203.0.113.10:prod"}
```

Set its settings before the first release, then release it:

```sh
devopsy @vm1-traefik --vars set --show PROXY_LE_ACME_EMAIL
devopsy @vm1-traefik --release
```

`.devopsy/.env.example` lists every setting. They live in the server's
`traefik/prod/shared/.env` (under its release root); change one with
`--vars set`, then `devopsy @vm1-traefik --reload` (or the next release)
applies it, through `deploy` (`reload: deploy`); a setting that turns a
profile on (`COMPOSE_PROFILES`) needs a release, which pulls its image. A release first runs `check`, which refuses settings
Traefik would take without a word (a resolver it lacks or that nothing
answers), before anything changes. `deploy` is what
each release runs then: it prepares `mnt/`, writes the public wildcard
certificate, refreshes trusted proxies' ranges and starts or updates
every service.

Upgrading is a release from a newer checkout; `devopsy @vm1-traefik
--rollback` goes back. Routed sites see a
few seconds without Traefik when its container is recreated.

Traefik floats on its minor (`traefik:v3.7`): every release pulls the
image again, so patches arrive with your next release. A new minor brings
[migration notes](https://doc.traefik.io/traefik/migrate/v3/) and a new
Yaegi for the plugins, so you move to it by editing the tag in
`.devopsy/compose.yaml`. socket-proxy, acme-dns and the tools (jq, curl)
publish no minor tag and stay exact pins.

Without a CA server in `.env`, certificates come from Let's Encrypt
**staging**. Switch every resolver with:

```sh
devopsy @vm1-traefik letsencrypt production   # or staging; no argument shows the current one
```

It sets the CA server in `.env`, moves the old environment's accounts and
certificates aside so they are not served until renewal, keeps the acme-dns
registrations, and restarts Traefik.

`devopsy restart` pulls newer images and recreates what changed.

## Routing a project

Join the `traefik` network and add labels:

```yaml
services:
  web:
    image: traefik/whoami:v1.12
    labels:
      - traefik.enable=true
      - traefik.http.routers.myapp.rule=Host(`example.com`)
      # Only needed when the container exposes several ports.
      - traefik.http.services.myapp.loadbalancer.server.port=80
    networks: [default, traefik]

networks:
  traefik:
    name: traefik
    external: true
```

Router names must be unique on the host. Prefix them with the project name.

The `traefik` network is shared: every container on it can reach every other
one, on any port, without going through Traefik. So:

- Only the service Traefik routes joins it. Databases, caches and workers
  stay on the project's own network.
- Do not rely on Traefik middlewares (basic auth, IP allow lists) as a
  service's only protection: another project's container reaches it
  directly. The application checks access itself.

That is fine for projects that trust each other, the usual case on a
server; projects that do not belong on separate servers.

## Certificates

Three resolvers, all Let's Encrypt, named by CA (`le`), ACME challenge
and, for DNS-01, who answers it. Every router uses this server's default,
`PROXY_CERTRESOLVER` in `.env` (`le-http01` when unset), unless it picks
one with `traefik.http.routers.<name>.tls.certresolver=<resolver>`. The
site templates leave it to the server unless their own
`PROXY_CERTRESOLVER` names one.

- **`le-http01`, HTTP-01.** Always available. Works for any domain that
  points at this server, with or without a CDN proxy like Cloudflare in
  front. Traefik answers the challenge on port 80 before the HTTPS redirect.
- **`le-dns01-selfhosted`, DNS-01 through this server's own acme-dns.** No DNS provider
  token: each domain needs one CNAME, created once by whoever manages its
  DNS. See below.
- **`le-dns01-cloudflare`, DNS-01 through Cloudflare's API.** Set a token as
  `PROXY_CLOUDFLARE_DNS_API_TOKEN` (`--vars set`, hidden prompt). See
  below.

Both DNS-01 resolvers work with a CDN proxy on, before DNS points at this
server, and for wildcard certificates.

### acme-dns

[acme-dns](https://github.com/joohoi/acme-dns) is a small DNS server that only
serves ACME challenge records. Each server runs its own, for its own
subdomain, and Traefik updates it through an API that only Traefik can reach.
This is how hosting panels like Forge do DNS validation.

1. Pick a subdomain of a domain you control, unique per server, and turn
   the service on in the server's `.env`:

   ```dotenv
   COMPOSE_PROFILES=acmedns
   PROXY_ACMEDNS_DOMAIN=acme-vm1.example.com
   ```

   acme-dns listens only on the server's public IPv4, which avoids clashing
   with systemd-resolved on 127.0.0.53:53. `deploy` detects it and saves it
   as `PROXY_ACMEDNS_IP`; set it yourself behind NAT.

2. Run `devopsy @vm1-traefik --release` (the profile's image comes with
   a release), then `devopsy @vm1-traefik acmedns`.
   It prints two records to create once in `example.com`: an A record for
   `ns-acme-vm1.example.com` and an NS record delegating
   `acme-vm1.example.com` to it.
3. Open port 53, UDP and TCP, in your provider's firewall.
4. Route a site with the `le-dns01-selfhosted` resolver. Its first attempt registers the
   domain and fails with a "CNAME required" error in Traefik's log. Run
   `devopsy @vm1-traefik acmedns` again: it lists the CNAME each domain
   needs, like

   ```
   _acme-challenge.client.org.  CNAME  5400f461-...acme-vm1.example.com.
   ```

   Once it exists, make Traefik try again. It only retries when a router
   changes, and recreating an identical container does not count:
   `devopsy @<server>-traefik domains <compose project> --retry` asks again
   without a restart (see "Checking domains"). Renewals need nothing more.

Each domain's credentials can only change its own challenge record, so
nothing here can touch real DNS records. acme-dns does not resolve other
names, so port 53 is not an open resolver.

The registrations live in `mnt/letsencrypt/acme-dns-accounts.json`. A site
moving to another server registers again there and needs its CNAME changed to
the new server's name. Do that before switching the site's DNS: the old
server's certificate stays valid meanwhile.

### Cloudflare API token

Create a custom token in Cloudflare (My Profile > API Tokens > Create Token >
Custom token) with:

- **Permissions:** Zone > Zone > Read, and Zone > DNS > Edit. Read finds the
  zone of a record and Edit writes the challenge TXT record. Nothing else.
- **Zone resources:** Include > Specific zone, only the zones that receive
  challenge records. Not "All zones".
- **Client IP filtering:** optionally the server's IP, so the token is useless
  anywhere else.

Use one token per server: a leaked token then only exposes that server's
zones. It is unrelated to any token your applications use, for example to
purge Cloudflare's cache.

### Propagation checks

Before asking Let's Encrypt, Traefik checks the challenge TXT record: first
through recursive resolvers, `PROXY_DNS_RESOLVERS` (default the host's,
`127.0.0.11:53` through Docker, then 1.1.1.1 and 1.0.0.1; each is asked in
turn when one fails or has no answer yet), then at the zone's authoritative
nameservers on port 53.

Behind a firewall that blocks outbound DNS, skip the checks and wait
instead:

```sh
devopsy @vm1-traefik --vars set --show PROXY_DNS_SKIP_CHECKS PROXY_DNS_PROPAGATION_DELAY
# true, 60s
```

Checking through the host's resolver alone does not work: Traefik looks the
name up before writing the record, and the resolver caches that it does not
exist, up to the zone's negative TTL. Waiting is reliable: Cloudflare's API
writes the record within seconds, and Let's Encrypt asks the authoritative
nameservers itself.

### DNS-01 for any domain: CNAME delegation

Let's Encrypt checks a TXT record at `_acme-challenge.<domain>`. It follows a
CNAME there, and so does Traefik when it writes the record. So a domain can
hand its challenge to a zone you control, without giving you access to its
DNS:

1. Add a dedicated domain to Cloudflare as its own zone, used only for
   challenges, for example `example-acme.com`. The free plan is enough. Use a
   separate domain rather than a subdomain of a zone you already have: a
   token's scope is a whole zone, so a subdomain would mean giving the token
   that entire zone.
2. At the domain's DNS host, create once:

   ```
   _acme-challenge.client.org.  CNAME  client.org.example-acme.com.
   ```

   For a wildcard certificate the same record covers `*.client.org`.
3. Route the site with the `le-dns01-cloudflare` resolver. Traefik follows the CNAME,
   writes the TXT record at `client.org.example-acme.com` and removes it once
   validated. Renewals need nothing more.

The token then only needs the challenge zone, however many domains delegate
to it. The zones the sites live in stay out of its reach, even when they are
on Cloudflare too, which makes this the safest setup and not only the one for
outside domains.

Keep the CNAME as long as the site uses the `le-dns01-cloudflare` resolver. Removing it
breaks the next renewal, about 30 days before expiry.

## Wildcard URLs

With a base domain for the server, like `vm1.example.com`, every
environment gets a URL next to its own domains, its compose project's:
`whoami-prod.vm1.example.com`. Set it once, in this Traefik's settings, as
`PROXY_BASE_DOMAIN`. Traefik exports it to projects as `BASE_DOMAIN` with
devopsy's labels (devopsy-cli's README, "Roles, exports and imports"):

```yaml
# this template's traefik service
- devopsy.role=proxy
- devopsy.export.BASE_DOMAIN=${PROXY_BASE_DOMAIN:-}
```

A project imports it, and each of its releases writes it into the release's
`devopsy-target.env`, unless the target sets `PROXY_BASE_DOMAIN` itself
(empty: no automatic URL). A release fails while no proxy runs, unless the
import ends in `?`. devopsy-cli knows no URL: the site templates compute
their hosts from the compose project name (`COMPOSE_PROJECT_NAME`, which
devopsy sets), the imported domain and the environment's
`PROXY_DOMAINS`, in their `env` capability
(`.devopsy/capabilities/env/compute`), and use the resulting rule in their
labels:

```yaml
labels:
  - devopsy.import.PROXY_BASE_DOMAIN=proxy/BASE_DOMAIN
  - traefik.enable=true
  - traefik.http.routers.${COMPOSE_PROJECT_NAME:-app}.rule=${SITE_HOST_RULE:-HostRegexp(`^app\.localhost$`)}
```

The fallback is a `HostRegexp` because Traefik requests no certificate for
one: an environment without hosts would otherwise log an ACME error for
`app.localhost` on every attempt.

`SITE_HOST_RULE` matches the wildcard host plus `PROXY_DOMAINS`, the
environment's own domains, set in its `env` in config.yaml or its `.env`
(on a server, the target's `shared/.env`):

```dotenv
PROXY_DOMAINS="example.org www.example.org"
```

Without a base domain, a released environment only answers on its
`PROXY_DOMAINS`; locally, its host is `<project>.localhost`, which
browsers resolve to the local machine.

DNS: one wildcard record, `*.vm1.example.com A <server IP>`. With
`PROXY_BASE_DOMAIN` set, `deploy` also requests one wildcard
certificate for all wildcard URLs (`mnt/dynamic/public-wildcard.yaml`),
which avoids Let's Encrypt's limit of 50 certificates per domain a week. It
needs a DNS-01 resolver, `PROXY_WILDCARD_CERTRESOLVER`: `auto` (`le-dns01-selfhosted`
when acme-dns is set up, else `le-dns01-cloudflare` with a token, else
none), `le-dns01-selfhosted`, `le-dns01-cloudflare`, or `none` for one
HTTP-01 certificate per wildcard URL. With `le-dns01-selfhosted`,
`devopsy acmedns` then lists the CNAME for
`_acme-challenge.vm1.example.com`.

Files in `mnt/dynamic/` are Traefik dynamic configuration (file provider), for
anything else that does not belong to a project.

## Real client IPs behind proxies and CDNs

Behind a proxy (Cloudflare, Fastly, a load balancer, another nginx...),
Traefik sees the proxy's IPs, so applications would log and rate-limit those
instead of visitors. The bundled local plugin (`plugins-local/`, standard
library only, nothing downloaded) fixes that for any number of proxies, each
with its ranges and the header it passes the visitor in:

```sh
devopsy proxies add cloudflare                        # preset: its published ranges, CF-Connecting-IP
devopsy proxies add fastly Fastly-Client-IP https://example.com/fastly-ranges.txt
devopsy proxies add lb X-Forwarded-For 10.0.0.0/16    # your own load balancer
devopsy proxies                                       # list
devopsy proxies refresh                               # fetch URL ranges again (no restart)
devopsy proxies remove lb
```

Ranges are CIDRs, or `https` URLs listing one per line. For a request from a
proxy's ranges, the visitor's IP (for `X-Forwarded-For`, the rightmost address
that is not a trusted proxy) becomes the client IP: Traefik sends it as
`X-Forwarded-For` and `X-Real-Ip`, so applications keep trusting only
Traefik. Any other request loses the proxies' headers, so they cannot be
spoofed by reaching the server directly. Proxied and direct sites work on the
same server: each request is judged by who connected.

The proxies are kept in `mnt/proxies/proxies.conf`. Adding the first proxy,
removing the last one, or changing the ranges of an `X-Forwarded-For` or
`X-Real-Ip` proxy (those are also trusted for Traefik's forwarded headers)
restarts Traefik; anything else does not. Those are static settings, which
`deploy` and `devopsy proxies` write together in `mnt/traefik.env`.

Every `deploy`, so every release, fetches URL ranges again. Cloudflare's
change rarely. For regular refreshes in between, schedule `devopsy
@vm1-traefik proxies refresh` from CI or locally, or add a cron to the
deploy user's crontab on the server (the cron below assumes the release
root is the deploy user's home); the release lock keeps it out of a
release:

```cron
17 4 * * 1  cd ~/traefik/prod/current && flock ~/traefik/prod/.lock devopsy proxies refresh
```

Load balancers that pass TCP connections with the PROXY protocol (HAProxy, most
cloud network load balancers) need no plugin: Traefik supports it natively,
for example `TRAEFIK_ENTRYPOINTS_WEBSECURE_PROXYPROTOCOL_TRUSTEDIPS` in a
compose override.

## Behind a Cloudflare Tunnel

A site behind Cloudflare's proxy still answers anyone who reaches the
server directly: its IP is no secret, and Certificate Transparency logs
list every name it has a certificate for. Cloudflare's WAF then protects
nothing against a scanner. Through a [Cloudflare
Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)
the server publishes no port but SSH: `cloudflared` connects out to
Cloudflare, and visitors' requests come back through it.

```
visitor -> Cloudflare's edge (TLS, WAF, Access) -> tunnel -> cloudflared
        -> http://traefik.tunnel:8081 (the tunnel entrypoint) -> the site
```

The connectors are a template of their own,
[devopsy-template-cloudflared](https://github.com/hanoii/devopsy-template-cloudflared),
released after this one. This template always has the other end:

- the `tunnel` entrypoint (`:8081`, plain HTTP, never published) and the
  `tunnel` network, which only Traefik and the connectors join;
- trust of that network's subnet, read from Docker by every `deploy`: the
  visitor's IP from `CF-Connecting-IP` (the proxies plugin, built in:
  `devopsy proxies` lists it) and `X-Forwarded-Proto: https`, so
  applications build `https://` URLs. A request to port 8081 from
  anywhere else (another project on the `traefik` network) is not
  trusted.

So tunnels come and go as releases of their own, with no Traefik restart.
The first release with the tunnel entrypoint recreates Traefik twice: once
for the entrypoint, once for its trust, read from the network that first
start made.

### Setting one up

`setup`'s fifth answer, "Through a Cloudflare Tunnel", sets everything and
then prints the Cloudflare steps with the values filled in:

- `PROXY_TUNNEL_ONLY=true`: routers that name no entrypoint attach to the
  tunnel's only, never to `web` and `websecure`;
- `PROXY_HTTP_PORT=127.0.0.1:80` and `PROXY_HTTPS_PORT=127.0.0.1:443`:
  nothing answers on the server's public address;
- `PROXY_WILDCARD_CERTRESOLVER=none`: no certificate on the server, TLS
  ends at Cloudflare's edge;
- `PROXY_BASE_DOMAIN`, ideally a zone's apex (`example.com`, URLs like
  `whoami-prod.example.com`): Cloudflare's free edge certificate covers
  one level below the zone only.

In the Cloudflare account that holds the zone:

1. Networking > Tunnels: create a tunnel (cloudflared) and copy its
   token. The dashboard waits for a connector before going on: release
   devopsy-template-cloudflared then, from its checkout:

   ```sh
   devopsy @vm1:own/prod --vars set PROXY_TUNNEL_TOKEN
   devopsy @vm1:own/prod --release
   ```

2. A published application route: `*.example.com` to
   `http://traefik.tunnel:8081`, type HTTP. A site's own domain gets a
   route of its own.
3. DNS: `*.example.com` CNAME `<tunnel id>.cfargotunnel.com`, proxied.
   The dashboard creates it with the route: check it is there.
4. SSL/TLS: Always Use HTTPS (requests reach Traefik as plain HTTP, past
   its redirect). The encryption mode does not apply to the tunnel's
   hostnames: leave it as it is, Full (strict) for any record of the
   zone that reaches a server directly.
5. At your provider's firewall, inbound SSH only; outbound port 7844, UDP
   (QUIC) or TCP.

Then check with `devopsy @vm1-traefik domains`.

### A tunnel per Cloudflare account

A tunnel belongs to an account, not to a zone or a server: any zone of
that account points a proxied CNAME at it, and a zone of another account
cannot (Cloudflare's error 1033, as when no connector is up). A server
serving one client whose account holds every zone needs one tunnel. Each
further account is another instance of the tunnel template
(`@vm1:acme/prod`), with that account's token, and every instance ends
at the same entrypoint: a site's one router answers its names whatever
tunnel they came through.

A route creates its DNS record only when the name has none: moving a
name to another tunnel, remove its record first, then add the route (or
point the record at `<new tunnel id>.cfargotunnel.com`).

The simplest shape is the client's own: their account, their zones, their
server, your membership in their account revocable. On a shared server of
your own, two ways to serve a client's domain:

- **Cloudflare for SaaS:** the client's name is a custom hostname in your
  zone, which the client points at with a CNAME and proves with a TXT
  record. Some custom hostnames are included per zone, then a small fee
  each (Cloudflare's pricing); an apex domain needs the client's DNS on Cloudflare, or `www`.
  Your edge and your WAF rules; if you leave, the client re-points one
  record.
- **A tunnel instance from the client's account:** their token, their
  edge and WAF, their DNS; you run only the connector.

### Wildcard URLs one level deeper

The free edge certificate covers `*.example.com`, not `*.vm1.example.com`.
For a second server's wildcard URLs in the same zone:

- Advanced Certificate Manager on the zone (a monthly fee per zone, not
  per server): one certificate listing each server's wildcard;
- a zone per server, its wildcard URLs at that zone's apex;
- no wildcard URLs on that server (`PROXY_BASE_DOMAIN` empty): sites only
  on their own domains, one level down and always covered.

This is Cloudflare's proxy, not the tunnel: a proxied two-level wildcard
fails at the edge without a tunnel too.

### Tunneled and direct sites on one server

Without `PROXY_TUNNEL_ONLY`, routers that name no entrypoint stay on `web`
and `websecure`: a tunnel opens no site that does not ask. A site goes
through the tunnel only by naming it, `PROXY_ENTRYPOINTS=tunnel` in the
site templates (with `PROXY_CERTRESOLVER` unset: a router with a resolver
is a TLS router, which never matches the tunnel's plain HTTP). Such a site
answers nothing on ports 80 and 443, its Host header and SNI included.

### What `domains` reports

A host whose routers are on the tunnel entrypoint only is checked through
Cloudflare: DNS (proxied), the request through the tunnel and its status,
Cloudflare's error code when there is one (1033: no connector of the
record's tunnel, or, with this server's connectors up, a record naming
another tunnel or one of another account), never a certificate here. A section lists
the connectors on the `tunnel` network, each with its health: healthy once
connected to Cloudflare. Tunneled hosts are left out of the `devopsy
--probe` line: there is no port to probe.

### What the edge covers

What a tunnel and Cloudflare's Free plan give, and what stays yours. The
host's half (firewall, SSH, upgrades, backups) is in
[devopsy-server's README](https://github.com/hanoii/devopsy-server#things-to-know).

Covered, on the Free plan:

- No port to scan: the server's address answers SSH only, so requests
  reach a site only through Cloudflare, its WAF and its rules.
- The Free Managed Ruleset (Security > WAF), which Cloudflare updates for
  widespread flaws, often before a patched image exists.
- One rate limiting rule, Bot Fight Mode, Always Use HTTPS.
- Cloudflare Access for up to 50 users: put children (`pr-*`, staging)
  or an admin path behind a login, at the edge.
- Tunnel health alerts (Notifications), by email or webhook.

Not covered:

- The application's own bugs, and its updates: a release brings them
  (devopsy-cli's README, "Choosing tags").
- The Cloudflare account itself: two-factor authentication, and who is a
  member. The tunnel's token runs that tunnel only, and can be refreshed
  in the dashboard.
- Requests Cloudflare lets through: the WAF blocks known patterns, not
  every exploit.

Limits of the Free and Pro plans, through a tunnel as through the proxy:
a request body of 100 MB at most (a larger upload, or a registry's layer
push, fails with 413), and an answer within 100 seconds (else 524).

## Stopped sites

Traefik drops a stopped container's router, so its hosts get Traefik's 404
at once: only another router can answer them, and that router belongs to
the project, which knows its own domains and whether its app is coming back.

A small service with a router on the app's rule at `priority=2`. A
router's priority defaults to its rule's length, so the app's router wins
while the app runs; when the app is stopped, recreated or unhealthy,
Traefik drops its router and the other one answers.
devopsy-template-drupal11's `alwaysup` is the worked example: a Caddy that
tries the app every second for up to a minute and streams its answer, so
visitors who arrive during a deploy or a sync get the real page, late, and
a maintenance page only when the app does not come back in time, or at
once in its maintenance mode. Give such a router the app's certificate
resolver label too, no `devopsy.data` label and no `depends_on` on the app
(it would stop with it): a sync or an import never stops it.
Nothing of it needs this Traefik beyond routing.

## Dashboard

It is published on `127.0.0.1:8080`. From your machine:

```sh
ssh -L 8080:127.0.0.1:8080 your-server
```

Then open <http://localhost:8080>.

The API and dashboard answer Traefik itself and the host only, never
projects' containers, although port 8080 listens on every network Traefik
joins: `deploy` and `restart` write `mnt/dynamic/devopsy-api.yaml`, a
router with an IP allowlist of localhost and the gateways of Traefik's
networks, where connections to the published port come from. Until the
first `deploy` finishes, the API answers 404.

## From your machine

With the alias from Setup, the commands here (`proxies`,
`acmedns`, `letsencrypt`, `deploy`, logs) run from any directory:

```sh
devopsy @vm1-traefik proxies add cloudflare
devopsy @vm1-traefik acmedns
```

## Checking domains

```sh
devopsy @vm1-traefik domains whoami-prod          # one compose project's hosts
devopsy @vm1-traefik domains                      # every host, and the wildcards
devopsy @vm1-traefik domains whoami-prod --retry  # ask again for missing certificates
```

For a compose project, the hosts its running containers' Traefik rules
name; without one, every host Traefik routes and the public wildcard. Per
host: what Traefik knows (routed, resolver, how the certificate is issued,
the acme-dns CNAME it needs, from `lib/facts`), then, from the server
itself, DNS through 1.1.1.1 (over HTTPS), the certificate Traefik presents
for the name, and a request through what DNS returns, so Cloudflare's
proxy and its origin errors (521, 522, 525, 526) are recognized. It ends
each host with what to do next, like the CNAME to create or "certificate
ready: point its DNS at ...", and prints the `devopsy --probe` line that
checks the same hosts from your machine (devopsy-cli). Hosts through a
Cloudflare Tunnel are checked through Cloudflare, with the tunnel's
connectors (Behind a Cloudflare Tunnel).

The server's public IP, for "this server" and the `--probe --ip` line, is
`PROXY_PUBLIC_IP` when set, else what 1.1.1.1 sees (`/cdn-cgi/trace`, so
it works behind NAT), else `PROXY_ACMEDNS_IP`, else the default route's.
Only host names reach the checks: a project's rule that names something
else is listed as not checked.

`--retry` writes a router file asking Traefik for the missing certificates
again (`lib/retry`, no restart), then checks again. Once every routed host
has a valid certificate, `domains` withdraws the request: Traefik keeps and
renews the certificates without it. The checks run in the `curl` service
(profile `tools`), so the host needs no `curl` or `dig`.

## Forgetting domains

Traefik keeps every certificate it was ever given and renews all of them,
routed or not. A domain that is gone (a test site, a server's old wildcard
domain) fails its renewals from 30 days before expiry, with errors in the
log, and its acme-dns registration stays in `devopsy acmedns`.

```sh
devopsy @vm1-traefik forget                         # what is stored, and queued
devopsy @vm1-traefik forget old.example.com         # queue it
devopsy @vm1-traefik forget --now old.example.com   # now: Traefik restarts
devopsy @vm1-traefik forget --cancel                # empty the queue
```

A domain stands for itself and its wildcard: `old.example.com` also forgets
`*.old.example.com`, never `site.old.example.com`. A certificate goes when
any of its names matches, from every resolver's store, with the acme-dns
registration of the domain: in Traefik's file and in acme-dns's own
database. Delete its `_acme-challenge` CNAME afterwards.

Traefik reads its stores only when it starts, so they are edited while it
is stopped. Queued domains wait for the next `deploy` (a release) or
`restart`, which stops Traefik for them: sites are unavailable for a few
seconds. `--now` does that at once, for everything queued. Nothing stops
when nothing is stored for the queued domains. Each edited store keeps a copy next to it
(`mnt/letsencrypt/<file>.forget-<time>`).

Forget a domain once nothing routes it: one a router still names gets a
new certificate as Traefik starts, and through `le-dns01-selfhosted` a new
registration, with a new CNAME.

## Customizing

Put changes in `.devopsy/compose.override.yaml`, which is not committed. On
a server, put it in `shared/`: releases link it in.
Traefik reads its configuration from `TRAEFIK_*` environment variables, so
most changes are extra variables:

```yaml
services:
  traefik:
    environment:
      - TRAEFIK_ACCESSLOG=true
```

One Traefik per host: it holds the `proxy` role, so a release of a second
one on the same server is refused. Its network is always `traefik`.

## License

GPL-3.0. See [LICENSE](LICENSE).
