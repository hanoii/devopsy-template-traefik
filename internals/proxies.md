# Proxies

The `proxies` plugin and the forwarded headers: how a request from a trusted proxy in front gets its visitor's IP, and what no visitor may set. Code: `plugins-local/.../proxies/`, `commands/proxies`, `static_env` in `lib/common.sh`. The Yaegi rules hold for every plugin, `hold` too.

## The plugin and the headers

- `devopsy proxies` keeps the proxies in `mnt/proxies/proxies.conf` and writes `mnt/dynamic/proxies.yaml` (the middleware and its ranges), which needs no restart.
- The plugin maps a request from a proxy's ranges to the visitor's IP from that proxy's header by changing the request's remote address, from which Traefik then builds X-Forwarded-For.
- Proxies that send X-Forwarded-For or X-Real-Ip get the entrypoints' forwarded-headers trust (`static_env`, from `mnt/proxies/forwarded-ips`), since Traefik strips those headers from an untrusted peer before middlewares run.
- For a request from a proxy only X-Forwarded-For and X-Forwarded-Proto are the proxy's, and the plugin sets the rest as Traefik does for an untrusted peer (X-Forwarded-Host from the Host, -Port by Traefik's rule, -Uri, -Method and -Prefix, the TLS client certificate's two and X-Real-Ip removed, X-Real-Ip then set from the proxy's header): an entrypoint trusting the proxy keeps every X-Forwarded-* it passes on, and Cloudflare passes its visitor's own, so a visitor would set the host an app builds links on.
- Accepted: a load balancer's own X-Forwarded-Host and X-Forwarded-Port are replaced too.
- A visitor's `Forwarded` is removed on every entrypoint that reaches apps, for every request (`forwarded@file`, a headers middleware `static_env` puts first on `websecure` and `tunnel`), since Traefik never sets or removes RFC 7239's header and an app trusting Traefik as its reverse proxy takes its host and proto from it.
- Accepted: a proxy in front that speaks only `Forwarded` loses it, which Traefik never read for the client IP anyway.
- `web` gets no `forwarded@file`: it only redirects (a project would need a router above the redirect's priority to reach an app there), and its internal routers (the redirect and ACME's) are built before the file provider loads, so a file middleware there would fail them at every start.
- A file middleware goes only on entrypoints without internal routers, as `proxies@file` and `tunnel@file` do.

## Loading plugins

- A plugin that fails to load stops Traefik (`TRAEFIK_EXPERIMENTAL_ABORTONPLUGINFAILURE=true`): skipped, the entrypoints' lists still name it and every router is dropped while the container looks healthy, whereas stopped, `up --wait` fails and the release rolls back.
- A changed plugin needs a new container, since Traefik loads plugins only when it starts: `deploy` keeps their checksum in `PROXY_PLUGINS_SUM`, the `devopsy.plugins` label, so a change recreates Traefik.

## Yaegi

- Plugins use the standard library only and pass `yaegi test` as well as `go test` (AGENTS.md, Checks), at Traefik's Yaegi version.
- A plugin never wraps the ResponseWriter: Yaegi hands a wrapper to Go code as ResponseWriter and Hijacker only, never Flusher, so a response through it goes out only as Go's buffer fills (websockets still work).
- An http-wasm plugin cannot call the next handler twice.
- A `for` statement's variable is per iteration under Yaegi, so an assignment to it in the body never reaches the next one (write `page := 1; for {`).
- A map index on the left of a multiple assignment stores nothing under Yaegi (`m[k], err = f()`), so assign through a plain variable.
- Goroutines running the same `select` at once deadlock in it under Yaegi, never woken by its `time.After`, so a request's waits are `time.Sleep` then `ctx.Err()`, and only one reader goroutine selects.
- To try a proxy locally, add one for Docker's gateway range (for example `192.168.0.0/16` on OrbStack) and send its header through a whoami.
