//go:build e2e

package tests

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// reviewDeployed takes a scripts host with a Traefik of its own, named as a server
// host's is, and deploys it: what these tests break stays off the shared hosts.
func reviewDeployed(t *testing.T) (host, string) {
	t.Helper()
	h, dir := project(t, "traefik", nil)
	// Traefik's stores are 10001's: the next test's copy could not remove them.
	t.Cleanup(func() {
		_, _ = h.shErr(dir, nil, `devopsy down -v --remove-orphans; docker run --rm -v "$PWD/.devopsy/mnt:/m" busybox:1.37 rm -rf /m/letsencrypt`)
	})
	h.sh(dir, nil, "devopsy deploy")
	h.routed("devopsy-api@file")
	return h, dir
}

// nudge makes a container event, and so one more pass of attach, and waits long
// enough for that pass to have run.
func (h host) nudge() {
	h.t.Helper()
	h.sh("/", nil, `docker run --rm busybox:1.37 true`)
	time.Sleep(3 * time.Second)
}

// BUG: attach parses the networks' compose project out of a string of
// |name=project|, and never checks a network's label as it does a container's
// (plain). A network whose label holds `x|vv-iso_proxy=evil`, sorted first by
// `docker network ls`, makes project_of answer "evil" for another project's
// network: attach disconnects Traefik from that site, or from Traefik's own
// networks (the socket proxy's, cutting every router).
func TestReviewNetworkLabelForgesProject(t *testing.T) {
	h, _ := reviewDeployed(t)
	h.isolated("vv-iso", "vv-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "vv-iso_proxy") })

	h.sh("/", nil, `docker network create --label 'com.docker.compose.project=x|vv-iso_proxy=evil|traefik_dockerproxy=evil' aaa-forged >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker network rm aaa-forged`) })
	h.nudge()

	n := h.proxyNetworks()
	if !joined(n, "vv-iso_proxy") {
		t.Errorf("a label on another network took Traefik off a site's: %s", n)
	}
	if !joined(n, "traefik_dockerproxy") {
		t.Errorf("a label on another network took Traefik off its socket proxy's: %s", n)
	}
	t.Logf("attach said:\n%s", h.sh("/", nil, `docker logs --tail 20 traefik-attach-1 2>&1`))
}

// BUG: pass takes the newest container of the project with devopsy.role=proxy for
// Traefik, from `docker ps -a`, and a one-off of the traefik service (in_traefik,
// or `devopsy run traefik`) carries that label too. While one exists, what it
// should connect goes to the one-off; one left stopped (run without --rm) stops
// attach serving Traefik for good.
func TestReviewOneOffTakenForTraefik(t *testing.T) {
	h, dir := reviewDeployed(t)
	h.sh(dir, nil, "devopsy run --no-deps -T --entrypoint true traefik >/dev/null")
	t.Cleanup(func() {
		_, _ = h.shErr("/", nil, `docker ps -aq --filter label=com.docker.compose.oneoff=True | xargs -r docker rm -f`)
	})
	h.isolated("oo-iso", "oo-iso.example.com")
	deadline := time.Now().Add(10 * time.Second)
	for !joined(h.proxyNetworks(), "oo-iso_proxy") {
		if time.Now().After(deadline) {
			t.Fatalf("Traefik never joined the site with a stopped one-off about: %s\none-offs: %s\nattach:\n%s", h.proxyNetworks(),
				h.sh("/", nil, `docker ps -a --filter label=com.docker.compose.oneoff=True --format '{{.Names}} {{.Status}} {{.Networks}}'`),
				h.sh("/", nil, `docker logs --tail 20 traefik-attach-1 2>&1`))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// BUG: Docker removes a network with a stopped container on it, and a stopped
// Traefik holds no endpoint, so a project's down while Traefik is stopped removes
// the network. attach gets there first when it runs (it disconnects Traefik on the
// destroy event); with it down too, Traefik cannot start: its config names a
// network that is gone, attach skips one that has no project, and `up` fails.
func TestReviewDownWhileTraefikStopped(t *testing.T) {
	h, dir := reviewDeployed(t)
	h.isolated("ds-iso", "ds-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "ds-iso_proxy") })

	h.sh(dir, nil, "devopsy stop attach traefik")
	h.sh("/home/devopsy/sites/ds-iso", nil, "docker compose -p ds-iso down -v")
	t.Logf("networks of the stopped Traefik: %s", h.proxyNetworks())
	t.Logf("network left: %s", h.sh("/", nil, `docker network ls --filter name=ds-iso --format '{{.Name}}'`))
	if out, err := h.shErr(dir, nil, "docker start traefik-traefik-1 2>&1"); err != nil {
		t.Logf("docker start: %v\n%s", err, out)
	}
	h.sh("/", nil, `docker start traefik-attach-1 >/dev/null`)
	time.Sleep(3 * time.Second)
	t.Logf("attach said:\n%s", h.sh("/", nil, `docker logs --tail 10 traefik-attach-1 2>&1`))
	if out, err := h.shErr(dir, nil, "devopsy up -d --wait 2>&1"); err != nil {
		t.Errorf("up does not bring Traefik back: %v\n%s", err, out)
	}
}

// BUG: a change to a project's proxy network, with Traefik on it: compose stops the
// service to recreate a network whose configuration changed, cannot remove one
// Traefik holds, and the release fails with the site down.
func TestReviewProxyNetworkChanged(t *testing.T) {
	h, _ := reviewDeployed(t)
	h.isolated("nc-iso", "nc-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "nc-iso_proxy") })
	dir := "/home/devopsy/sites/nc-iso"
	h.sh(dir, nil, `sed -i 's/proxy: {name: nc-iso_proxy}/proxy: {name: nc-iso_proxy, driver_opts: {com.docker.network.driver.mtu: "1400"}}/' compose.yaml`)
	if out, err := h.shErr(dir, nil, "docker compose -p nc-iso up -d --wait 2>&1"); err != nil {
		t.Errorf("a release changing the proxy network's settings fails: %v\n%s", err, out)
		t.Logf("state after it: %s", h.sh("/", nil, `docker ps -a --filter label=com.docker.compose.project=nc-iso --format '{{.Names}} {{.Status}}'`))
	}
	if a := h.request("https://nc-iso.example.com/"); a.Code != 200 {
		t.Errorf("the site after the change: %+v", a)
	}
}

// The stopped Traefik again, attach up: who gets there first, attach's disconnect
// or compose's removal of the network. Passed its 8 rounds; slow, so only on demand.
func TestReviewDownWhileTraefikStoppedRace(t *testing.T) {
	if os.Getenv("REVIEW_SLOW") == "" {
		t.Skip("REVIEW_SLOW=1 runs it")
	}
	h, dir := reviewDeployed(t)
	h.isolated("dr-iso", "dr-iso.example.com")
	site := "/home/devopsy/sites/dr-iso"
	for i := 0; i < 8; i++ {
		h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "dr-iso_proxy") })
		h.sh(dir, nil, "devopsy stop traefik")
		h.sh(site, nil, "docker compose -p dr-iso down -v")
		if out, err := h.shErr(dir, nil, "docker start traefik-traefik-1 2>&1"); err != nil {
			t.Fatalf("round %d: Traefik does not start after another project's down, attach up: %v\n%s", i, err, out)
		}
		h.sh(site, nil, "docker compose -p dr-iso up -d --wait")
	}
}

// The removal against the next up, at the delays where it can land between up's
// look at the network and its container's creation. Passed its 30 rounds; slow,
// so only on demand.
func TestReviewRemovalAgainstUp(t *testing.T) {
	if os.Getenv("REVIEW_SLOW") == "" {
		t.Skip("REVIEW_SLOW=1 runs it")
	}
	h, _ := reviewDeployed(t)
	h.isolated("ru-iso", "ru-iso.example.com")
	dir := "/home/devopsy/sites/ru-iso"
	failures := 0
	const rounds = 30
	for i := 0; i < rounds; i++ {
		delay := fmt.Sprintf("0.%02d", i%25*2)
		out, err := h.shErr(dir, nil, `docker compose -p ru-iso down -v >/dev/null 2>&1; sleep "$1"; docker compose -p ru-iso up -d --wait 2>&1`, delay)
		if err != nil {
			failures++
			t.Logf("up at %ss after the down failed: %v\n%s", delay, err, out)
			_, _ = h.shErr(dir, nil, `sleep 1; docker compose -p ru-iso up -d --wait`)
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d ups after a down failed", failures, rounds)
	}
}

// A site on an internal network and a routed one still resolves and reaches
// outside names, as templates' apps need for updates and mail.
func TestReviewAppResolvesOutside(t *testing.T) {
	h, _ := reviewDeployed(t)
	h.isolated("dn-iso", "dn-iso.example.com")
	control, cerr := h.shErr("/", nil, `docker run --rm busybox:1.37 nslookup -type=A example.com`)
	out, err := h.inside("dn-iso-web-1", "nslookup -type=A example.com")
	if cerr == nil && err != nil {
		t.Errorf("the app cannot resolve what a plain container can: %v\n%s\ncontrol:\n%s", err, out, control)
	}
	t.Logf("control (%v):\n%s\napp (%v):\n%s", cerr, control, err, out)
	route, _ := h.inside("dn-iso-web-1", "ip route")
	t.Logf("route:\n%s", route)
}

// BUG: a service called dockerproxy on a project's network, once Traefik is on it:
// Traefik reaches the socket proxy by that name (tcp://dockerproxy:2375), and
// Docker's DNS answers with the project's container.
func TestReviewAliasOnTraefik(t *testing.T) {
	h, _ := reviewDeployed(t)
	dir := "/home/devopsy/sites/al-iso"
	compose := `services:
  dockerproxy:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.al-iso.rule=Host(` + "`al-iso.example.com`" + `)
      - traefik.docker.network=al-iso_proxy
    networks: [proxy]
networks:
  proxy: {name: al-iso_proxy}
`
	h.sh("/", nil, `mkdir -p "$1" && printf '%s' "$2" >"$1/compose.yaml"`, dir, compose)
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "docker compose -p al-iso down -v") })
	h.sh(dir, nil, "docker compose -p al-iso up -d --wait")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "al-iso_proxy") })
	site := h.address("al-iso-dockerproxy-1", "al-iso_proxy")
	real := h.address("traefik-dockerproxy-1", "traefik_dockerproxy")
	out := h.sh("/", nil, `docker exec traefik-traefik-1 nslookup -type=A dockerproxy 127.0.0.11 2>&1 || true`)
	t.Logf("site %s, socket proxy %s:\n%s", site, real, out)
	if strings.Contains(out, site) {
		t.Errorf("Traefik resolves dockerproxy to the site's container %s:\n%s", site, out)
	}
}

// BUG: wanted_networks ignores a failed `docker inspect` of the labeled
// containers (lines is empty, and it returns 0), and pass then disconnects Traefik
// from every project's network, though it knows nothing about them. Nothing
// repairs that until the next container event. attach runs on a fake docker here.
func TestReviewFailedInspectCutsNoSite(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	fake := `mkdir /fake && cat >/fake/docker <<'EOF'
#!/bin/sh
case "$1 $2" in
  "ps -aq") case "$*" in *role=proxy*) echo proxy ;; *) echo site ;; esac ;;
  "network ls") echo '|site_proxy=site' ;;
  "network disconnect") : ;;
  "inspect -f") case "$3" in *Config.Labels*) echo failed >&2; exit 1 ;; *) echo ',site_proxy,' ;; esac ;;
  *) sleep 100 ;;
esac
EOF
chmod +x /fake/docker
PATH=/fake:$PATH ATTACH_PROJECT=self timeout 3 sh /attach
true`
	out := h.sh(dir, nil, `docker run --rm -v "$PWD/.devopsy/lib/attach:/attach:ro" busybox:1.37 sh -c "$1"`, fake)
	if strings.Contains(out, "attach: disconnected") {
		t.Errorf("a failed inspect took Traefik off a site:\n%s", out)
	}
}

// BUG: the same selection, by labels anyone can set: a container that claims
// Traefik's project and role, newest of them, gets every project's network
// connected to it by attach, and so reaches each site directly.
func TestReviewForgedProxyLabels(t *testing.T) {
	h, _ := reviewDeployed(t)
	h.sh("/", nil, `docker run -d --name forged -l com.docker.compose.project=traefik -l devopsy.role=proxy busybox:1.37 sleep 100000 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f forged >/dev/null`) })
	h.isolated("fp-iso", "fp-iso.example.com")
	h.nudge()
	on := h.sh("/", nil, `docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' forged`)
	if joined(on, "fp-iso_proxy") {
		out, err := h.inside("forged", "wget -q -T 3 -O - http://web.fp-iso_proxy/ | head -n 1")
		t.Errorf("attach connected a container with forged labels to a site's network: %s\nit reaches the site (%v): %s", on, err, out)
	}
	if !joined(h.proxyNetworks(), "fp-iso_proxy") {
		t.Errorf("and Traefik is not on it: %s", h.proxyNetworks())
	}
}

// attach's healthcheck is green only after its first pass, and `up --wait` fails
// on an unhealthy one: a first pass that connects every site (attach and Traefik
// recreated by one release) has start_period 30 s and two 10 s retries.
func TestReviewFirstPassAgainstHealthcheck(t *testing.T) {
	h, dir := reviewDeployed(t)
	const sites = 80
	h.sh("/", nil, `i=0; while [ $i -lt "$1" ]; do
		docker network create --label com.docker.compose.project=s$i s${i}_proxy >/dev/null
		docker create --name s$i --network s${i}_proxy -l traefik.enable=true -l traefik.docker.network=s${i}_proxy -l com.docker.compose.project=s$i busybox:1.37 sleep 100000 >/dev/null
		i=$((i+1)); done`, fmt.Sprint(sites))
	t.Cleanup(func() {
		_, _ = h.shErr(dir, nil, `devopsy down -v --remove-orphans; docker ps -aq --filter label=traefik.enable=true | xargs -r docker rm -f; docker network prune -f`)
	})
	h.waitFor("the sites connected", func() bool { return joined(h.proxyNetworks(), fmt.Sprintf("s%d_proxy", sites-1)) })
	h.sh(dir, nil, "devopsy stop attach")
	h.sh(dir, nil, "devopsy up -d --wait --no-deps --force-recreate traefik")
	start := time.Now()
	h.sh("/", nil, `docker start traefik-attach-1 >/dev/null`)
	for end := time.Now().Add(4 * time.Minute); time.Now().Before(end); time.Sleep(time.Second) {
		if _, err := h.shErr("/", nil, `docker exec traefik-attach-1 test -e /tmp/attach.ready`); err == nil {
			break
		}
	}
	took := time.Since(start)
	t.Logf("first pass over %d sites: %s; Traefik on %d networks; health: %s", sites, took, len(strings.Fields(h.proxyNetworks())), h.sh("/", nil, `docker inspect -f '{{.State.Health.Status}}' traefik-attach-1`))
	if took > 50*time.Second {
		t.Errorf("the first pass over %d sites took %s, past the healthcheck's 50 s", sites, took)
	}
}
