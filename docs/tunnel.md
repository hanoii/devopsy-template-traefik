# Behind a Cloudflare Tunnel

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

## Setting one up

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
   The dashboard creates it with the route, only when the name has no
   record: remove one from before (an A record to the server) first.
4. SSL/TLS: Always Use HTTPS (requests reach Traefik as plain HTTP, past
   its redirect). The encryption mode does not apply to the tunnel's
   hostnames: leave it as it is, Full (strict) for any record of the
   zone that reaches a server directly.
5. At your provider's firewall, inbound SSH only; outbound port 7844, UDP
   (QUIC) or TCP.

Then check with `devopsy @vm1-traefik domains`.

## A tunnel per Cloudflare account

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

## Wildcard URLs one level deeper

The free edge certificate covers `*.example.com`, not `*.vm1.example.com`.
For a second server's wildcard URLs in the same zone:

- Advanced Certificate Manager on the zone (a monthly fee per zone, not
  per server): one certificate listing each server's wildcard;
- a zone per server, its wildcard URLs at that zone's apex;
- no wildcard URLs on that server (`PROXY_BASE_DOMAIN` empty): sites only
  on their own domains, one level down and always covered.

This is Cloudflare's proxy, not the tunnel: a proxied two-level wildcard
fails at the edge without a tunnel too.

## Tunneled and direct sites on one server

Without `PROXY_TUNNEL_ONLY`, routers that name no entrypoint stay on `web`
and `websecure`: a tunnel opens no site that does not ask. A site goes
through the tunnel only by naming it, `PROXY_ENTRYPOINTS=tunnel` in the
site templates (with `PROXY_CERTRESOLVER` unset: a router with a resolver
is a TLS router, which never matches the tunnel's plain HTTP). Such a site
is not served on ports 80 and 443, its Host header and SNI included: port
80 only redirects to HTTPS, and 443 answers 404.

## What `domains` reports

A host whose routers are on the tunnel entrypoint only is checked through
Cloudflare: DNS (proxied), the request through the tunnel and its status,
Cloudflare's error code when there is one (1033: no connector of the
record's tunnel, or, with this server's connectors up, a record naming
another tunnel or one of another account), never a certificate here. A section lists
the connectors on the `tunnel` network, each with its health: healthy once
connected to Cloudflare. Tunneled hosts are left out of the `devopsy
--probe` line: there is no port to probe.

## What the edge covers

What a tunnel and Cloudflare's Free plan give, and what stays yours. The
host's half (firewall, SSH, upgrades, backups) is in
[devopsy-server's "Things to know"](https://github.com/hanoii/devopsy-server/blob/main/docs/things-to-know.md).

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
  ([Choosing tags](https://github.com/hanoii/devopsy-cli/blob/main/docs/images.md#choosing-tags)).
- The Cloudflare account itself: two-factor authentication, and who is a
  member. The tunnel's token runs that tunnel only, and can be refreshed
  in the dashboard.
- Requests Cloudflare lets through: the WAF blocks known patterns, not
  every exploit.

Limits of the Free and Pro plans, through a tunnel as through the proxy:
a request body of 100 MB at most (a larger upload, or a registry's layer
push, fails with 413), and an answer within 100 seconds (else 524).
