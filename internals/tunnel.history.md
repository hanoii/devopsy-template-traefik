History, not rules: `internals/tunnel.md` wins where they differ.

- Cloudflare Tunnel was decided on 2026-10-28, with the connectors' template (devopsy-template-cloudflared).
- A tunnel belongs to an account: a zone of another account cannot point at it, which gives error 1033, as when no connector is up.
- Default entrypoints became explicit because, with none marked, adding `tunnel` would have put every direct site behind any tunnel on the server.
- Without the tunnel's forwarded-headers trust Traefik set `X-Forwarded-Proto: http`, and Drupal built `http://` URLs or looped.
- Conditional `ports:` were rejected for tunnel-only servers: compose cannot leave an entry empty, hence loopback.
