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
  dropped, so PHP backends cannot be fooled by them, and so is a visitor's
  `Forwarded` (RFC 7239), which Traefik itself passes on: an app that
  trusts Traefik as its reverse proxy never takes a visitor's host or
  scheme from it.
- Traefik and acme-dns run as UID 10001, like every devopsy project's
  containers, never as the deploy user, with no capabilities, a read-only
  root filesystem and `no-new-privileges`. devopsy gives them their `mnt`
  directories (`devopsy.mnt.owner=user`, from their `user:`). The dashboard listens on localhost
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

## Routing a project

Join the `traefik` network and add labels:

```yaml
services:
  web:
    image: traefik/whoami:v1.12
    labels:
      - traefik.enable=true
      - traefik.http.routers.myapp.rule=Host(`example.com`)
      # Visitors wait through the app's stops (docs/hold.md).
      - traefik.http.routers.myapp.middlewares=hold@file
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

## Documentation

- [Running Traefik](docs/running.md): settings, upgrades, the dashboard,
  commands from your machine and customizing.
- [Certificates](docs/certificates.md): the resolvers, acme-dns, a
  Cloudflare token, CNAME delegation, staging and production, and
  forgetting domains.
- [Domains](docs/domains.md): wildcard URLs and the `domains` report.
- [Real client IPs behind proxies and CDNs](docs/proxies.md).
- [Behind a Cloudflare Tunnel](docs/tunnel.md).
- [Stopped sites](docs/hold.md): holding visitors through a site's stops.

## License

GPL-3.0. See [LICENSE](LICENSE).
