# AGENTS.md

devopsy-template-traefik is the Traefik reverse proxy for a devopsy server, run with [devopsy-cli](https://github.com/hanoii/devopsy-cli) and released onto each server like any devopsy project (`traefik/prod` under the server's release root). It owns everything about routing and certificates; devopsy-cli knows no proxy.

## Workflow

### Git

- Conventional commits; the subject about 50 characters, 72 at most, the reason in the body: release notes are commit subjects, and tools cut longer ones.
- A commit made with an AI agent ends with one line naming it, `Assisted-by: <agent>:<model> [<effort>]` (for example `Assisted-by: claude-code:claude-opus-5-5 high`), never a session link (for example `Claude-Session`) or `Co-Authored-By`, even when a tool asks for one: GitHub shows a co-author as a person.
- Any word with an `@` in a commit message or a doc goes in backticks (for example `@prod`, `deploy@host`): GitHub links a bare `@name` to that user.
- Push and tag only when asked: a push publishes, a tag releases.
- A change of one commit (for example a quick fix) goes straight onto `main`, in the main checkout.
- Any larger task gets one branch, `<type>/<slug>`, its type what it brings to `main` whatever its commits' types (for example `feat/snapshots`, `docs/readme`).
- All of a task's work goes on its branch, reviews' failing tests and their fixes included; never a branch off a task's branch.
- A finished branch is rebased onto `main`, merged with `--no-ff` and deleted.
- A partial merge, only when asked, is rebased and merged the same way and keeps the branch and its worktree, rebased at once onto the merge for the rest of the task.
- A branch is worked in a worktree of its own, `.worktrees/<slug>` (`git worktree add .worktrees/<slug> -b <type>/<slug>`), removed once merged: inside the repository, a worktree is within the directories the agent may use.
- The main checkout stays on `main`.
- A worktree of a devopsy project run beside its main checkout gets its own `project:` (for example `whoami-<slug>`) in `.devopsy/config.local.yaml`: otherwise both are one local compose project, sharing containers and volumes.
- `.worktrees/` is listed in `.gitignore` and in the ignore file of any Docker build context that holds it: a `docker build` would otherwise send every worktree in its context.
- A session in a worktree starts by rebasing it onto `main`: the work adapts to what changed while it is still cheap.

### Tests and checks

- Every change carries its test, a fix's failing without it, a feature's checking what it promises, and a behavior a change touches with no test gets one then: a test is what keeps a fix fixed.
- A fix's test is run against the code before the fix, and the summary says it failed there.
- Unit tests first; an end-to-end test only for what needs a real system (for example Docker, sshd, the network, a terminal or timing).
- One test per behavior: extend the test that covers it before adding another, and never test one thing at both levels.
- The suites stay fast, locally and in CI, since every change waits on them: a new test runs in parallel with the others where it can, polls rather than sleeps, and reuses the setups that exist.
- A test made faster checks the same things, never fewer: a speed-up that drops a case or an assertion is a regression.
- A check or a refusal is only for a mistake any release can make, a setting a tool would otherwise take without a word; none is added for one upgrade only (for example a refusal of an old name after a rename): an upgrade's own steps cover those, and old names left over are ignored.
- Until 1.0.0, no tests of specific older versions and no handling of mixed versions (for example an operator and a server on different devopsy): a new behavior an older server lacks refuses before anything changes, and that is all.

### Refactors

- Before writing code for one case, look for the same pattern elsewhere (for example a message, a check, an interrupt) and propose one helper for all, each site adding only what is its own: variants drift apart, and a fix then lands in one of them only.
- A design that makes a change awkward is proposed for change, never worked around: workarounds pile up where the design is wrong.
- When a change makes the code simpler (less code, fewer external calls), the tests' fakes are extended to match, never the heavier code kept or a test weakened to pass.
- A refactor reaching beyond the task is agreed before it is made.

### Reviews

- A review round is two independent reviewers, one on Opus and one on Fable, on the task's branch in its worktree.
- A review brief lists what earlier rounds found and states Tests and checks' two limits, mixed versions and lean checks: findings of those kinds are not fixed.
- A review brief also asks for the same pattern written twice and for workarounds of the design (Refactors).
- A reviewer writes what it tests into the repository as `test:` commits on the task's branch, a failing test that proves a bug kept with a comment naming it, never product code: a harness outside the repository is lost with its round.
- While building or after a review, a bug, an edge case, a name or a verified finding is decided and fixed without asking, a fix with its failing test first, and said in the summary; only what changes the agreed design or user-facing behavior is asked.
- At most two rounds per branch: after the second round's fixes, the tests, the end-to-end suite and a hand test cover the rest.

### Servers and names

- Never touch a real server without asking, even read-only; test on throwaway machines and delete them afterwards: a read can still load a server, and only its owner knows what runs there.
- Names in examples, tests, docs and commit messages are generic (for example `app`, `shop`, `vm1`, `example.com`), never a real project, client, server, domain or person, nor anything from an agent's memory or another project: a public history keeps whatever a commit once said.

### Docs

- The root holds what every task in the repository needs; a part only some tasks need is an area, `internals/<area>.md`, with one line in the root's index saying what it covers and when to read it: the root loads with every session, the areas only when their line says so.
- A root is kept under about 250 lines, a guide, never a cap: past it, what only some tasks need moves to an area, and no needed rule is cut or squeezed to fit.
- A rule's history goes in `internals/<area>.history.md`, or `internals/<section>.history.md` for a rule in a section of the root, starting with a line saying it is history, not rules, and its area's file wins where they differ.
- A history file is read only before changing or questioning one of its rules, when a review finding looks like it contradicts a decision, or when asked why.
- How a script or a function works, step by step, goes in its comments, never in an AGENTS.md or area: a comment changes with its code.
- A commit's reason goes in its body; only a decision a later session must respect becomes a rule, in its area or the root, its story in the history, in the same commit: anywhere else, the next session rediscovers it or contradicts it.
- Before adding a rule, find any line it repeats or contradicts, in its file and the root, and change that line instead: two lines on one thing drift apart.
- A line written or changed in an AGENTS.md or area is checked before its commit (for example `Push and tag only when asked: a push publishes, a tag releases.`):
  - one rule, imperative, no bold, no prose around it;
  - a why only when something real breaks without it, saying what breaks, never an invented one, which an agent takes for a reason;
  - examples written "(for example ...)" before the colon, and one colon at most outside code;
  - one reading only, else reworded, never explained further;
  - no story (review rounds, what broke, measurements), which goes in the history.
- A README says at a glance what the repository is and how to start; anything longer is a doc of its own in `docs/` that it links to: a README that piles up docs gets skimmed past.
- READMEs and docs are written plainly, with no selling words, no bold lead-in on every bullet, no closing summary, no emoji and no "Note" asides: they read as generated and bury the facts.
- Each fact has one home, and other docs link to it rather than repeat it: two copies drift apart.

## Layout

- `.devopsy/` is all a release uploads: `config.yaml` (`project: traefik`, its one environment `prod`, the release steps), `compose.yaml` (Traefik, socket-proxy, acme-dns in the `acmedns` profile, `jq`, `curl` and `sqlite` in the `tools` profile, for scripts only) and `.env.example` (what a server sets in its `shared/.env`).
- `.devopsy/commands/` are POSIX `sh`, each with a `## Description:` line; `setup` is the only one run locally (`## Run: local`, `## Needs: target`), and `check` is a release's prepare step.
- `.devopsy/lib/common.sh` is sourced by the commands and lib's scripts; `lib/facts`, `lib/retry` and `lib/domains/` serve `domains`.
- `.devopsy/mnt/` (on servers `shared/mnt`, never uploaded) holds the ACME stores, acme-dns's database, Traefik's file provider (`mnt/dynamic/`), the proxies' list, `mnt/traefik.env` (the static settings) and `mnt/forget.pending`.
- `.devopsy/plugins-local/` holds the local Traefik plugins, `proxies` and `hold`, run by Yaegi: standard library only.
- `tests/` (with the root's `go.mod`) and `.github/workflows/ci.yml` are the checks.
- `README.md` and `docs/` are what users see: a change to behavior changes the doc that covers it (each area's line names it, `docs/running.md` the settings and upgrades) in the same commit.

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
- A pinned name changes with a bump, so a rollback across one goes back exactly, while a floating name goes forward on a rollback (devopsy-cli's docs, "Choosing tags").
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

- `internals/certificates.md`: the resolvers and their names, a router's `tls` label, when Traefik requests or retries a certificate, the ACME stores and `forget`, acme-dns, DNS-01 checks and `check_settings` (`commands/letsencrypt`, `acmedns`, `forget`, `check`, `lib/retry`, `check_settings` and `forget_apply` in `lib/common.sh`); its user doc `docs/certificates.md`; read when a task touches a certificate, a resolver, acme-dns or `PROXY_DNS_*`.
- `internals/proxies.md`: the `proxies` plugin, the forwarded headers and `Forwarded`, Yaegi's limits, plugins loaded again (`plugins-local/.../proxies/`, `commands/proxies`, `mnt/proxies/`); its user doc `docs/proxies.md`; read when a task touches a plugin, a proxy in front, the client IP or a forwarded header.
- `internals/network.md`: the `traefik` network and its aliases, the socket proxy, Traefik read-only, the API's access, `PROXY_READ_TIMEOUT`, an HTTP proxy on the host (`compose.yaml`, `api_access` in `lib/common.sh`); its user docs `README.md` (Routing a project), `docs/running.md`; read when a task touches a network, the socket proxy, the API or an entrypoint's settings.
- `internals/domains.md`: the domains report, checked from the server (`commands/domains`, `lib/facts`, `lib/domains/`); its user doc `docs/domains.md`; read when a task touches `domains`.
- `internals/tunnel.md`: the tunnel end, its trust, default entrypoints and `PROXY_TUNNEL_ONLY` (`tunnel_subnet` and `static_env` in `lib/common.sh`, `commands/setup`); its user doc `docs/tunnel.md`; read when a task touches Cloudflare Tunnel or which entrypoints a router gets.
- `internals/hold.md`: holding visitors through a site's stop, the errors middleware, the catch-all, stopped and paused containers (`plugins-local/.../hold/`, `hold_middleware` in `lib/common.sh`); its user doc `docs/hold.md`; read when a task touches what a visitor gets while a site is down.
- `internals/tests.md`: the test hosts, lanes, script tests, scenarios and releases (`tests/`); read before writing or changing a test.
