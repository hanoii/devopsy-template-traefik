# AGENTS.md

devopsy-template-traefik is the Traefik reverse proxy for a devopsy server, run with [devopsy-cli](https://github.com/hanoii/devopsy-cli) and released onto each server like any devopsy project (`traefik/prod` under the server's release root). It owns everything about routing and certificates; devopsy-cli knows no proxy.

## Layout

- `.devopsy/` is all a release uploads: `config.yaml` (`project: traefik`, its one environment `prod`, the release steps), `compose.yaml` (Traefik, socket-proxy, acme-dns in the `acmedns` profile, `jq`, `curl` and `sqlite` in the `tools` profile, for scripts only) and `.env.example` (what a server sets in its `shared/.env`).
- `.devopsy/commands/` are POSIX `sh`, each with a `## Description:` line; `setup` is the only one run locally (`## Run: local`, `## Needs: target`), and `check` is a release's prepare step.
- `.devopsy/lib/common.sh` is sourced by the commands and lib's scripts; `lib/facts`, `lib/retry` and `lib/domains/` serve `domains`.
- `.devopsy/mnt/` (on servers `shared/mnt`, never uploaded) holds the ACME stores, acme-dns's database, Traefik's file provider (`mnt/dynamic/`), the proxies' list, `mnt/traefik.env` (the static settings) and `mnt/forget.pending`.
- `.devopsy/plugins-local/` holds the local Traefik plugins, `proxies` and `hold`, run by Yaegi: standard library only.
- `tests/` (with the root's `go.mod`) and `.github/workflows/ci.yml` are the checks.

## Rules

- The `traefik` network, the resolvers `le-http01`, `le-dns01-selfhosted` and `le-dns01-cloudflare`, the entrypoints `web`, `websecure` and `tunnel`, the `tunnel` network and `http://traefik.tunnel:8081`, and the labels `devopsy.role=proxy` and `devopsy.export.BASE_DOMAIN` keep their names: other projects name them in their own compose files, released on their own schedule.
- What a command writes at runtime goes in `mnt/` or `.env`: elsewhere in `.devopsy/` a file goes with the next release.
- `.env` is written through it (`env_set`, `cat >`), never `mv` over it: on servers it is a link to `shared/.env`.
- Containers run as 10001, never as the deploy user, who is in the docker group; scripts touch `mnt/letsencrypt` and `mnt/acmedns`, 10001's with files of mode 600, only through `in_traefik` (a one-off Traefik container as 10001).
- The host needs no `jq`, `curl` or `sqlite`: they are the `tools` profile's services, and Traefik's own `wget` does the rest.
- Traefik is configured with `TRAEFIK_*` variables in `compose.yaml`, never static config files, and what a server may change is a variable with a default in `.env.example`, set with `devopsy @<server>-traefik --vars`.
- The template's settings are `PROXY_*`, a name meaning one fact on both sides of the proxy role (for example `PROXY_BASE_DOMAIN`, exported as `BASE_DOMAIN`): `DEVOPSY_*` are devopsy-cli's own, and Traefik reads `TRAEFIK_*` as its static configuration.
- `PROXY_BASE_DOMAIN` is this server's setting and reaches a site through the export label, read by devopsy-cli from the running container at each of the site's releases (a target may override it there); `PROXY_CERTRESOLVER` is the default here and a site's own choice there, and `PROXY_DOMAINS` is a site's.
- The entrypoints' middleware lists have one writer, `static_env` (`lib/common.sh`, called by `deploy` and `proxies`), which composes `mnt/traefik.env` and restarts Traefik only when it changed: entrypoints are static configuration.
- A middleware file is written before the env names it and removed only once the running Traefik no longer does: an entrypoint naming a missing middleware breaks every router.
- Traefik never mounts the Docker socket, socket-proxy stays read-only and accepts only the `traefik` container, both run unprivileged, and the dashboard stays on localhost.
- Every service has a fixed non-root UID (10001; socket-proxy's 65534, in the socket's group), `read_only`, `cap_drop: [ALL]` and `no-new-privileges`, a capability added back only for a stated reason (none today): a tool as root, even without capabilities, reads files the deploy user cannot.
- A tool whose image only works as root is the one exception to the non-root UID, said here with why.
- A new service gets all four.
- An image built here has no setuid binaries or file capabilities it does not need.
- Images float on the series upstream patches (for example `traefik:v3.7`), never `latest` or a major tag; a minor is bumped by hand, in a commit, after reading every migration note in between (`https://doc.traefik.io/traefik/migrate/v3/`): a minor brings migration notes and a new Yaegi for the plugins.
- What publishes no minor tag is pinned exactly (socket-proxy, acme-dns, jq, curl), socket-proxy's release notes read on every upgrade: its allow-list flags are security settings.
- A pinned name changes with a bump, so a rollback across one goes back exactly, while a floating name goes forward on a rollback (devopsy-cli's README, "Choosing tags").
- `setup` keeps no state: it drives the other commands through its target and sets values with `--vars set` from its environment, never as arguments.
- No migration code: a breaking change to `shared/` is moved by hand on each server.

## Releases

- The template is released from a checkout or an alias whose `source:` names it (for example `devopsy @<server>:prod --release`); servers are the operator's, never in this repository, and a server's settings go in its `shared/.env` through `--vars`.
- `config.yaml`'s `defaults:` give `prod` `mode: image` (nothing to build), `check` as `--release`'s prepare step and `deploy` as its run step, and `deploy` for `--rollback` and `--reload`.
- A release that renames the `traefik` network fails while projects are attached, leaving Traefik stopped: compose cannot remove a network with endpoints.
- A rename disconnects the projects and removes the old network first, releases Traefik, then `docker network connect`s each project until its own release.

## Checks

```sh
cd .devopsy && docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:stable -s sh -x commands/* lib/facts lib/retry lib/common.sh
```

Plugins: `go test` in each plugin's directory, then the same tests under Traefik's interpreter, from `.devopsy/plugins-local`:

```sh
go install github.com/traefik/yaegi/cmd/yaegi@v0.16.1   # Traefik's version
GOPATH=$PWD GO111MODULE=off yaegi test github.com/hanoii/devopsy-traefik/proxies
GOPATH=$PWD GO111MODULE=off yaegi test github.com/hanoii/devopsy-traefik/hold
```

The tests (`tests/`, behind the `e2e` tag, since every one needs Docker):

```sh
go test -tags e2e -count=1 -v ./tests                      # under 2 minutes
go test -tags e2e -count=1 -v -run '^TestHoldNot$' ./tests # one test
DEVOPSY_BIN=/tmp/devopsy-linux go test -tags e2e ./tests   # another devopsy
go test -count=1 ./tests                                   # the agents' names alone
```

- A plugin passes under Yaegi as well as Go: Yaegi differs from Go in ways only `yaegi test` shows.
- The tests run the template on the real devopsy and a real Docker, never fakes: the template's contract with devopsy (its environment, its commands found by their `## Description:` line, `run --rm --no-deps -T` as compose runs it) is what breaks when devopsy changes, and a fake would pass there.
- CI (`ci.yml`) runs shellcheck, the plugins under Go and Yaegi, and the tests on every push.
- `TestAgentsNamesWhatExists` (`tests/agents_test.go`, no build tag, so no Docker) fails on a path, a shell word or a Go name this file or an area cites that exists nowhere in the repository.
- What needs a real ACME, DNS, Cloudflare or a public IP (certificates, acme-dns registrations, a real tunnel, `domains` against real DNS) is checked by hand on a throwaway machine (devopsy-cli's AGENTS.md, Checks), with Traefik on 18080 and 18443.

## Areas

Each area is read when its line says so; its history, `internals/<area>.history.md`, as Workflow's Docs says. The root's own sections have `internals/rules.history.md`, `internals/releases.history.md` and `internals/checks.history.md`.

- `internals/certificates.md`: the resolvers and their names, a router's `tls` label, when Traefik requests or retries a certificate, the ACME stores and `forget`, acme-dns, DNS-01 checks and `check_settings` (`commands/letsencrypt`, `acmedns`, `forget`, `check`, `lib/retry`, `check_settings` and `forget_apply` in `lib/common.sh`); read when a task touches a certificate, a resolver, acme-dns or `PROXY_DNS_*`.
- `internals/proxies.md`: the `proxies` plugin, the forwarded headers and `Forwarded`, Yaegi's limits, plugins loaded again (`plugins-local/.../proxies/`, `commands/proxies`, `mnt/proxies/`); read when a task touches a plugin, a proxy in front, the client IP or a forwarded header.
- `internals/network.md`: the `traefik` network and its aliases, the socket proxy, Traefik read-only, the API's access, `PROXY_READ_TIMEOUT`, an HTTP proxy on the host (`compose.yaml`, `api_access` in `lib/common.sh`); read when a task touches a network, the socket proxy, the API or an entrypoint's settings.
- `internals/domains.md`: the domains report, checked from the server (`commands/domains`, `lib/facts`, `lib/domains/`); read when a task touches `domains`.
- `internals/tunnel.md`: the tunnel end, its trust, default entrypoints and `PROXY_TUNNEL_ONLY` (`tunnel_subnet` and `static_env` in `lib/common.sh`, `commands/setup`); read when a task touches Cloudflare Tunnel or which entrypoints a router gets.
- `internals/hold.md`: holding visitors through a site's stop, the errors middleware, the catch-all, stopped and paused containers (`plugins-local/.../hold/`, `hold_middleware` in `lib/common.sh`); read when a task touches what a visitor gets while a site is down.
- `internals/tests.md`: the test hosts, lanes, script tests, scenarios and releases (`tests/`); read before writing or changing a test.
