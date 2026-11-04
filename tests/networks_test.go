//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// isolatedCompose is the compose file of a project routed through Traefik for
// hostname, as the templates are: web on private and on a network of its own that
// Traefik is asked to join, db on private, which is internal.
func isolatedCompose(name, hostname string) string {
	return fmt.Sprintf(`services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.%[1]s.rule=Host(%[2]s)
      - traefik.docker.network=%[1]s_proxy
    networks: [private, proxy]
  db:
    image: busybox:1.37
    command: [sleep, "100000"]
    networks: [private]
networks:
  private: {internal: true}
  proxy: {name: %[1]s_proxy}
`, name, "`"+hostname+"`")
}

// compose runs a compose file as project name on h, taken down when the test ends.
func (h host) compose(name, file string) {
	h.t.Helper()
	dir := "/home/devopsy/sites/" + name
	h.sh("/", nil, `mkdir -p "$1" && printf '%s' "$2" >"$1/compose.yaml"`, dir, file)
	h.t.Cleanup(func() { _, _ = h.shErr(dir, nil, "docker compose -p "+name+" down -v") })
	h.sh(dir, nil, "docker compose -p "+name+" up -d --wait")
}

// isolated runs isolatedCompose's project on h, routed.
func (h host) isolated(name, hostname string) {
	h.t.Helper()
	h.compose(name, isolatedCompose(name, hostname))
	h.routed(name + "@docker")
}

// ownTraefik takes a scripts host with a Traefik of its own, for what a test breaks
// or changes in it.
func ownTraefik(t *testing.T) (host, string) {
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

// passed waits for a pass of attach begun after this call: it connects a network
// that only now exists, so whatever was there before has been read.
func (h host) passed() {
	h.t.Helper()
	n := fmt.Sprintf("pass%d", time.Now().UnixNano())
	h.sh("/", nil, `docker network create --label "com.docker.compose.project=$1" "$1_proxy" >/dev/null &&
		docker run -d --name "$1" --network "$1_proxy" -l traefik.docker.network="$1_proxy" -l "com.docker.compose.project=$1" busybox:1.37 sleep 100000 >/dev/null`, n)
	h.waitFor("a pass of attach", func() bool { return joined(h.proxyNetworks(), n+"_proxy") })
	h.sh("/", nil, `docker rm -f "$1" >/dev/null`, n)
	h.waitFor("attach to remove what it passed on", func() bool { return !joined(h.proxyNetworks(), n+"_proxy") })
}

// inside runs a shell script in a busybox sharing container's networks: the
// templates' images have no shell.
func (h host) inside(container, script string) (string, error) {
	return h.shErr("/", nil, `docker run --rm -q --network "container:$1" busybox:1.37 sh -c "$2"`, container, script)
}

// networksOf is what container is on, sorted by Docker.
func (h host) networksOf(container string) string {
	h.t.Helper()
	return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$1"`, container))
}

// proxyNetworks is what Traefik's container is on.
func (h host) proxyNetworks() string {
	h.t.Helper()
	return h.networksOf("traefik-traefik-1")
}

// address is container's address on network.
func (h host) address(container, network string) string {
	h.t.Helper()
	return strings.TrimSpace(h.sh("/", nil, `docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$1"`, container, network))
}

func joined(networks, network string) bool {
	for _, n := range strings.Fields(networks) {
		if n == network {
			return true
		}
	}
	return false
}

// Two projects each on a network of their own, with Traefik on both: both answer
// through Traefik, one reaches nothing of the other's directly, a database has
// no way out, and the API refuses a site even once api_access has listed its
// network's gateway.
func TestProjectIsolation(t *testing.T) {
	h := traefik(t)
	h.isolated("aa-iso", "aa-iso.example.com")
	h.isolated("bb-iso", "bb-iso.example.com")
	for _, site := range []string{"aa-iso", "bb-iso"} {
		if a := h.request("https://" + site + ".example.com/"); a.Code != 200 {
			t.Errorf("%s through Traefik: %+v", site, a)
		}
	}

	web := "aa-iso-web-1"
	// The same way in reaches this project's own, or the refusals below prove nothing.
	for _, target := range []string{"web.aa-iso_proxy", h.address(web, "aa-iso_private")} {
		if out, err := h.inside(web, "wget -q -T 3 -O /dev/null http://"+target+"/"); err != nil {
			t.Errorf("did not reach %s in its own project: %v\n%s", target, err, out)
		}
	}
	for _, target := range []string{
		"web.bb-iso_proxy",
		"web.bb-iso_private",
		h.address("bb-iso-web-1", "bb-iso_proxy"),
		h.address("bb-iso-web-1", "bb-iso_private"),
		h.address("bb-iso-db-1", "bb-iso_private"),
	} {
		if out, err := h.inside(web, "wget -q -T 3 -O /dev/null http://"+target+"/"); err == nil {
			t.Errorf("reached %s from the other project: %s", target, out)
		}
	}
	mine := map[string]bool{h.address(web, "aa-iso_proxy"): true, h.address(web, "aa-iso_private"): true}
	out, err := h.inside(web, "nslookup -type=A web 127.0.0.11 | sed -n 's/^Address: //p'")
	if err != nil || strings.TrimSpace(out) == "" {
		t.Errorf("the alias web: %v\n%s", err, out)
	}
	for _, ip := range strings.Fields(out) {
		if !mine[ip] {
			t.Errorf("the alias web resolves to %s, not this project's", ip)
		}
	}

	if route, err := h.inside("aa-iso-db-1", "ip route"); err != nil || strings.Contains(route, "default") {
		t.Errorf("a database's routes: %v\n%s", err, route)
	}
	if route, err := h.inside(web, "ip route"); err != nil || !strings.Contains(route, "default") {
		t.Errorf("the app's routes: %v\n%s", err, route)
	}

	if out := h.sh("/", nil, `docker exec traefik-traefik-1 cat /proc/sys/net/ipv4/ip_forward`); strings.TrimSpace(out) != "0" {
		t.Errorf("Traefik forwards between the networks it is on: %s", out)
	}

	h.sh(proxy, nil, "devopsy t api_access")
	// The API, and the tunnel's entrypoint, which Cloudflare's rules guard.
	for _, path := range []string{":8080/api/version", ":8081/"} {
		url := "http://" + h.address("traefik-traefik-1", "aa-iso_proxy") + path
		if out, err := h.inside(web, "wget -S -q -T 3 -O /dev/null "+url+" 2>&1 || true"); err != nil || !strings.Contains(out, "403") {
			t.Errorf("%s from a site: %v\n%s", path, err, out)
		}
	}
	if out := h.api("/api/version"); !strings.Contains(out, "Version") {
		t.Errorf("the API from the host: %s", out)
	}
}

// A running project with a database moves off the shared network and back, as a
// release and a rollback do: the site answers each time, and the database, which
// nothing else changes, keeps its name, or the app cannot find it.
func TestProjectMoves(t *testing.T) {
	h := traefik(t)
	shared := fmt.Sprintf(`services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.move.rule=Host(%s)
    networks: [default, traefik]
  db:
    image: busybox:1.37
    command: [sleep, "100000"]
networks:
  traefik: {name: traefik, external: true}
`, "`move.example.com`")
	for _, step := range []struct{ name, file string }{
		{"on the shared network", shared},
		{"moved", isolatedCompose("move", "move.example.com")},
		{"moved back", shared},
		{"moved again", isolatedCompose("move", "move.example.com")},
	} {
		h.compose("move", step.file)
		h.routed("move@docker")
		if step.file == shared {
			// As long as a release takes: attach has removed the network it left.
			h.waitFor(step.name+": attach to remove move_proxy", func() bool {
				return strings.TrimSpace(h.sh("/", nil, `docker network ls -q --filter name=^move_proxy$`)) == ""
			})
		}
		h.waitFor(step.name+": the site", func() bool { return h.request("https://move.example.com/").Code == 200 })
		if out, err := h.inside("move-web-1", "nslookup -type=A db 127.0.0.11"); err != nil || !strings.Contains(out, "Name:") {
			t.Errorf("%s: db has no name: %v\n%s", step.name, err, out)
		}
	}
}

// Another project's first release, named to sort before every network Traefik is
// on: Traefik is not restarted, its default route stays, and a request every
// 0.2 s, each 3 s long so some are in flight throughout, is answered.
func TestAnotherProjectsRelease(t *testing.T) {
	h := traefik(t)
	h.isolated("mm-iso", "mm-iso.example.com")
	state := func() string {
		return h.sh("/", nil, `docker inspect -f '{{.State.StartedAt}}' traefik-traefik-1; docker exec traefik-traefik-1 ip route | grep ^default`)
	}
	before := state()
	action := `mkdir -p /home/devopsy/sites/aaa-iso && cd /home/devopsy/sites/aaa-iso &&
		printf '%s' '` + isolatedCompose("aaa-iso", "aaa-iso.example.com") + `' >compose.yaml &&
		docker compose -p aaa-iso up -d --wait`
	t.Cleanup(func() { _, _ = h.shErr("/home/devopsy/sites/aaa-iso", nil, "docker compose -p aaa-iso down -v") })
	for _, a := range h.during("https://mm-iso.example.com/?wait=3s", action) {
		if a.Code != 200 {
			t.Errorf("a request during the release: %+v", a)
		}
	}
	h.routed("aaa-iso@docker")
	h.waitFor("the new project answers", func() bool { return h.request("https://aaa-iso.example.com/").Code == 200 })
	if after := state(); after != before {
		t.Errorf("Traefik changed:\n%s\nto\n%s", before, after)
	}
}

// Labels naming what is not their own connect nothing and take nothing from
// Traefik, including one written to pass for the fields around it.
func TestHostileLabels(t *testing.T) {
	h := traefik(t)
	h.isolated("vv-iso", "vv-iso.example.com")
	h.isolated("hh-iso", "hh-iso.example.com")
	before := h.proxyNetworks()
	nl := "\n"
	for i, label := range []string{
		"traefik_dockerproxy", "traefik_netproxy", "traefik_acmedns", "tunnel", "traefik",
		"vv-iso_proxy", "vv-iso_private",
		"vv-iso_private|\"hh-iso\"|,vv-iso_private,|",
		"\"vv-iso_private\"|\"hh-iso\"|,vv-iso_private,",
		"x" + nl + "\"vv-iso_private\"|\"hh-iso\"|,vv-iso_private,",
		"no-such-network",
	} {
		name := fmt.Sprintf("hostile-%d", i)
		h.sh("/", nil, `docker run -d --name "$1" --network hh-iso_proxy -l traefik.enable=true -l "traefik.docker.network=$2" -l com.docker.compose.project=hh-iso busybox:1.37 sleep 100000 >/dev/null`, name, label)
		t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f "$1" >/dev/null`, name) })
	}
	// A container that joined the other project's network itself.
	h.sh("/", nil, `docker run -d --name hostile-joined --network vv-iso_private -l traefik.enable=true -l traefik.docker.network=vv-iso_private -l com.docker.compose.project=hh-iso busybox:1.37 sleep 100000 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f hostile-joined >/dev/null`) })

	// A project connected after them: its pass has read every label above.
	h.isolated("hz-iso", "hz-iso.example.com")
	h.waitFor("the later project answers", func() bool { return h.request("https://hz-iso.example.com/").Code == 200 })
	for _, n := range strings.Fields(before) {
		if !joined(h.proxyNetworks(), n) {
			t.Errorf("Traefik left %s: %s", n, h.proxyNetworks())
		}
	}
	if joined(h.proxyNetworks(), "vv-iso_private") {
		t.Errorf("Traefik joined another project's private network: %s", h.proxyNetworks())
	}
	if !joined(h.proxyNetworks(), "vv-iso_proxy") || h.request("https://vv-iso.example.com/").Code != 200 {
		t.Errorf("the other project lost its route: %s", h.proxyNetworks())
	}
}

// A project taken down leaves no network, though compose gives up on the one
// Traefik holds, and the other stays routed; a stopped one keeps its network, and
// comes back. So does every site when Traefik is recreated, on its own.
func TestProjectNetworksLifecycle(t *testing.T) {
	h := traefik(t)
	h.isolated("ll-iso", "ll-iso.example.com")
	h.isolated("kk-iso", "kk-iso.example.com")
	networks := func(project string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker network ls -q --filter "name=^$1_"`, project))
	}
	dir := "/home/devopsy/sites/ll-iso"

	h.sh(dir, nil, "docker compose -p ll-iso stop")
	h.unrouted("ll-iso@docker")
	h.passed()
	if !joined(h.proxyNetworks(), "ll-iso_proxy") || !strings.Contains(h.sh("/", nil, `docker network ls --format '{{.Name}}'`), "ll-iso_proxy") {
		t.Errorf("a stopped project lost its network: %s", h.proxyNetworks())
	}
	h.sh(dir, nil, "docker compose -p ll-iso start")
	h.waitFor("the started project answers", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })

	route := func() string {
		return h.sh("/", nil, `docker exec traefik-traefik-1 ip route | awk '/^default/ {print $3}'`)
	}
	before := route()
	h.sh(proxy, nil, "devopsy up -d --wait --force-recreate traefik")
	h.waitFor("Traefik back on every project", func() bool {
		n := h.proxyNetworks()
		return joined(n, "ll-iso_proxy") && joined(n, "kk-iso_proxy")
	})
	for _, site := range []string{"ll-iso", "kk-iso"} {
		h.waitFor(site+" answers after Traefik's recreate", func() bool { return h.request("https://"+site+".example.com/").Code == 200 })
	}

	if after := route(); after != before {
		t.Errorf("Traefik's default route moved with its recreate:\n%s\nto\n%s", before, after)
	}

	h.sh(dir, nil, "docker compose -p ll-iso down -v")
	h.waitFor("no network left", func() bool { return networks("ll-iso") == "" })
	if joined(h.proxyNetworks(), "ll-iso_proxy") {
		t.Errorf("Traefik on a network that went: %s", h.proxyNetworks())
	}
	if a := h.request("https://kk-iso.example.com/"); a.Code != 200 {
		t.Errorf("the other project after the down: %+v", a)
	}
	// Down and up at once: the removal waits, so it does not beat the next up.
	h.sh(dir, nil, "docker compose -p ll-iso up -d --wait")
	h.waitFor("up again", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })
	for i := 0; i < 3; i++ {
		h.sh(dir, nil, "docker compose -p ll-iso down -v && docker compose -p ll-iso up -d --wait")
		h.waitFor("up at once", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })
	}
}

// attach on its own: netproxy going away brings it back and it connects what it
// missed; a changed script recreates it and not Traefik, and it still gets through
// netproxy; domains says when it is down.
func TestAttach(t *testing.T) {
	h, dir := ownTraefik(t)
	state := func(format, container string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f "$1" "$2"`, format, container))
	}
	attach, tr := "traefik-attach-1", "traefik-traefik-1"

	restarts := state("{{.RestartCount}}", attach)
	h.sh("/", nil, "docker restart -t 1 traefik-netproxy-1")
	h.waitFor("attach restarted", func() bool { return state("{{.RestartCount}}", attach) != restarts })
	h.waitFor("attach healthy", func() bool { return state("{{.State.Health.Status}}", attach) == "healthy" })
	h.passed()

	ids := func(c string) string { return state("{{.Id}}", c) }
	attachID, traefikID := ids(attach), ids(tr)
	h.sh(dir, nil, `printf '# changed\n' >>.devopsy/lib/attach && devopsy deploy`)
	if ids(attach) == attachID {
		t.Errorf("a changed script left attach running")
	}
	if ids(tr) != traefikID {
		t.Errorf("a changed script recreated Traefik")
	}
	h.passed()

	if out := h.sh(dir, nil, "devopsy domains 2>&1"); strings.Contains(out, "attach is not running") {
		t.Errorf("domains with attach up:\n%s", out)
	}
	start := time.Now()
	h.sh(dir, nil, "devopsy stop attach")
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("attach took %s to stop: it waited out the grace period", took)
	}
	if out := h.sh(dir, nil, "devopsy domains 2>&1"); !strings.Contains(out, "attach is not running") {
		t.Errorf("domains with attach down:\n%s", out)
	}
}

// A one-off of Traefik's service, as a command runs, carries its labels: running or
// stopped, it joins nothing, and Traefik joins a project that appears meanwhile.
func TestOneOffIsNotTraefik(t *testing.T) {
	h := traefik(t)
	h.isolated("oa-iso", "oa-iso.example.com")
	ids := func() []string {
		return strings.Fields(h.sh("/", nil, `docker ps -aq --filter label=com.docker.compose.project=traefik --filter label=com.docker.compose.oneoff=True`))
	}
	t.Cleanup(func() {
		for _, id := range ids() {
			_, _ = h.shErr("/", nil, `docker rm -f "$1" >/dev/null`, id)
		}
	})
	h.sh(proxy, nil, `devopsy run -d --no-deps --entrypoint sh traefik -c 'sleep 300' >/dev/null`)
	h.sh(proxy, nil, `devopsy run --no-deps -T --entrypoint true traefik >/dev/null`)
	if got := ids(); len(got) != 2 {
		t.Fatalf("one-offs: %v", got)
	}
	h.isolated("ob-iso", "ob-iso.example.com")
	h.waitFor("Traefik on a project that appeared", func() bool { return joined(h.proxyNetworks(), "ob-iso_proxy") })
	for _, id := range ids() {
		n := h.networksOf(id)
		if strings.Contains(n, "_proxy") {
			t.Errorf("a one-off joined a project's network: %s", n)
		}
	}
}

// A network's label, like a container's, cannot pass for the fields around it:
// neither cut Traefik off another project or its own networks, nor keep it off one.
func TestNetworkLabelsAreText(t *testing.T) {
	h := traefik(t)
	h.isolated("vv-iso", "vv-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "vv-iso_proxy") })
	h.sh("/", nil, `docker network create --label 'com.docker.compose.project=x|vv-iso_proxy=evil|traefik_dockerproxy=evil|ww-iso_proxy=traefik' aaa-forged >/dev/null &&
		docker network create --label "$(printf 'com.docker.compose.project=x\nvv-iso_proxy=evil')" aaa-forged2 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker network rm aaa-forged aaa-forged2`) })
	h.passed()
	n := h.proxyNetworks()
	if !joined(n, "vv-iso_proxy") || !joined(n, "traefik_dockerproxy") {
		t.Errorf("a label on another network took Traefik off one: %s", n)
	}
	h.isolated("ww-iso", "ww-iso.example.com")
	h.waitFor("Traefik on a project another network's label names", func() bool { return joined(h.proxyNetworks(), "ww-iso_proxy") })
}

// Docker removes a network a stopped container names, compose may make it again
// under the name, and that container cannot start on either: a project's down, or
// its down and up, while Traefik and attach are stopped must not keep Traefik down.
func TestDownWhileTraefikStopped(t *testing.T) {
	h := traefik(t)
	h.isolated("ds-iso", "ds-iso.example.com")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "ds-iso_proxy") })
	h.sh(proxy, nil, "devopsy stop attach traefik")
	h.sh("/home/devopsy/sites/ds-iso", nil, "docker compose -p ds-iso down -v")
	if out, err := h.shErr(proxy, nil, "devopsy up -d --wait 2>&1"); err != nil {
		t.Fatalf("up does not bring Traefik back: %v\n%s", err, out)
	}
	h.routed("devopsy-api@file")

	dir := "/home/devopsy/sites/ds-iso"
	h.sh(dir, nil, "docker compose -p ds-iso up -d --wait")
	h.waitFor("Traefik on the site again", func() bool { return joined(h.proxyNetworks(), "ds-iso_proxy") })
	h.sh(proxy, nil, "devopsy stop attach traefik")
	h.sh(dir, nil, "docker compose -p ds-iso down -v && docker compose -p ds-iso up -d --wait")
	if out, err := h.shErr(proxy, nil, "devopsy up -d --wait 2>&1"); err != nil {
		t.Fatalf("up does not bring Traefik back on a network made again: %v\n%s", err, out)
	}
	h.routed("ds-iso@docker")
	h.waitFor("the site answers on the network made again", func() bool { return h.request("https://ds-iso.example.com/").Code == 200 })
}

// A project's service named as one of Traefik's own does not take that name over:
// Traefik's provider goes on, on the name scoped to the socket proxy's network.
func TestProjectServiceNamedAsTraefiksOwn(t *testing.T) {
	h := traefik(t)
	h.compose("al-iso", `services:
  dockerproxy:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.al-iso.rule=Host(`+"`al-iso.example.com`"+`)
      - traefik.docker.network=al-iso_proxy
    networks: [proxy]
networks:
  proxy: {name: al-iso_proxy}
`)
	h.routed("al-iso@docker")
	h.waitFor("Traefik on the site", func() bool { return joined(h.proxyNetworks(), "al-iso_proxy") })
	site := h.address("al-iso-dockerproxy-1", "al-iso_proxy")
	endpoint := h.sh("/", nil, `docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' traefik-traefik-1 | sed -n 's|^TRAEFIK_PROVIDERS_DOCKER_ENDPOINT=tcp://||p' | cut -d: -f1`)
	out := h.sh("/", nil, `docker exec traefik-traefik-1 nslookup -type=A "$1" 127.0.0.11 2>&1 || true`, strings.TrimSpace(endpoint))
	if strings.Contains(out, site) || !strings.Contains(out, "Address") {
		t.Errorf("Traefik's endpoint %s resolves to the site's container %s:\n%s", endpoint, site, out)
	}
	h.isolated("am-iso", "am-iso.example.com")
}

// A read that fails skips the pass: an empty answer would read as nothing wanted,
// and take Traefik off every site. Whichever read fails, and with none failing,
// which shows the fake can tell. On a fake docker, since a real one fails on no
// command.
func TestFailedReadCutsNoSite(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	run := func(fails string) string {
		fake := `mkdir /fake && cat >/fake/docker <<'EOF'
#!/bin/sh
case "$1 $2" in
  "ps -aq") case "$*" in *role=proxy*) echo proxy ;; *) [ "$FAILS" = none ] || echo site ;; esac ;;
  "network ls") [ "$FAILS" != networks ] || { echo failed >&2; exit 1; }; echo 'site_proxy=abc="site"' ;;
  "network disconnect") : ;;
  "inspect -f") case "$3" in
    *Config.Labels*) [ "$FAILS" != labels ] || { echo failed >&2; exit 1; }; echo '"site_proxy"|"site"|,site_proxy,' ;;
    *) echo 'site_proxy=abc ' ;;
    esac ;;
  *) sleep 100 ;;
esac
EOF
chmod +x /fake/docker
FAILS=$1 PATH=/fake:$PATH ATTACH_PROJECT=self timeout 3 sh /attach
true`
		return h.sh(dir, nil, `docker run --rm -v "$PWD/.devopsy/lib/attach:/attach:ro" busybox:1.37 sh -c "$1" sh "$2"`, fake, fails)
	}
	for _, fails := range []string{"networks", "labels"} {
		if out := run(fails); strings.Contains(out, "disconnected") {
			t.Errorf("a failed read of %s took Traefik off a site:\n%s", fails, out)
		}
	}
	if out := run("none"); !strings.Contains(out, "disconnected site_proxy") {
		t.Errorf("nothing wanted, and Traefik stayed on the network:\n%s", out)
	}
}
