# devopsy-traefik

The Traefik setup for a devopsy server: one per Docker host, routing every
project on it, with automatic Let's Encrypt certificates. It is run with
[devopsy-cli](https://github.com/hanoii/devopsy-cli).

- Traefik v3 with the Docker provider. Containers are only routed if they opt
  in with `traefik.enable=true`.
- Traefik never mounts the Docker socket. It reads a filtered, read-only API
  through [socket-proxy](https://github.com/wollomatic/socket-proxy).
- HTTP redirects to HTTPS. Certificates use the HTTP-01 challenge by default,
  with an optional DNS-01 resolver through Cloudflare.
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
- **`cloudflare`, DNS-01.** Only when `.devopsy/dns.env` exists: copy
  `dns.env.example` and set a Cloudflare API token.

Both DNS-01 resolvers work with a CDN proxy on, before DNS points at this
server, and for wildcard certificates.

### acme-dns

[acme-dns](https://github.com/joohoi/acme-dns) is a small DNS server that only
serves ACME challenge records. Each server runs its own, for its own
subdomain, and Traefik updates it through an API that only Traefik can reach.
This is how hosting panels like Forge do DNS validation.

1. In `.devopsy/.env`, pick a subdomain of a domain you control, unique per
   server, and turn the service on:

   ```dotenv
   COMPOSE_PROFILES=acmedns
   DEVOPSY_ACMEDNS_DOMAIN=acme-vm1.example.com
   ```

2. Run `devopsy up -d`, then `devopsy acmedns`. It prints two records to
   create once in `example.com`: an A record for `ns-acme-vm1.example.com`
   and an NS record delegating `acme-vm1.example.com` to it.
3. Open port 53, UDP and TCP, in your provider's firewall.
4. Route a site with the `acmedns` resolver. Its first attempt registers the
   domain and fails with a "CNAME required" error in Traefik's log. Run
   `devopsy acmedns` again: it lists the CNAME each domain needs, like

   ```
   _acme-challenge.client.org.  CNAME  5400f461-...acme-vm1.example.com.
   ```

   Once it exists, the next retry gets the certificate, and renewals need
   nothing more.

Each domain's credentials can only change its own challenge record, so
nothing here can touch real DNS records. acme-dns does not resolve other
names, so port 53 is not an open resolver.

The registrations live in `mnt/letsencrypt/acme-dns-accounts.json`. A site
moving to another server registers again there and needs its CNAME changed to
the new server's name. Do that before switching the site's DNS: the old
server's certificate stays valid meanwhile.

`devopsy acmedns` needs `jq` to list the CNAMEs.

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

## Dashboard

It is published on `127.0.0.1:8080`. From your machine:

```sh
ssh -L 8080:127.0.0.1:8080 your-server
```

Then open <http://localhost:8080>.

## Customizing

Put changes in `.devopsy/compose.override.yaml`, which is not committed.
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
