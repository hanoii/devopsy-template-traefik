# Tunnel

Traefik's end of Cloudflare Tunnel; the connectors are devopsy-template-cloudflared's. Code: `tunnel_subnet` and `static_env` in `lib/common.sh`, `commands/setup`, `compose.yaml`.

- The connectors are a template of their own, with an instance per Cloudflare account (for example `@vm1:own/prod`), never services here: a tunnel belongs to an account, and compose cannot make a service per token.
- The tunnel end is static and always there, the `tunnel` entrypoint (`:8081`, plain HTTP, never published) and the `tunnel` network (fixed name, only Traefik and the connectors on it), so a tunnel added or removed later is a release of its own, with no Traefik restart.
- `PROXY_TUNNEL_ONLY` is the only static change a tunnel can need later.
- The connectors never join the shared `traefik` network: trust is by the `tunnel` network's subnet, so a project on `traefik` that reaches `:8081` cannot claim a visitor's IP or `https`.
- The connectors address Traefik as `traefik.tunnel`, the network-scoped name (`internals/network.md`).
- `static_env` reads the `tunnel` network's subnet from Docker (`tunnel_subnet`) and writes it as the `tunnel` entrypoint's forwarded-headers trust, so `X-Forwarded-Proto: https` survives (without it Traefik sets `http`, and apps build `http://` URLs or loop).
- `static_env` writes the subnet into `mnt/dynamic/tunnel.yaml` too, the proxies plugin with one entry (`CF-Connecting-IP` from that subnet), as a middleware of its own (`tunnel@file`) that `devopsy proxies` never touches.
- A first `up` makes the `tunnel` network, so the `static_env` after it restarts Traefik once, and a network made again with another subnet is caught the same way.
- Default entrypoints are always explicit, `tunnel` alone with `PROXY_TUNNEL_ONLY=true`, else `web` and `websecure`: with none marked, Traefik attaches routers to every entrypoint, which would put every direct site behind any tunnel on the server.
- A site on a mixed server names `tunnel` itself (`PROXY_ENTRYPOINTS=tunnel` in the site templates), without a resolver, since a router with one is a TLS router, which never matches the tunnel's plain HTTP.
- `check_settings` refuses a `PROXY_TUNNEL_ONLY` other than `true`, `false` or unset, since a typo would leave every site on the open ports.
- A tunnel-only server keeps 80 and 443 published on loopback (`setup` sets `PROXY_HTTP_PORT=127.0.0.1:80` and `PROXY_HTTPS_PORT=127.0.0.1:443`), its provider's firewall closing them too, since compose cannot leave a `ports:` entry empty.
- On a tunnel-only server `setup` sets `PROXY_WILDCARD_CERTRESOLVER=none`, and nothing checks the wildcard's resolver: a certificate requested there is unused, not harmful.
- `setup` prints the Cloudflare side, with its values, and calls no API: the tunnel, its route, the DNS record and the zone's settings stay the operator's, in the account that owns them.
