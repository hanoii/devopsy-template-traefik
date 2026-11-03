# Domains

`devopsy domains`: for each host Traefik routes, its DNS, certificate and next step, checked from the server. Code: `commands/domains`, `lib/facts` (what Traefik knows about hosts, as JSON), `lib/domains/` (the report's jq filters, `report.jq` and `text.jq`, files so the tests run them alone).

- `domains` checks from the server: DNS over HTTPS to 1.1.1.1, TLS to the server's own public IP, and a request through what DNS returns, all in the `curl` service.
- For the outside view it prints the `devopsy --probe` line to run, since a server never drives the operator's devopsy.
- The public IP is `PROXY_PUBLIC_IP`, else 1.1.1.1's `/cdn-cgi/trace`, since behind NAT the default route's address is private.
- A certificate's validity comes from the verified handshake's verbose line.
- Only names that match a host name reach the check script, whose tab-separated lines jq turns into JSON: hosts come from every project's rules, and one stray quote breaks hand-built JSON for the whole server.
- Cloudflare's proxy is recognized by `cf-ray` on the HTTPS answer, else on plain HTTP: its free edge certificate covers one level below the zone only, so a proxied `*.prod1.example.com` fails the handshake at the edge while HTTP answers.
- A host whose routers are on `tunnel` only is tunneled (`method: tunnel` in `lib/facts`): no handshake with the server's IP, the request through Cloudflare with its error code read from the body (for example `error code: 1033`), and the connectors from `docker network inspect tunnel`, by their compose health.
- A 1033 means either no connector or a record in another account than the tunnel's, so the next step tells them apart by whether this server's connectors are healthy.
- A held site is checked with `X-Devopsy-Hold: no` (`internals/hold.md`), so a site down reads as down.
