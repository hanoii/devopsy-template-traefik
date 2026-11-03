# Certificates

How Traefik gets, keeps and forgets certificates, and what answers its DNS-01 challenges. Code: `commands/letsencrypt`, `commands/acmedns`, `commands/forget`, `commands/check`, `lib/retry`, and `check_settings`, `forget_apply` and `in_traefik` in `lib/common.sh`.

## Resolvers

- Resolvers are named by CA, ACME challenge and, for DNS-01, who answers it (`le-http01`, `le-dns01-selfhosted`, `le-dns01-cloudflare`), and a second CA adds names beside these: a rename costs every certificate, since each resolver's store is its own file (`acme-<resolver>.json`).
- The ACME settings carry the CA's prefix (for example `PROXY_LE_ACME_EMAIL`, `PROXY_LE_ACME_CASERVER`), since another CA brings its own email and EAB key.
- What runs acme-dns keeps its software's name: the `acmedns` profile, service and command, and `PROXY_ACMEDNS_*`.
- `le-http01` uses HTTP-01, never TLS-ALPN: HTTP-01 also works behind a CDN proxy, and Traefik's ACME challenge router comes before the HTTPS redirect.
- Every resolver is always defined, `le-dns01-cloudflare` without a token too, since Traefik starts without one and fails only a router that uses it.
- Cloudflare's token is `PROXY_CLOUDFLARE_DNS_API_TOKEN`, set in the server's `shared/.env` and masked through `secrets:` in `config.yaml`.
- `check_settings` refuses a resolver Traefik lacks, and a DNS-01 one (the default or the wildcard's) without what answers it (acme-dns's profile, Cloudflare's token): a router naming one gets no certificate, which Traefik only logs.
- `check_settings` runs as the release's prepare step (`check`), before the switch, so a refusal changes nothing live, and in `deploy` too, for a plain `devopsy deploy`.
- A site that leaves the resolver to the server has no `tls` label: a router without one gets its entrypoint's, `PROXY_CERTRESOLVER` (the `websecure` one when it names no entrypoint), while `tls.certresolver=` empty or `tls=true` gets no certificate at all.

## Requests and the stores

- Traefik requests or retries a certificate only when a router's configuration changes, so recreating an identical container retries nothing, and taking the site down and up does.
- A wildcard requested before its DNS existed fails until then, and `domains --retry` (`lib/retry`) asks again without a restart.
- A Traefik restart retries as well, and requests HTTP-01 certificates for routed hosts that had none, which Traefik then serves before the wildcard; both are valid.
- Any stored certificate covers a host, from any resolver, and a wildcard covers every host under it, so per-host HTTP-01 requests stop once the public wildcard exists.
- The wildcard router matches one reserved name only, so other hosts keep the usual 404 rather than the noop service's 418.
- Traefik reads its ACME files only at startup, writes its copy back on changes and renews every stored certificate, routed or not, so the stores are edited with Traefik stopped.
- lego loads acme-dns's registrations (`acme-dns-accounts.json`) once, so there is no command to register a domain ahead: it would restart Traefik per domain.
- Every ACME file stays mode 600 (`umask 077`, `cp -p`, writes through the file, `mv`): Traefik skips a resolver whose store has any group or other permission, logging an error while the container stays healthy, and requests none of its certificates.
- A store restored or copied with a wider mode is fixed with `chmod 600` in `in_traefik`.
- devopsy makes `mnt/letsencrypt` and `mnt/acmedns` (mode 755, which shows only the files' names) and gives them and everything in them to 10001 before every release's run step (`devopsy.mnt.owner=user` on `traefik` and `acmedns`), so files moved in by hand are fixed too.
- No `init` service: Traefik, lego and acme-dns create their files with mode 600 as they need them.
- `deploy` makes only the directories the host writes (`dynamic`, `proxies`), so they stay the deploy user's.
- A fresh named volume tests none of this: Docker copies the image's directory owners into an empty volume at each mount, unless it is `nocopy`.

## Forgetting

- `devopsy forget` only queues (`mnt/forget.pending`); `forget_apply`, called by `deploy`, `restart` and `forget --now` right before their `up`, stops Traefik for a few seconds, edits the stores and removes the registrations from acme-dns's database (`records` and `txt`, by subdomain).
- A forget's first pass skips the stop when nothing matches, and acme-dns keeps running, since it reads its database on every request.
- The stores are edited with no jq on the host or in Traefik's image: one Traefik container prints every store as one JSON object, the `jq` service turns it into tab-separated lines, and a second Traefik container writes them through the files.
- Private keys travel only in pipes and shell variables, never to the host's disk.
- acme-dns's database is edited by the `sqlite` service (Python's module from the official image, as 10001, no network), since acme-dns's image has no sqlite and its API cannot remove a registration.

## acme-dns and DNS

- acme-dns binds the public IP only (`PROXY_ACMEDNS_IP`, which `deploy` detects and saves into `.env`): systemd-resolved holds 127.0.0.53:53 on many cloud images, and Docker cannot publish 0.0.0.0:53 then.
- `deploy` saves what it detects (`PROXY_ACMEDNS_IP`, `PROXY_DOCKER_GID`) into `.env`, so a plain `devopsy up` later interpolates the same compose file.
- acme-dns's log level is `warn`, its word, never `warning`.
- A registration failing with "no such table: records" (Traefik reports it as EOF) means `mnt/acmedns` is not 10001's.
- On Cloudflare the acme-dns domain's nameserver is `ns-<acme-dns domain>`, an A record that is not proxied, since Cloudflare refuses NS and A records on one name.
- `le-dns01-cloudflare` checks propagation through `PROXY_DNS_RESOLVERS`, the host's first (`127.0.0.11`, Docker's embedded DNS, on every network Traefik joins), then 1.1.1.1, lego asking the next on an error or an empty answer, then the zone's authoritative nameservers directly on port 53.
- The authoritative nameservers are asked directly because lego looks the challenge name up (following CNAMEs) before writing the record, so a recursive resolver caches the NXDOMAIN until its negative TTL ends.
- A server whose firewall blocks outbound DNS skips every check (`PROXY_DNS_SKIP_CHECKS`) and waits `PROXY_DNS_PROPAGATION_DELAY` instead.
