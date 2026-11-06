# devopsy-template-traefik

The Traefik setup for a devopsy server: one per Docker host, routing every
project on it, with automatic Let's Encrypt certificates. It is run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli).

- Traefik v3 with the Docker provider. A container is routed only when it
  opts in with `traefik.enable=true`.
- Traefik never mounts the Docker socket. It reads a filtered, read-only
  API through [socket-proxy](https://github.com/wollomatic/socket-proxy).
- Optionally, sites are served only through a Cloudflare Tunnel, with
  only SSH open (devopsy-template-cloudflared).
- HTTP redirects to HTTPS. Certificates use the HTTP-01 challenge by
  default, or DNS-01 through the server's own acme-dns or Cloudflare.
- Headers that alias others (`X_Forwarded_For` for `X-Forwarded-For`) are
  dropped, since PHP backends would read them as the real ones. A
  visitor's `Forwarded` header (RFC 7239), which Traefik itself would pass
  on, is dropped too, so an app that trusts Traefik as its reverse proxy
  cannot take a visitor's host or scheme from it.
- Traefik and acme-dns run as UID 10001, like every devopsy project's
  containers, never as the deploy user. They have no capabilities, a
  read-only root filesystem and `no-new-privileges`, and devopsy gives
  them their `mnt` directories (`devopsy.mnt.owner=user`, from their
  `user:`). The dashboard listens on localhost only.
- It is released like any devopsy project: upgrades and changes of
  settings are releases, and a bad one rolls back.
- The host needs only Docker and devopsy. The commands use Traefik's own
  `wget` and the jq, curl and Python images (profile `tools`; sqlite
  comes from Python's module, curl from the `curlimages/curl` image).

## Using this template

This template is meant to be copied and changed. Start a project from it
on GitHub ("Use this template"), or clone it and keep this repository as a
remote (`upstream`) to pull its changes when you choose. Nothing from
this repository reaches your copy or your servers until you pull its
changes and release them.

The other templates (whoami, drupal11, registry) route through it and
import its base domain, so a change here can break them. Keep its
interface, or change the sites with it:

- `devopsy.role=proxy` and the `BASE_DOMAIN` export;
- the `traefik` and `tunnel` networks, and the `traefik.docker.network`
  label naming a project's own;
- the `le-http01`, `le-dns01-selfhosted` and `le-dns01-cloudflare`
  resolvers;
- the `web`, `websecure` and `tunnel` entrypoints.

## Setup

A server needs Docker, a deploy user in the `docker` group, and
[devopsy](https://github.com/hanoii/devopsy-cli).
[devopsy-server](https://github.com/hanoii/devopsy-server) sets that up on
Debian. Then, from your checkout:

```sh
devopsy @vm1:prod setup   # vm1: the server's SSH destination
```

`setup` asks for the settings: an email, wildcard URLs and their
certificates or a Cloudflare Tunnel, the resolver sites use by default,
and Cloudflare's proxy. Then it:

1. writes them with `--vars`;
2. releases;
3. prints the DNS records to create: the wildcard's, DNS only and never
   proxied, or for a tunnel the Cloudflare steps and its proxied record;
4. checks them with `domains`;
5. adds the alias below, under a name you give.

It defaults to production certificates; answer no for staging. Each step
is a devopsy command, printed before it runs. Run `setup` again to change
a setting, like the wildcard's resolver. The rest of this section does the
same by hand.

Its one environment, `prod`, is the same on every server: `devopsy
@<server>:prod` from your checkout (`traefik/prod` under the server's
release root, compose project `traefik-prod`). Add an alias per server to
your user config, with `source:` naming your checkout. Releases run only
from there, and every other command works from any directory.

```yaml
# ~/.config/devopsy/config.yaml
aliases:
  vm1-traefik: {source: ~/src/devopsy-template-traefik, to: "devopsy@203.0.113.10:prod"}
```

Set the settings before the first release, then release it:

```sh
devopsy @vm1-traefik --vars set --show PROXY_LE_ACME_EMAIL
devopsy @vm1-traefik --release
```

## Routing a project

Give the routed service a network of the project's own, and name it in a
label. Traefik joins it by itself:

```yaml
services:
  web:
    image: traefik/whoami:v1.12
    labels:
      - traefik.enable=true
      - traefik.http.routers.myapp.rule=Host(`example.com`)
      # The network Traefik reaches it on.
      - traefik.docker.network=${COMPOSE_PROJECT_NAME:-myapp}_proxy
      # Visitors wait through the app's stops (docs/hold.md).
      - traefik.http.routers.myapp.middlewares=hold@file
      # Only needed when the container exposes several ports.
      - traefik.http.services.myapp.loadbalancer.server.port=80
    networks: [private, proxy]
  database:
    image: mariadb:11.8
    networks: [private]

networks:
  # What is only here, like the database, has no way out.
  private:
    internal: true
  proxy:
    name: ${COMPOSE_PROJECT_NAME:-myapp}_proxy
```

Router names must be unique on the host, so prefix them with the project
name.

The `attach` service connects Traefik to that network when the container
appears, and removes the network about 10 seconds after nothing is left
on it. It connects
only a network the container is on, of the same compose project. Traefik
is not restarted, so a project's release never interrupts the others.
`docker compose down` still says `Resource is still in use` about that
network, since Traefik is on it at that moment, and exits 0.

A project's containers reach another project's only through Traefik's
`web` and `websecure` entrypoints, as any visitor does. A site served only
through a tunnel is out of their reach (docs/tunnel.md). Only the services
you put on `proxy` share a network with Traefik, so keep databases, caches
and workers off it.

Moving a running project works by a release, and back by a rollback, as long
as its private network is a new one like `private` above. Do not set
`internal: true` on its existing `default` instead: Compose recreates that
network, and a service it does not recreate at the same time, usually the
database, comes back without its name, so the release fails and so does
bringing the previous one back. `up -d --wait --force-recreate` recovers it.
The old `<project>_default` network stays, unused, for a rollback, as
`<project>_private` does after one. Remove it with `docker network rm` once you
no longer need that.

A site on its own network takes two of Docker's networks. Docker's default
address pools hold about 30, so a server not set up by devopsy-server needs a
larger `default-address-pools` in `daemon.json` beyond about a dozen sites.

A change to the `proxy` network's own settings, such as a driver option,
needs the project down first (`devopsy @prod down`), so Traefik lets the
network go. Otherwise its release fails, since Compose cannot recreate a
network Traefik is on, and though the previous release comes back, its
container is left off the network. The site then does not answer until
`devopsy @prod up -d --force-recreate`.

While `attach` is down, sites already connected keep working. A new one
is not connected: a release succeeds, its visitors wait 30 seconds and get
a 504, and `devopsy domains` says `attach` is not running.

A project may still join the shared `traefik` network instead, as an
`external` network. Every container on it can reach every other one, on
any port, without going through Traefik, so never rely on Traefik's
middlewares (basic auth, IP allow lists) as a service's only protection
there.

This separates containers, not the people behind them: a deploy user in
the `docker` group controls the whole server.
Projects that do not trust each other belong on separate servers.

## Documentation

- [Running Traefik](docs/running.md): settings, upgrades, the dashboard,
  commands from your machine and customizing.
- [Certificates](docs/certificates.md): the resolvers, HSTS, acme-dns, a
  Cloudflare token, CNAME delegation, staging and production, and
  forgetting domains.
- [Domains](docs/domains.md): wildcard URLs and the `domains` report.
- [Real client IPs behind proxies and CDNs](docs/proxies.md).
- [Behind a Cloudflare Tunnel](docs/tunnel.md).
- [Stopped sites](docs/hold.md): holding visitors through a site's stops.

## License

GPL-3.0. See [LICENSE](LICENSE).
