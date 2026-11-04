# Network

The `traefik` network, projects' own networks and `attach`, the socket proxies, Traefik's own container and its API. Code: `compose.yaml`, `lib/attach`, `api_access` in `lib/common.sh`.

- One Traefik runs per host (the `proxy` role refuses a second), so its network's name is fixed, `traefik` (AGENTS.md, Releases, for a rename).
- A template's routed services join `<project>_proxy` and name it in `traefik.docker.network`, never `traefik`: a compromised container on `traefik` reaches every other there past Traefik's middlewares.
- `traefik` stays for projects that have not moved, and `TRAEFIK_PROVIDERS_DOCKER_NETWORK` keeps naming it for them.
- A template's `default` is `internal: true`, so what is only on it has no way out, and its routed services go out through `<project>_proxy`.
- `attach` connects Traefik to a network only when a routed container is on it, of that container's compose project, never one of Traefik's own project.
- `attach` reads a label's value as JSON and refuses all but a plain name: a label is anyone's text, and could otherwise pass for the fields around it.
- `attach` removes a project's network once Traefik left and no container is on it, stopped ones included: `down` gives up on it while Traefik holds it, and a stopped container cannot start without it.
- `attach` leaves networks of no compose project alone, so one connected by hand stays.
- `attach` exits when its event stream ends, for the restart policy: as PID 1 it ignores a signal it has no handler for, and would go on healthy and deaf.
- `deploy` stays a plain `up`, and `attach` reconnects a recreated Traefik: connecting a created container first was slower at every size.
- `gw_priority` on Traefik's `default` is required: Docker otherwise moves the default gateway and the published ports to a network that sorts earlier, cutting requests in flight and changing the client address Traefik sees.
- `deploy`'s `PROXY_ATTACH_SUM`, in `attach`'s label, recreates it when `lib/attach` changes, since a bind mount's content is not in compose's hash.
- `netproxy` filters by URL, so what compromises `attach` can connect or cut any container and remove an unused network; only `attach` reaches it, and `dockerproxy` stays read-only.
- Each socket proxy and acme-dns have networks of their own, and Traefik's API answers 403 on `traefik` and on a project's network: a routed container reaches none of them.
- On `traefik` a service alias resolves to any project's container with that alias, so a service there that proxies to a sibling uses the network-scoped name (for example `app.<project>_default`).
- `dockerproxy` allows only what Traefik's Docker provider calls (version, list, inspect, events), never all of `containers/`, which also lets through `export`, `archive`, `logs` and `attach` of every container.
- Accepted: inspect still shows every container's environment, which Traefik needs for labels.
- Traefik runs read-only with no capabilities: Docker lets a container bind 80 and 443 without `NET_BIND_SERVICE`, and Traefik writes only to `/letsencrypt` (local plugins need no plugins storage).
- The API and the dashboard (the `traefik` entrypoint, `:8080`, on every network Traefik joins) are allowed from localhost and the gateways of Traefik's networks only: `api_access`, after every `up` in `deploy` and `restart`, writes a router whose IP allow list has them (localhost for `exec`, as `lib/facts` and the hold's probes use it, the gateways for the published port, through docker-proxy or NAT), read from the running container.
- `api_access` drops a gateway that is no IP (Docker 29 prints a missing one as `invalid IP`), since one bad range makes the router invalid.
- `PROXY_READ_TIMEOUT` is the read timeout of `websecure` and `tunnel`, how long a request, body included, may take to arrive: entrypoints are static, so a server where a registry runs raises it, and projects cannot.
- Behind an HTTP proxy, calls to a container's own addresses pass `-Y off`: Docker gives containers the client's proxy settings (`~/.docker/config.json`, `proxies`), and busybox's `wget` (Traefik's, acme-dns's) ignores no_proxy; fetches from the internet keep the proxy.
