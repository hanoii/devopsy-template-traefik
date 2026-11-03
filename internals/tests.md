# Tests

The template's tests: hosts, lanes, script tests, scenarios and releases. Code: `tests/` (its package doc comment, `tests/doc.go`, says how a run is set up).

## Hosts

- Hosts are privileged Debian containers (`tests/Dockerfile`), each with its own dockerd and the deploy user, devopsy installed (the latest release, installed once and copied, else `DEVOPSY_BIN`, a linux build) and the checkout's `.devopsy/` copied in by `git ls-files`, so never `.env` or `mnt`.
- A test takes a host for itself (`take`), since the networks have fixed names (`traefik`, `tunnel`) and the ports are the host's.
- Scripts hosts are for script tests, which make and remove the networks or deploy a Traefik of their own, taken down after.
- Server hosts each have a Traefik that the first scenario taking the host deploys and the next ones share (`traefik()`).
- `TESTS_LANES` (3) server hosts and one fewer scripts hosts run at once.
- A test that changes its Traefik's settings or log (a resolver it lacks, a setting of its own) deploys one on a scripts host.
- Docker Hub comes through a mirror kept in a volume (`devopsy-traefik-tests-mirror`); `TESTS_KEEP=1` leaves the containers.

## Writing a test

- A script test is `h, dir := project(t, name, env)` (a copy with its own compose project and `.env`, taken down after), then `h.sh(dir, env, "devopsy ...")`.
- `devopsy t '<shell>'` runs `lib/common.sh`'s functions through devopsy, a command each copy gets, never the template's.
- A scenario is `h := traefik(t)` and `h.site(...)` (a container routed through it), then `h.request`, `h.during` (a request every 0.2 s around an action), `h.from` (curl on a network) and `h.api`.
- A scenario leaves its host's Traefik as it found it (for example `proxies add` removes its proxy).
- A test waits for what Traefik does (`h.routed`, `h.unrouted`), never sleeps for it.
- Every host's deploy user reaches itself over SSH, so a test releases the template as to a server (`devopsy @devopsy@localhost:prod --release`, `TestRelease`): the upload, the activation, `shared/`, `--vars`, `--reload`, the role and imports.
