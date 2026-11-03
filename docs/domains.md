# Domains

## Wildcard URLs

With a base domain for the server, like `vm1.example.com`, every
environment gets a URL beside its own domains, named after its compose
project: `whoami-prod.vm1.example.com`. Set the base domain once, in this
Traefik's settings, as `PROXY_BASE_DOMAIN`. Traefik exports it to projects
as `BASE_DOMAIN` with devopsy's labels ([Roles, exports and
imports](https://github.com/hanoii/devopsy-cli/blob/main/docs/roles.md)):

```yaml
# this template's traefik service
- devopsy.role=proxy
- devopsy.export.BASE_DOMAIN=${PROXY_BASE_DOMAIN:-}
```

A project imports it, and each of the project's releases writes it into
the release's `devopsy-target.env`, unless the target sets
`PROXY_BASE_DOMAIN` itself (empty for no automatic URL). A release fails
while no proxy runs, unless the import ends in `?`.

devopsy-cli does not compute URLs. The site templates compute their hosts
in their `env` capability (`.devopsy/capabilities/env/compute`), from the compose
project name (`COMPOSE_PROJECT_NAME`, which devopsy sets), the imported
domain and the environment's `PROXY_DOMAINS`, and use the resulting rule
in their labels:

```yaml
labels:
  - devopsy.import.PROXY_BASE_DOMAIN=proxy/BASE_DOMAIN
  - traefik.enable=true
  - traefik.http.routers.${COMPOSE_PROJECT_NAME:-app}.rule=${SITE_HOST_RULE:-HostRegexp(`^app\.localhost$`)}
```

The fallback is a `HostRegexp` because Traefik requests no certificate for
one. Otherwise an environment without hosts would log an ACME error for
`app.localhost` on every attempt.

`SITE_HOST_RULE` matches the wildcard host plus `PROXY_DOMAINS`, the
environment's own domains. They are set in its `env` in `config.yaml` or
in its `.env` (on a server, the target's `shared/.env`):

```dotenv
PROXY_DOMAINS="example.org www.example.org"
```

Without a base domain, a released environment answers only on its
`PROXY_DOMAINS`. Locally, its host is `<project>.localhost`, which
browsers resolve to the local machine.

The DNS for wildcard URLs is one wildcard record, `*.vm1.example.com A
<server IP>`. With `PROXY_BASE_DOMAIN` set, `deploy` also requests one
wildcard certificate for all of them (`mnt/dynamic/public-wildcard.yaml`),
which avoids Let's Encrypt's limit of 50 certificates per domain a week.
It needs a DNS-01 resolver, set as `PROXY_WILDCARD_CERTRESOLVER`:

- `auto`: `le-dns01-selfhosted` when acme-dns is set up, else
  `le-dns01-cloudflare` with a token, else none;
- `le-dns01-selfhosted` or `le-dns01-cloudflare`;
- `none`, for one HTTP-01 certificate per wildcard URL.

With `le-dns01-selfhosted`, `devopsy acmedns` then lists the CNAME for
`_acme-challenge.vm1.example.com`.

## Checking domains

```sh
devopsy @vm1-traefik domains whoami-prod          # one compose project's hosts
devopsy @vm1-traefik domains                      # every host, and the wildcards
devopsy @vm1-traefik domains whoami-prod --retry  # ask again for missing certificates
```

Given a compose project, `domains` checks the hosts its running
containers' Traefik rules name. Without one, it checks every host Traefik
routes, and the public wildcard. For each host it reports:

- what Traefik knows, from `lib/facts`: whether the host is routed, its
  resolver, how its certificate is issued, and the acme-dns CNAME it
  needs;
- from the server itself: DNS through 1.1.1.1 (over HTTPS), the
  certificate Traefik presents for the name, and a request through what
  DNS returns, so Cloudflare's proxy and its origin errors (521, 522, 525,
  526) are recognized;
- what to do next, like the CNAME to create, or "certificate ready: point
  its DNS at ...".

It then prints the `devopsy --probe` line that checks the same hosts from
your machine (devopsy-cli). Hosts behind a Cloudflare Tunnel are checked
through Cloudflare, with the tunnel's connectors ([Behind a Cloudflare
Tunnel](tunnel.md)).

The server's public IP, for "this server" and the `--probe --ip` line, is
`PROXY_PUBLIC_IP` when set, else what 1.1.1.1 sees (`/cdn-cgi/trace`, so it
works behind NAT), else `PROXY_ACMEDNS_IP`, else the default route's. Only
host names reach the checks: a project's rule that names something else is
listed as not checked.

`--retry` writes a router file asking Traefik for the missing certificates
again (`lib/retry`, no restart), then checks again. Once every routed host
has a valid certificate, `domains` withdraws the request, and Traefik keeps
and renews the certificates without it. The checks run in the `curl`
service (profile `tools`), so the host needs no `curl` or `dig`.
