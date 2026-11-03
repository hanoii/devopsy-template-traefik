# Certificates

Three resolvers, all Let's Encrypt, named by CA (`le`), ACME challenge
and, for DNS-01, who answers it. Every router uses this server's default,
`PROXY_CERTRESOLVER` in `.env` (`le-http01` when unset), unless it picks
one with `traefik.http.routers.<name>.tls.certresolver=<resolver>`. The
site templates leave it to the server unless their own
`PROXY_CERTRESOLVER` names one.

- **`le-http01`, HTTP-01.** Always available. Works for any domain that
  points at this server, with or without a CDN proxy like Cloudflare in
  front. Traefik answers the challenge on port 80 before the HTTPS redirect.
- **`le-dns01-selfhosted`, DNS-01 through this server's own acme-dns.** No DNS provider
  token: each domain needs one CNAME, created once by whoever manages its
  DNS. See below.
- **`le-dns01-cloudflare`, DNS-01 through Cloudflare's API.** Set a token as
  `PROXY_CLOUDFLARE_DNS_API_TOKEN` (`--vars set`, hidden prompt). See
  below.

Both DNS-01 resolvers work with a CDN proxy on, before DNS points at this
server, and for wildcard certificates.

## acme-dns

[acme-dns](https://github.com/joohoi/acme-dns) is a small DNS server that only
serves ACME challenge records. Each server runs its own, for its own
subdomain, and Traefik updates it through an API that only Traefik can reach.
This is how hosting panels like Forge do DNS validation.

1. Pick a subdomain of a domain you control, unique per server, and turn
   the service on in the server's `.env`:

   ```dotenv
   COMPOSE_PROFILES=acmedns
   PROXY_ACMEDNS_DOMAIN=acme-vm1.example.com
   ```

   acme-dns listens only on the server's public IPv4, which avoids clashing
   with systemd-resolved on 127.0.0.53:53. `deploy` detects it and saves it
   as `PROXY_ACMEDNS_IP`; set it yourself behind NAT.

2. Run `devopsy @vm1-traefik --release` (the profile's image comes with
   a release), then `devopsy @vm1-traefik acmedns`.
   It prints two records to create once in `example.com`: an A record for
   `ns-acme-vm1.example.com` and an NS record delegating
   `acme-vm1.example.com` to it.
3. Open port 53, UDP and TCP, in your provider's firewall.
4. Route a site with the `le-dns01-selfhosted` resolver. Its first attempt registers the
   domain and fails with a "CNAME required" error in Traefik's log. Run
   `devopsy @vm1-traefik acmedns` again: it lists the CNAME each domain
   needs, like

   ```
   _acme-challenge.client.org.  CNAME  5400f461-...acme-vm1.example.com.
   ```

   Once it exists, make Traefik try again. It only retries when a router
   changes, and recreating an identical container does not count:
   `devopsy @<server>-traefik domains <compose project> --retry` asks again
   without a restart ([Checking domains](domains.md#checking-domains)). Renewals need nothing more.

Each domain's credentials can only change its own challenge record, so
nothing here can touch real DNS records. acme-dns does not resolve other
names, so port 53 is not an open resolver.

The registrations live in `mnt/letsencrypt/acme-dns-accounts.json`. A site
moving to another server registers again there and needs its CNAME changed to
the new server's name. Do that before switching the site's DNS: the old
server's certificate stays valid meanwhile.

## Cloudflare API token

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

## Propagation checks

Before asking Let's Encrypt, Traefik checks the challenge TXT record: first
through recursive resolvers, `PROXY_DNS_RESOLVERS` (default the host's,
`127.0.0.11:53` through Docker, then 1.1.1.1 and 1.0.0.1; each is asked in
turn when one fails or has no answer yet), then at the zone's authoritative
nameservers on port 53.

Behind a firewall that blocks outbound DNS, skip the checks and wait
instead:

```sh
devopsy @vm1-traefik --vars set --show PROXY_DNS_SKIP_CHECKS PROXY_DNS_PROPAGATION_DELAY
# true, 60s
```

Checking through the host's resolver alone does not work: Traefik looks the
name up before writing the record, and the resolver caches that it does not
exist, up to the zone's negative TTL. Waiting is reliable: Cloudflare's API
writes the record within seconds, and Let's Encrypt asks the authoritative
nameservers itself.

## DNS-01 for any domain: CNAME delegation

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
3. Route the site with the `le-dns01-cloudflare` resolver. Traefik follows the CNAME,
   writes the TXT record at `client.org.example-acme.com` and removes it once
   validated. Renewals need nothing more.

The token then only needs the challenge zone, however many domains delegate
to it. The zones the sites live in stay out of its reach, even when they are
on Cloudflare too, which makes this the safest setup and not only the one for
outside domains.

Keep the CNAME as long as the site uses the `le-dns01-cloudflare` resolver. Removing it
breaks the next renewal, about 30 days before expiry.

## Staging and production

Without a CA server in `.env`, certificates come from Let's Encrypt
**staging**. Switch every resolver with:

```sh
devopsy @vm1-traefik letsencrypt production   # or staging; no argument shows the current one
```

It sets the CA server in `.env`, moves the old environment's accounts and
certificates aside so they are not served until renewal, keeps the acme-dns
registrations, and restarts Traefik.

## Forgetting domains

Traefik keeps every certificate it was ever given and renews all of them,
routed or not. A domain that is gone (a test site, a server's old wildcard
domain) fails its renewals from 30 days before expiry, with errors in the
log, and its acme-dns registration stays in `devopsy acmedns`.

```sh
devopsy @vm1-traefik forget                         # what is stored, and queued
devopsy @vm1-traefik forget old.example.com         # queue it
devopsy @vm1-traefik forget --now old.example.com   # now: Traefik restarts
devopsy @vm1-traefik forget --cancel                # empty the queue
```

A domain stands for itself and its wildcard: `old.example.com` also forgets
`*.old.example.com`, never `site.old.example.com`. A certificate goes when
any of its names matches, from every resolver's store, with the acme-dns
registration of the domain: in Traefik's file and in acme-dns's own
database. Delete its `_acme-challenge` CNAME afterwards.

Traefik reads its stores only when it starts, so they are edited while it
is stopped. Queued domains wait for the next `deploy` (a release) or
`restart`, which stops Traefik for them: sites are unavailable for a few
seconds. `--now` does that at once, for everything queued. Nothing stops
when nothing is stored for the queued domains. Each edited store keeps a copy next to it
(`mnt/letsencrypt/<file>.forget-<time>`).

Forget a domain once nothing routes it: one a router still names gets a
new certificate as Traefik starts, and through `le-dns01-selfhosted` a new
registration, with a new CNAME.
