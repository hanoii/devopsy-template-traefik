# Real client IPs behind proxies and CDNs

Behind a proxy (Cloudflare, Fastly, a load balancer, another nginx),
Traefik sees the proxy's IPs, so applications would log and rate-limit
those instead of their visitors. The bundled local plugin (`plugins-local/`,
standard library only, nothing downloaded) fixes that for any number of
proxies, each with its ranges and the header it passes the visitor's IP
in:

```sh
devopsy proxies add cloudflare                        # preset: its published ranges, CF-Connecting-IP
devopsy proxies add fastly Fastly-Client-IP https://example.com/fastly-ranges.txt
devopsy proxies add lb X-Forwarded-For 10.0.0.0/16    # your own load balancer
devopsy proxies                                       # list
devopsy proxies refresh                               # fetch URL ranges again (no restart)
devopsy proxies remove lb
```

Ranges are CIDRs, or `https` URLs listing one per line.

For a request from a proxy's ranges, the visitor's IP becomes the client
IP; for `X-Forwarded-For`, that is the rightmost address that is not a
trusted proxy. Traefik sends it as `X-Forwarded-For` and `X-Real-Ip`, so
applications keep trusting only Traefik. The other forwarded headers are
what Traefik makes for a direct visitor, whatever the proxy or its visitor
sent: `X-Forwarded-Host` and `-Port` come from the request's `Host`, and
the rest are removed (`Forwarded` too, as from anyone). Only
`X-Forwarded-For` and `-Proto` come from the proxy.

Any other request loses the proxies' headers, so they cannot be spoofed
by reaching the server directly. Proxied and direct sites work on the same
server, since each request is judged by who connected.

The proxies are kept in `mnt/proxies/proxies.conf`. Traefik restarts when
the first proxy is added, when the last one is removed, or when the ranges
of an `X-Forwarded-For` or `X-Real-Ip` proxy change, since those proxies
are also trusted for Traefik's forwarded headers. No other change of
proxies restarts it. Those are static settings, which `deploy` and
`devopsy proxies` write together in `mnt/traefik.env`.

`deploy`, which every release runs, fetches the URL ranges again.
Cloudflare's ranges change rarely. To refresh them in between, schedule `devopsy
@vm1-traefik proxies refresh` from CI or locally, or add a cron to the
deploy user's crontab on the server; the release lock keeps it out of a
release. The cron below assumes the release root is the deploy user's
home:

```cron
17 4 * * 1  cd ~/traefik/prod/current && flock ~/traefik/prod/.lock devopsy proxies refresh
```

Load balancers that pass TCP connections with the PROXY protocol (HAProxy,
most cloud network load balancers) need no plugin, since Traefik supports
the protocol natively. Set
`TRAEFIK_ENTRYPOINTS_WEBSECURE_PROXYPROTOCOL_TRUSTEDIPS` in a compose
override, for example.
