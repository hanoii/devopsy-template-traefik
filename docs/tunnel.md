# Behind a Cloudflare Tunnel

A site behind Cloudflare's proxy still answers anyone who reaches the
server directly. Its IP is no secret, and Certificate Transparency logs
list every name it has a certificate for, so Cloudflare's WAF protects
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
- trust of that network's subnet, which every `deploy` reads from Docker.
  From that subnet, Traefik takes the visitor's IP from `CF-Connecting-IP`
  (through the proxies plugin, built in: `devopsy proxies` lists it) and
  sets `X-Forwarded-Proto: https`, so applications build `https://` URLs.
  A request to port 8081 from anywhere else (another project on the
  `traefik` network) is not trusted.

So tunnels come and go as releases of their own, with no restart of
Traefik. The first release with the tunnel entrypoint recreates Traefik
twice: once for the entrypoint, and once for its trust, read from the
network that first start created.

## Setting one up

`setup`'s fifth answer, "Through a Cloudflare Tunnel", sets everything and
then prints the Cloudflare steps with the values filled in. It sets:

- `PROXY_TUNNEL_ONLY=true`, so routers that name no entrypoint attach only
  to the tunnel's, never to `web` and `websecure`;
- `PROXY_HTTP_PORT=127.0.0.1:80` and `PROXY_HTTPS_PORT=127.0.0.1:443`, so
  nothing answers on the server's public address;
- `PROXY_WILDCARD_CERTRESOLVER=none`, so there is no certificate on the
  server, and TLS ends at Cloudflare's edge;
- `PROXY_BASE_DOMAIN`, ideally a zone's apex (`example.com`, for URLs like
  `whoami-prod.example.com`), since Cloudflare's free edge certificate
  covers only one level below the zone.

In the Cloudflare account that holds the zone:

1. In Networking > Tunnels, create a tunnel (cloudflared) and copy its
   token. The dashboard waits for a connector before it goes on, so
   release devopsy-template-cloudflared then, from its checkout:

   ```sh
   devopsy @vm1:own/prod --vars set PROXY_TUNNEL_TOKEN
   devopsy @vm1:own/prod --release
   ```

2. Add a published application route: `*.example.com` to
   `http://traefik.tunnel:8081`, type HTTP. A site's own domain gets a
   route of its own.
3. In DNS, `*.example.com` is a proxied CNAME to `<tunnel
   id>.cfargotunnel.com`. The dashboard creates it with the route, but
   only when the name has no record, so first remove any record from
   before (an A record to the server).
4. In SSL/TLS, turn on Always Use HTTPS, since requests reach Traefik as
   plain HTTP, past its redirect. The encryption mode does not apply to
   the tunnel's hostnames: leave it as it is, Full (strict) for any record
   of the zone that reaches a server directly.
5. At your provider's firewall, allow inbound SSH only, and outbound port
   7844, UDP (QUIC) or TCP.

Then check with `devopsy @vm1-traefik domains`.

## A tunnel per Cloudflare account

A tunnel belongs to an account, not to a zone or a server. Any zone of
that account can point a proxied CNAME at it, and a zone of another
account cannot: that gives Cloudflare's error 1033, as when no connector
is up. A server serving one client whose account holds every zone needs
one tunnel. Each further account is another instance of the tunnel
template (`@vm1:acme/prod`), with that account's token. Every instance
ends at the same entrypoint, so a site's one router answers its names
whatever tunnel they came through.

A route creates its DNS record only when the name has none. To move a name
to another tunnel, remove its record first, then add the route, or point
the record at `<new tunnel id>.cfargotunnel.com`.

The simplest shape is the client's own: their account, their zones, their
server, and your membership in their account, which they can revoke. On a
shared server of your own, there are two ways to serve a client's domain:

- Cloudflare for SaaS. The client's name is a custom hostname in your
  zone, which the client points at with a CNAME and proves with a TXT
  record. Some custom hostnames are included per zone, then each costs a
  small fee (Cloudflare's pricing). An apex domain needs the client's DNS
  on Cloudflare, or `www`. The edge and the WAF rules are yours, and if
  you leave, the client points one record elsewhere.
- A tunnel instance from the client's account. The token, the edge, the
  WAF and the DNS are the client's; you run only the connector.

## Wildcard URLs one level deeper

The free edge certificate covers `*.example.com`, not `*.vm1.example.com`.
For a second server's wildcard URLs in the same zone, there is:

- Advanced Certificate Manager on the zone (a monthly fee per zone, not
  per server), for one certificate listing each server's wildcard;
- a zone per server, with its wildcard URLs at that zone's apex;
- no wildcard URLs on that server (`PROXY_BASE_DOMAIN` empty), with sites
  only on their own domains, one level down and always covered.

This comes from Cloudflare's proxy, not the tunnel: a proxied two-level
wildcard fails at the edge without a tunnel too.

## Tunneled and direct sites on one server

Without `PROXY_TUNNEL_ONLY`, routers that name no entrypoint stay on `web`
and `websecure`, so a tunnel opens no site that does not ask for it. A
site goes through the tunnel only by naming it, with
`PROXY_ENTRYPOINTS=tunnel` in the site templates and `PROXY_CERTRESOLVER`
unset: a router with a resolver is a TLS router, which never matches the
tunnel's plain HTTP. Such a site is not served on ports 80 and 443, even
with its Host header and SNI: port 80 only redirects to HTTPS, and 443
answers 404.

## What `domains` reports

A host whose routers are only on the tunnel entrypoint is checked through
Cloudflare, never for a certificate here. The report shows its DNS
(proxied), the request through the tunnel and its status, and Cloudflare's
error code when there is one. A 1033 means no connector of the record's
tunnel is up or, when this server's connectors are up, a record naming
another tunnel or one of another account.

A section lists the connectors on the `tunnel` network, each with its
health: healthy once connected to Cloudflare. Tunneled hosts are left out
of the `devopsy --probe` line, since there is no port to probe.

## What the edge covers

This is what a tunnel and Cloudflare's Free plan give, and what stays
yours. The host's half (firewall, SSH, upgrades, backups) is in
[devopsy-server's "Things to know"](https://github.com/hanoii/devopsy-server/blob/main/docs/things-to-know.md).

Covered, on the Free plan:

- No port to scan: the server's address answers SSH only, so requests
  reach a site only through Cloudflare, its WAF and its rules.
- The Free Managed Ruleset (Security > WAF), which Cloudflare updates for
  widespread flaws, often before a patched image exists.
- One rate limiting rule, Bot Fight Mode, and Always Use HTTPS.
- Cloudflare Access for up to 50 users, to put children (`pr-*`, staging)
  or an admin path behind a login, at the edge.
- Tunnel health alerts (Notifications), by email or webhook.

Not covered:

- The application's own bugs, and its updates, which a release brings
  ([Choosing tags](https://github.com/hanoii/devopsy-cli/blob/main/docs/images.md#choosing-tags)).
- The Cloudflare account itself: two-factor authentication, and who is a
  member. The tunnel's token runs that tunnel only, and can be refreshed
  in the dashboard.
- Requests Cloudflare lets through, since the WAF blocks known patterns,
  not every exploit.

The Free and Pro plans have limits, through a tunnel as through the
proxy: a request body of at most 100 MB (a larger upload, or a registry's
layer push, fails with 413), and an answer within 100 seconds (else 524).
