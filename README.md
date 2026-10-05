# devopsy-traefik

The Traefik setup for a devopsy server: one per Docker host, routing every
project on it, with automatic Let's Encrypt certificates. It is run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli).

- Traefik v3 with the Docker provider. Containers are only routed if they opt
  in with `traefik.enable=true`.
- Traefik never mounts the Docker socket. It reads a filtered, read-only API
  through [socket-proxy](https://github.com/wollomatic/socket-proxy).
- HTTP redirects to HTTPS. Certificates use the TLS-ALPN-01 challenge.
- Headers that alias others (`X_Forwarded_For` for `X-Forwarded-For`) are
  dropped, so PHP backends cannot be fooled by them.
- Traefik runs as an unprivileged user. The dashboard listens on localhost
  only.

## Setup

```sh
git clone https://github.com/hanoii/devopsy-traefik.git /srv/traefik
cd /srv/traefik
cp .devopsy/.env.example .devopsy/.env   # then edit it
devopsy up -d
```

Required in `.env`:

- `TRAEFIK_CERTIFICATESRESOLVERS_LETSENCRYPT1_ACME_EMAIL`
- `DEVOPSY_UID` and `DEVOPSY_GID`: the owner of `.devopsy/mnt/letsencrypt`.
- `DEVOPSY_DOCKER_GID`: the group of `/var/run/docker.sock`
  (`stat -c %g /var/run/docker.sock`).

Certificates come from Let's Encrypt **staging** until you set
`TRAEFIK_CERTIFICATESRESOLVERS_LETSENCRYPT1_ACME_CASERVER` to
`https://acme-v02.api.letsencrypt.org/directory`. Check that routing works
before switching, as production has strict rate limits.

`devopsy restart` pulls newer images and recreates what changed.

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

## Dashboard

It is published on `127.0.0.1:8080`. From your machine:

```sh
ssh -L 8080:127.0.0.1:8080 your-server
```

Then open <http://localhost:8080>.

## Customizing

Put changes in `.devopsy/compose.override.yaml`, which is not committed.
Traefik reads its configuration from `TRAEFIK_*` environment variables, so
most changes are extra variables. For example, a Cloudflare DNS challenge
resolver lets a new server get certificates before DNS points at it:

```yaml
services:
  traefik:
    environment:
      - TRAEFIK_CERTIFICATESRESOLVERS_CLOUDFLARE=true
      - TRAEFIK_CERTIFICATESRESOLVERS_CLOUDFLARE_ACME_EMAIL=you@example.com
      - TRAEFIK_CERTIFICATESRESOLVERS_CLOUDFLARE_ACME_STORAGE=/letsencrypt/acme-cloudflare.json
      - TRAEFIK_CERTIFICATESRESOLVERS_CLOUDFLARE_ACME_DNSCHALLENGE_PROVIDER=cloudflare
      - CF_DNS_API_TOKEN=${CF_DNS_API_TOKEN}
```

Projects then pick it with
`traefik.http.routers.myapp.tls.certresolver=cloudflare`.

`DEVOPSY_ENVIRONMENT` (default `main`) changes the project and network name,
to run a second Traefik on the same host. Change the ports along with it.

## License

GPL-3.0. See [LICENSE](LICENSE).
