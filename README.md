# devopsy-traefik

The Traefik setup for a devopsy server: one per Docker host, routing every
project on it, with automatic Let's Encrypt certificates. It is run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli).

- Traefik v3 with the Docker provider. Containers are only routed if they opt
  in with `traefik.enable=true`.
- Traefik never mounts the Docker socket. It reads a filtered, read-only API
  through [socket-proxy](https://github.com/wollomatic/socket-proxy).
- HTTP redirects to HTTPS. Certificates use the HTTP-01 challenge by default,
  with DNS-01 through the server's own acme-dns or Cloudflare.
- Headers that alias others (`X_Forwarded_For` for `X-Forwarded-For`) are
  dropped, so PHP backends cannot be fooled by them.
- Traefik and acme-dns run as UID 10001, like every devopsy project's
  containers, never as the deploy user. The dashboard listens on localhost
  only.
- Released like any devopsy project: upgrades and settings changes are
  releases, and a bad one rolls back.
- The host needs Docker and devopsy only: scripts use Traefik's own `wget`
  and the official jq image.

## Setup

A server needs Docker, a deploy user in the `docker` group owning `/srv`,
and [devopsy](https://github.com/hanoii/devopsy-cli).
[devopsy-server](https://github.com/hanoii/devopsy-server) sets that up on
Debian. Then add a target per server to your user-level targets, with
`source:` naming your checkout of this repository: releases run only from there, and every
other command works from any directory.

```yaml
# ~/.config/devopsy/targets.yaml
vm1-traefik:
  host: devopsy@203.0.113.10
  path: /srv/traefik
  mode: image                    # only .devopsy/: nothing to build
  source: ~/src/devopsy-traefik
  release: &steps
    remote: deploy
  rollback: *steps
```

The checkout's `.devopsy/targets.local.yaml` (not committed) works too, for
releases only.

Set its settings before the first release, then release it:

```sh
devopsy @vm1-traefik --vars set --show TRAEFIK_CERTIFICATESRESOLVERS_LETSENCRYPT1_ACME_EMAIL
devopsy @vm1-traefik --release
```

`.devopsy/.env.example` lists every setting. They live in the server's
`/srv/traefik/shared/.env`; change one with `--vars set`, then `devopsy
@vm1-traefik deploy` (or the next release) applies it. `deploy` is what each
release runs: it prepares `mnt/`, writes the public wildcard certificate,
refreshes trusted proxies' ranges and starts or updates every service.

Upgrading is a release from a newer checkout; `devopsy @vm1-traefik
rollback` goes back. Routed sites see a few seconds without Traefik when
its container is recreated.

Without a CA server in `.env`, certificates come from Let's Encrypt
**staging**. Switch every resolver with:

```sh
devopsy @vm1-traefik letsencrypt production   # or staging; no argument shows the current one
```

It sets the CA server in `.env`, moves the old environment's accounts and
certificates aside so they are not served until renewal, keeps the acme-dns
registrations, and restarts Traefik.

`devopsy restart` pulls newer images and recreates what changed.

### From a clone

Servers set up before releases have a git clone in `/srv/traefik`. Move it
over once, on the server, as the deploy user:

```sh
cd /srv/traefik && devopsy down
mkdir -p ../traefik.new/shared/mnt/proxies
mv .devopsy/.env ../traefik.new/shared/.env
mv .devopsy/mnt/* ../traefik.new/shared/mnt/
mv .devopsy/proxies.conf .devopsy/proxies.env ../traefik.new/shared/mnt/proxies/ 2>/dev/null
cd .. && mv traefik traefik.clone && mv traefik.new traefik
```

In `shared/.env`, remove `DEVOPSY_UID` and `DEVOPSY_GID`, and add what
devopsy-server used to write elsewhere: the wildcard domain as
`DEVOPSY_PROXY_WILDCARD_DOMAIN` (and `DEVOPSY_WILDCARD_CERTRESOLVER` if not auto),
and the Cloudflare token from
`dns.env` as `DEVOPSY_CLOUDFLARE_DNS_API_TOKEN`. Then release from your
machine. `mv` keeps the ACME files' mode 600; the `init` service hands them
to 10001 on the first start. Certificates and acme-dns registrations carry
over: no DNS changes. Remove `traefik.clone` once everything works.

### From DEVOPSY_WILDCARD_DOMAIN (October 2026)

The wildcard domain setting was `DEVOPSY_WILDCARD_DOMAIN`, the name projects
use, so devopsy gave Traefik a wildcard URL of its own. It is now
`DEVOPSY_PROXY_WILDCARD_DOMAIN`, exported to projects as `WILDCARD_DOMAIN`
(see Wildcard URLs). The next release's `deploy` renames it in
`shared/.env`. Projects then import it with a label. In this order:

1. Upgrade the server's devopsy-cli to v0.17.0 or newer (`devopsy
   --upgrade` as root): this project's own labels need it, and an older one
   refuses the release with an upgrade hint.
2. Release this project, so the running Traefik carries `devopsy.role=proxy`
   and its export.
3. Release each project with the import label. Until then their releases
   keep the domain they had; `devopsy @<target> --debug imports` shows
   which are stale.

## Routing a project

Join the `traefik-main` network and add labels:

```yaml
services:
  web:
    image: traefik/whoami
    labels:
      - traefik.enable=true
      - traefik.http.routers.myapp.rule=Host(`example.com`)
      # Only needed when the container exposes several ports.
      - traefik.http.services.myapp.loadbalancer.server.port=80
    networks: [default, traefik]

networks:
  traefik:
    name: traefik-main
    external: true
```

Router names must be unique on the host. Prefix them with the project name.

## Certificates

Three resolvers. Every router uses `letsencrypt1` unless `DEVOPSY_CERTRESOLVER`
in `.env` changes the default, or the router picks one with
`traefik.http.routers.<name>.tls.certresolver=<resolver>`.

- **`letsencrypt1`, HTTP-01.** Always available. Works for any domain that
  points at this server, with or without a CDN proxy like Cloudflare in
  front. Traefik answers the challenge on port 80 before the HTTPS redirect.
- **`acmedns`, DNS-01 through this server's own acme-dns.** No DNS provider
  token: each domain needs one CNAME, created once by whoever manages its
  DNS. See below.
- **`cloudflare`, DNS-01 through Cloudflare's API.** Set a token as
  `DEVOPSY_CLOUDFLARE_DNS_API_TOKEN` (`--vars set`, hidden prompt). See
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
   DEVOPSY_ACMEDNS_DOMAIN=acme-vm1.example.com
   ```

   acme-dns listens only on the server's public IPv4, which avoids clashing
   with systemd-resolved on 127.0.0.53:53. `deploy` detects it and saves it
   as `DEVOPSY_ACMEDNS_IP`; set it yourself behind NAT.

2. Run `devopsy @vm1-traefik deploy`, then `devopsy @vm1-traefik acmedns`.
   It prints two records to create once in `example.com`: an A record for
   `ns-acme-vm1.example.com` and an NS record delegating
   `acme-vm1.example.com` to it.
3. Open port 53, UDP and TCP, in your provider's firewall.
4. Route a site with the `acmedns` resolver. Its first attempt registers the
   domain and fails with a "CNAME required" error in Traefik's log. Run
   `devopsy acmedns` again: it lists the CNAME each domain needs, like

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
3. Route the site with the `cloudflare` resolver. Traefik follows the CNAME,
   writes the TXT record at `client.org.example-acme.com` and removes it once
   validated. Renewals need nothing more.

The token then only needs the challenge zone, however many domains delegate
to it. The zones the sites live in stay out of its reach, even when they are
on Cloudflare too, which makes this the safest setup and not only the one for
outside domains.

Keep the CNAME as long as the site uses the `cloudflare` resolver. Removing it
breaks the next renewal, about 30 days before expiry.

## Wildcard URLs

With a wildcard domain for the server, like `vm1.example.com`, every project
gets a URL next to its own domains: `<project>.vm1.example.com`. Set it
once, here, as `DEVOPSY_PROXY_WILDCARD_DOMAIN`. Traefik exports it to
projects with devopsy's labels (devopsy-cli's README, "Roles, exports and
imports"):

```yaml
# here, on the traefik service
- devopsy.role=proxy
- devopsy.export.WILDCARD_DOMAIN=${DEVOPSY_PROXY_WILDCARD_DOMAIN:-}
```

A project imports it, and each of its releases writes it into the release's
`target.env`, unless the target sets `DEVOPSY_WILDCARD_DOMAIN` itself
(empty: no automatic URL). A release fails while no proxy runs, unless the
import ends in `?`. devopsy-cli then gives compose files
`DEVOPSY_PROJECT_NAME`, `DEVOPSY_WILDCARD_HOST` and `DEVOPSY_HOST_RULE`:

```yaml
labels:
  - devopsy.import.DEVOPSY_WILDCARD_DOMAIN=proxy/WILDCARD_DOMAIN
  - traefik.enable=true
  - traefik.http.routers.${DEVOPSY_PROJECT_NAME:-app}.rule=${DEVOPSY_HOST_RULE:-HostRegexp(`^app\.localhost$`)}
```

The fallback is a `HostRegexp` because Traefik requests no certificate for
one: an environment without hosts would otherwise log an ACME error for
`app.localhost` on every attempt.

`DEVOPSY_HOST_RULE` matches the wildcard host plus `DEVOPSY_DOMAINS`, the
environment's own domains, set in its `.env` (on a server, the target's
`shared/.env`):

```dotenv
DEVOPSY_DOMAINS="example.org www.example.org"
```

Without a wildcard domain, a released environment only answers on its
`DEVOPSY_DOMAINS`; locally, `DEVOPSY_WILDCARD_HOST` is `<project>.localhost`,
which browsers resolve to the local machine.

DNS: one wildcard record, `*.vm1.example.com A <server IP>`. With
`DEVOPSY_PROXY_WILDCARD_DOMAIN` set, `deploy` also requests one wildcard
certificate for all wildcard URLs (`mnt/dynamic/public-wildcard.yaml`),
which avoids Let's Encrypt's limit of 50 certificates per domain a week. It
needs a DNS-01 resolver, `DEVOPSY_WILDCARD_CERTRESOLVER`: `auto` (acmedns
when configured, else cloudflare with a token, else none), `acmedns`,
`cloudflare`, or `none` for one HTTP-01 certificate per wildcard URL.
With `acmedns`, `devopsy acmedns` then lists the CNAME for
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
restarts Traefik; anything else does not.

Every `deploy`, so every release, fetches URL ranges again. Cloudflare's
change rarely. For regular refreshes in between, schedule `devopsy
@vm1-traefik proxies refresh` from CI or your machine, or add a cron to the
deploy user's crontab on the server; the release lock keeps it out of a
release:

```cron
17 4 * * 1  cd /srv/traefik/current && flock /srv/traefik/.lock devopsy proxies refresh
```

Load balancers that pass TCP connections with the PROXY protocol (HAProxy, most
cloud network load balancers) need no plugin: Traefik supports it natively,
for example `TRAEFIK_ENTRYPOINTS_WEBSECURE_PROXYPROTOCOL_TRUSTEDIPS` in a
compose override.

## Dashboard

It is published on `127.0.0.1:8080`. From your machine:

```sh
ssh -L 8080:127.0.0.1:8080 your-server
```

Then open <http://localhost:8080>.

## From your machine

With the user-level target from Setup, the commands here (`proxies`,
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
checks the same hosts from your machine (devopsy-cli).

The server's public IP, for "this server" and the `--probe --ip` line, is
`DEVOPSY_PUBLIC_IP` when set, else what 1.1.1.1 sees (`/cdn-cgi/trace`, so
it works behind NAT), else `DEVOPSY_ACMEDNS_IP`, else the default route's.
Only host names reach the checks: a project's rule that names something
else is listed as not checked.

`--retry` writes a router file asking Traefik for the missing certificates
again (`lib/retry`, no restart), then checks again. Once every routed host
has a valid certificate, `domains` withdraws the request: Traefik keeps and
renews the certificates without it. The checks run in the `curl` service
(profile `tools`), so the host needs no `curl` or `dig`.

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

`DEVOPSY_ENVIRONMENT` (default `main`) changes the project and network name,
to run a second Traefik on the same host. Change the ports along with it.

## License

GPL-3.0. See [LICENSE](LICENSE).
