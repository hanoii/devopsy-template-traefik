# Running Traefik

## Settings

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

`devopsy restart` pulls newer images and recreates what changed.

## Upgrades

Upgrading is a release from a newer checkout; `devopsy @vm1-traefik
--rollback` goes back. Routed sites see a
few seconds without Traefik when its container is recreated.

Traefik floats on its minor (`traefik:v3.7`): every release pulls the
image again, so patches arrive with your next release. A new minor brings
[migration notes](https://doc.traefik.io/traefik/migrate/v3/) and a new
Yaegi for the plugins, so you move to it by editing the tag in
`.devopsy/compose.yaml`. socket-proxy, acme-dns and the tools (jq, curl)
publish no minor tag and stay exact pins.

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

With the alias from [Setup](../README.md#setup), the commands here (`proxies`,
`acmedns`, `letsencrypt`, `deploy`, logs) run from any directory:

```sh
devopsy @vm1-traefik proxies add cloudflare
devopsy @vm1-traefik acmedns
```

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
