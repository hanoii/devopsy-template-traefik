# Network

The `traefik` network, the socket proxy, Traefik's own container and its API. Code: `compose.yaml`, `api_access` in `lib/common.sh`.

- One Traefik runs per host (the `proxy` role refuses a second), so its network's name is fixed, `traefik` (AGENTS.md, Releases, for a rename).
- Every routed container shares `traefik`, so a compromised one reaches the others' ports past Traefik's middlewares; accepted while projects on a server trust each other, none relies on a middleware for access control, and only a project's web service joins it.
- The socket proxy and acme-dns have networks of their own, and Traefik's API answers 403 on `traefik`: a routed container reaches none of them.
- On `traefik` a service alias resolves to any project's container with that alias, so a service there that proxies to a sibling uses the network-scoped name (for example `app.<project>_default`).
- The socket proxy allows only what Traefik's Docker provider calls (version, list, inspect, events), never all of `containers/`, which also lets through `export`, `archive`, `logs` and `attach` of every container.
- Accepted: inspect still shows every container's environment, which Traefik needs for labels.
- Traefik runs read-only with no capabilities: Docker lets a container bind 80 and 443 without `NET_BIND_SERVICE`, and Traefik writes only to `/letsencrypt` (local plugins need no plugins storage).
- The API and the dashboard (the `traefik` entrypoint, `:8080`, on every network Traefik joins) are allowed from localhost and the gateways of Traefik's networks only: `api_access`, after every `up` in `deploy` and `restart`, writes a router whose IP allow list has them, read from the running container.
- `api_access` drops a gateway that is no IP (Docker 29 prints a missing one as `invalid IP`), since one bad range makes the router invalid.
- `PROXY_READ_TIMEOUT` is the `websecure` entrypoint's read timeout, how long a request, body included, may take to arrive: entrypoints are static, so a server where a registry runs raises it, and projects cannot.
- Behind an HTTP proxy, calls to a container's own addresses pass `-Y off`: Docker gives containers the client's proxy settings, and busybox's `wget` (Traefik's, acme-dns's) ignores no_proxy; fetches from the internet keep the proxy.
