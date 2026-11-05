History, not rules: `internals/tunnel.md` wins where they differ.

- Cloudflare Tunnel was decided on 2026-10-28, with the connectors' template (devopsy-template-cloudflared).
- A tunnel belongs to an account: a zone of another account cannot point at it, which gives error 1033, as when no connector is up.
- Default entrypoints became explicit because, with none marked, adding `tunnel` would have put every direct site behind any tunnel on the server.
- Without the tunnel's forwarded-headers trust Traefik set `X-Forwarded-Proto: http`, and Drupal built `http://` URLs or looped.
- Conditional `ports:` were rejected for tunnel-only servers: compose cannot leave an entry empty, hence loopback.
- Traefik joining every project's network left `:8081` open to any project's container, which could name another site's host and skip Cloudflare's WAF and Access, though not fake its IP or `https`. It was first documented as a limit, then closed with `tunnel-allow@file` before the networks work merged, at the operator's choice.
- The allow list is an entrypoint middleware rather than a firewall rule, because the template holds no host rules and an entrypoint middleware also covers the `hold-tunnel` catch-all, so a host with no router gets the same 403.
- It is always written, with only the loopback until the `tunnel` network exists, so a first start has no window with the entrypoint open.
- The allow list made a stale subnet matter: after a `down` another project could take the old subnet and pass, while the connectors got a 403 until the next `deploy`. `restart` read nothing until a review found it. An IPv6 subnet was left out of the list too, which would have turned a connector's request over IPv6 from untrusted into refused.
