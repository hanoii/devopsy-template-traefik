# Behind a Cloudflare Tunnel

A site behind Cloudflare's proxy still answers anyone who reaches the
server directly. Its IP can be found, and Certificate Transparency logs
list every name it has a certificate for, so Cloudflare's WAF does not
protect it from a scanner. With a [Cloudflare
Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/),
the server publishes only SSH. `cloudflared` connects out to Cloudflare,
and visitors' requests come back through that connection.

```
visitor -> Cloudflare's edge (TLS, WAF, Access) -> tunnel -> cloudflared
        -> http://traefik.tunnel:8081 (the tunnel entrypoint) -> the site
```

The connectors are a template of their own,
[devopsy-template-cloudflared](https://github.com/hanoii/devopsy-template-cloudflared),
released after this one. Traefik's side is always part of this template:

- the `tunnel` entrypoint (`:8081`, plain HTTP, unpublished) and the
  `tunnel` network, which only Traefik and the connectors join;
- trust of that network's subnet, which every `deploy` reads from Docker.
  For requests from that subnet, Traefik takes the visitor's IP from
  `CF-Connecting-IP` (through the proxies plugin, built in: `devopsy
  proxies` lists it) and keeps the connector's `X-Forwarded-Proto:
  https`, so applications build `https://` URLs. Requests to port 8081
  from anywhere else, such as another project on the `traefik` network,
  are not trusted.

Since Traefik trusts the network's subnet rather than each connector,
tunnels are added and removed by releases of their own, without
restarting Traefik. The first release with the tunnel entrypoint recreates
Traefik twice, once for the entrypoint and once for its trust, which is
read from the network the first start created.

## Setting one up

`setup`'s fifth answer, "Through a Cloudflare Tunnel", sets everything,
then prints the Cloudflare steps with the values filled in. It sets:

- `PROXY_TUNNEL_ONLY=true`, so routers that name no entrypoint attach only
  to the tunnel entrypoint;
- `PROXY_HTTP_PORT=127.0.0.1:80` and `PROXY_HTTPS_PORT=127.0.0.1:443`, so
  nothing answers on the server's public address;
- `PROXY_WILDCARD_CERTRESOLVER=none`, since TLS ends at Cloudflare's edge
  and the server needs no certificate;
- `PROXY_BASE_DOMAIN`, best a zone's apex (`example.com`, for URLs like
  `whoami-prod.example.com`), because Cloudflare's free edge certificate
  covers only one level below the zone ([Wildcard URLs one level
  deeper](#wildcard-urls-one-level-deeper)).

In the Cloudflare account that holds the zone:

1. In Networking > Tunnels, create a tunnel (cloudflared) and copy its
   token. The dashboard waits for a connector before it goes on, so
   release devopsy-template-cloudflared at this point, from its checkout:

   ```sh
   devopsy @vm1:own/prod --vars set PROXY_TUNNEL_TOKEN
   devopsy @vm1:own/prod --release
   ```

2. Add a published application route from `*.example.com` to
   `http://traefik.tunnel:8081`, type HTTP. A site's own domain gets a
   route of its own.
3. The route creates the DNS record, `*.example.com` as a proxied CNAME
   to `<tunnel id>.cfargotunnel.com`, but only when the name has no record
   yet. Remove any older record (an A record to the server) first.
4. In SSL/TLS, turn on Always Use HTTPS, since requests reach Traefik as
   plain HTTP and skip its redirect. The encryption mode does not apply to
   the tunnel's hostnames. Leave it as it is, Full (strict) for any record
   of the zone that reaches a server directly.
5. At your provider's firewall, allow inbound SSH only, and outbound port
   7844, UDP (QUIC) or TCP.

Then check with `devopsy @vm1-traefik domains`.

## A tunnel per Cloudflare account

A tunnel belongs to an account, not to a zone or a server. Any zone in
that account can point a proxied CNAME at it. A zone in another account
cannot, and gets Cloudflare's error 1033, the same error as when no
connector is up. A server that hosts one client, whose account holds
every zone, needs one tunnel. Each further account is another instance of
the tunnel template (`@vm1:acme/prod`), with that account's token. All
instances end at the same entrypoint, so a site's single router answers
its names whichever tunnel they came through.

To move a name to another tunnel, remove its record first and then add
the route, since a route creates a record only for a name that has none
(step 3 above). Or point the record at `<new tunnel
id>.cfargotunnel.com`.

The easiest arrangement is a server that belongs to the client, on the
client's account and zones, with you as a member they can remove. On a shared
server of your own, there are two ways to serve a client's domain:

- Cloudflare for SaaS. The client's name becomes a custom hostname in
  your zone. The client points it at you with a CNAME and proves ownership
  with a TXT record. Each zone includes some custom hostnames, and more
  cost a small fee each (Cloudflare's pricing). An apex domain needs the
  client's DNS on Cloudflare, or `www`. The edge and the WAF rules are
  yours, and if you leave, the client changes one record.
- A tunnel instance from the client's account. The token, the edge, the
  WAF and the DNS are the client's, and you run only the connector.

## Wildcard URLs one level deeper

The free edge certificate covers `*.example.com`; `*.vm1.example.com` is
one level too deep. For a second server's wildcard URLs in the same zone,
the options are:

- Advanced Certificate Manager on the zone, for a monthly fee per zone
  whatever the number of servers, with one certificate that lists each
  server's wildcard;
- a zone per server, with its wildcard URLs at that zone's apex;
- no wildcard URLs on that server (`PROXY_BASE_DOMAIN` empty), with its
  sites only on their own domains, which are one level down and always
  covered.

The limit comes from Cloudflare's proxy: a proxied two-level wildcard
fails at the edge without a tunnel too.

## Tunneled and direct sites on one server

Without `PROXY_TUNNEL_ONLY`, routers that name no entrypoint stay on `web`
and `websecure`, so a tunnel opens only the sites that ask for it. A site
asks with `PROXY_ENTRYPOINTS=tunnel` in the site templates, and with
`PROXY_CERTRESOLVER` unset, since a router with a resolver is a TLS router
and never matches the tunnel's plain HTTP. Such a site is not served on
ports 80 and 443, even with a matching Host header and SNI. Port 80 only
redirects to HTTPS, and 443 answers 404.

## What `domains` reports

For a host whose routers are only on the tunnel entrypoint, `domains`
checks through Cloudflare and looks for no certificate on the server. It
shows the host's DNS (proxied), the request through the tunnel with its
status, and Cloudflare's error code when there is one. A 1033 means that
no connector of the record's tunnel is up, or, when this server's
connectors are up, that the record names another tunnel or one in another
account.

A separate section lists the connectors on the `tunnel` network with
their health; a connector is healthy once it is connected to Cloudflare.
Tunneled hosts are left out of the `devopsy --probe` line, since there is
no port to probe.

## What the edge covers

This is what a tunnel and Cloudflare's Free plan give, and what stays
yours. The host's half (firewall, SSH, upgrades, backups) is in
[devopsy-server's "Things to know"](https://github.com/hanoii/devopsy-server/blob/main/docs/things-to-know.md).

Covered, on the Free plan:

- The server's address answers only SSH, so there is no port to scan, and
  requests reach a site only through Cloudflare, its WAF and its rules.
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
