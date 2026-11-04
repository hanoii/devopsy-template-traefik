//go:build e2e

package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// isolated runs a compose project routed through h's Traefik for hostname,
// as the templates are: web on default and on a network of its own that Traefik
// is asked to join, db on default, which is internal. Taken down when the test
// ends.
func (h host) isolated(name, hostname string) {
	h.t.Helper()
	dir := "/home/devopsy/sites/" + name
	compose := fmt.Sprintf(`services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.%[1]s.rule=Host(%[2]s)
      - traefik.docker.network=%[1]s_proxy
    networks: [default, proxy]
  db:
    image: busybox:1.37
    command: [sleep, "100000"]
networks:
  default: {internal: true}
  proxy: {name: %[1]s_proxy}
`, name, "`"+hostname+"`")
	h.sh("/", nil, `mkdir -p "$1" && printf '%s' "$2" >"$1/compose.yaml"`, dir, compose)
	h.t.Cleanup(func() { _, _ = h.shErr(dir, nil, "docker compose -p "+name+" down -v") })
	h.sh(dir, nil, "docker compose -p "+name+" up -d --wait")
	h.routed(name + "@docker")
}

// inside runs a shell script in a busybox sharing container's networks: the
// templates' images have no shell.
func (h host) inside(container, script string) (string, error) {
	return h.shErr("/", nil, `docker run --rm -q --network "container:$1" busybox:1.37 sh -c "$2"`, container, script)
}

// proxyNetworks is what Traefik's container is on, sorted by Docker.
func (h host) proxyNetworks() string {
	h.t.Helper()
	return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' traefik-traefik-1`))
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

// waitFor polls cond until it holds, for what attach does a moment after the
// event it reacts to.
func (h host) waitFor(what string, cond func() bool) {
	h.t.Helper()
	for end := time.Now().Add(30 * time.Second); !cond(); time.Sleep(250 * time.Millisecond) {
		if time.Now().After(end) {
			h.t.Fatalf("never: %s", what)
		}
	}
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
	for _, target := range []string{
		"web.bb-iso_proxy",
		"web.bb-iso_default",
		h.address("bb-iso-web-1", "bb-iso_proxy"),
		h.address("bb-iso-web-1", "bb-iso_default"),
		h.address("bb-iso-db-1", "bb-iso_default"),
	} {
		if out, err := h.inside(web, "wget -q -T 3 -O /dev/null http://"+target+"/"); err == nil {
			t.Errorf("reached %s from the other project: %s", target, out)
		}
	}
	mine := map[string]bool{h.address(web, "aa-iso_proxy"): true, h.address(web, "aa-iso_default"): true}
	out, _ := h.inside(web, "nslookup -type=A web 127.0.0.11 | sed -n 's/^Address: //p'")
	for _, ip := range strings.Fields(out) {
		if !mine[ip] {
			t.Errorf("the alias web resolves to %s, not this project's", ip)
		}
	}

	if route, _ := h.inside("aa-iso-db-1", "ip route"); strings.Contains(route, "default") {
		t.Errorf("a database has a way out:\n%s", route)
	}
	if route, _ := h.inside(web, "ip route"); !strings.Contains(route, "default") {
		t.Errorf("the app has no way out:\n%s", route)
	}

	h.sh(proxy, nil, "devopsy t api_access")
	api := "http://" + h.address("traefik-traefik-1", "aa-iso_proxy") + ":8080/api/version"
	if out, err := h.inside(web, "wget -S -q -T 3 -O /dev/null "+api+" 2>&1 || true"); err != nil || !strings.Contains(out, "403") {
		t.Errorf("the API from a site: %v\n%s", err, out)
	}
	if out := h.api("/api/version"); !strings.Contains(out, "Version") {
		t.Errorf("the API from the host: %s", out)
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
	action := `mkdir -p /home/devopsy/sites/aaa-iso && cd /home/devopsy/sites/aaa-iso && cat >compose.yaml <<'YAML'
services:
  web:
    image: traefik/whoami:v1.11
    labels:
      - traefik.enable=true
      - traefik.http.routers.aaa-iso.rule=Host(` + "`aaa-iso.example.com`" + `)
      - traefik.docker.network=aaa-iso_proxy
    networks: [proxy]
networks:
  proxy: {name: aaa-iso_proxy}
YAML
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
		"vv-iso_proxy", "vv-iso_default",
		"vv-iso_default|\"hh-iso\"|,vv-iso_default,|",
		"\"vv-iso_default\"|\"hh-iso\"|,vv-iso_default,",
		"x" + nl + "\"vv-iso_default\"|\"hh-iso\"|,vv-iso_default,",
		"no-such-network",
	} {
		name := fmt.Sprintf("hostile-%d", i)
		h.sh("/", nil, `docker run -d --name "$1" --network hh-iso_proxy -l traefik.enable=true -l "traefik.docker.network=$2" -l com.docker.compose.project=hh-iso busybox:1.37 sleep 100000 >/dev/null`, name, label)
		t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f "$1" >/dev/null`, name) })
	}
	// A container that joined the other project's network itself.
	h.sh("/", nil, `docker run -d --name hostile-joined --network vv-iso_default -l traefik.enable=true -l traefik.docker.network=vv-iso_default -l com.docker.compose.project=hh-iso busybox:1.37 sleep 100000 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f hostile-joined >/dev/null`) })

	// A project connected after them: its pass has read every label above.
	h.isolated("hz-iso", "hz-iso.example.com")
	h.waitFor("the later project answers", func() bool { return h.request("https://hz-iso.example.com/").Code == 200 })
	for _, n := range strings.Fields(before) {
		if !joined(h.proxyNetworks(), n) {
			t.Errorf("Traefik left %s: %s", n, h.proxyNetworks())
		}
	}
	if joined(h.proxyNetworks(), "vv-iso_default") {
		t.Errorf("Traefik joined another project's default: %s", h.proxyNetworks())
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
	// A later event's pass has seen the stopped project.
	h.sh("/", nil, `docker run --rm --name ll-event busybox:1.37 true`)
	time.Sleep(time.Second)
	if !joined(h.proxyNetworks(), "ll-iso_proxy") || !strings.Contains(h.sh("/", nil, `docker network ls --format '{{.Name}}'`), "ll-iso_proxy") {
		t.Errorf("a stopped project lost its network: %s", h.proxyNetworks())
	}
	h.sh(dir, nil, "docker compose -p ll-iso start")
	h.waitFor("the started project answers", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })

	h.sh(proxy, nil, "devopsy up -d --wait --force-recreate traefik")
	h.waitFor("Traefik back on every project", func() bool {
		n := h.proxyNetworks()
		return joined(n, "ll-iso_proxy") && joined(n, "kk-iso_proxy")
	})
	for _, site := range []string{"ll-iso", "kk-iso"} {
		h.waitFor(site+" answers after Traefik's recreate", func() bool { return h.request("https://"+site+".example.com/").Code == 200 })
	}

	h.sh(dir, nil, "docker compose -p ll-iso down -v")
	h.waitFor("no network left", func() bool { return networks("ll-iso") == "" })
	if joined(h.proxyNetworks(), "ll-iso_proxy") {
		t.Errorf("Traefik on a network that went: %s", h.proxyNetworks())
	}
	if a := h.request("https://kk-iso.example.com/"); a.Code != 200 {
		t.Errorf("the other project after the down: %+v", a)
	}
	// Down and up at once: the removal never beats the next up.
	h.sh(dir, nil, "docker compose -p ll-iso up -d --wait")
	h.waitFor("up again", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })
	for i := 0; i < 3; i++ {
		h.sh(dir, nil, "docker compose -p ll-iso down -v && docker compose -p ll-iso up -d --wait")
		h.waitFor("up at once", func() bool { return h.request("https://ll-iso.example.com/").Code == 200 })
	}
}

// attach on its own: netproxy going away brings it back and it connects what it
// missed; a changed script recreates it and not Traefik; domains says when it is
// down.
func TestAttach(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	h.sh(dir, nil, "devopsy deploy")
	state := func(format, container string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f "$1" "$2"`, format, container))
	}
	attach, tr := "traefik-attach-1", "traefik-traefik-1"

	restarts := state("{{.RestartCount}}", attach)
	h.sh("/", nil, "docker restart -t 1 traefik-netproxy-1")
	h.waitFor("attach restarted", func() bool { return state("{{.RestartCount}}", attach) != restarts })
	h.waitFor("attach healthy", func() bool { return state("{{.State.Health.Status}}", attach) == "healthy" })
	h.sh("/", nil, `docker network create --label com.docker.compose.project=missed missed_proxy >/dev/null &&
		docker run -d --name missed --network missed_proxy -l traefik.enable=true -l traefik.docker.network=missed_proxy -l com.docker.compose.project=missed busybox:1.37 sleep 100000 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker rm -f missed; docker network rm missed_proxy") })
	h.waitFor("attach connected a new network", func() bool { return joined(h.proxyNetworks(), "missed_proxy") })

	ids := func(c string) string { return state("{{.Id}}", c) }
	attachID, traefikID := ids(attach), ids(tr)
	h.sh(dir, nil, `printf '# changed\n' >>.devopsy/lib/attach && devopsy deploy`)
	if ids(attach) == attachID {
		t.Errorf("a changed script left attach running")
	}
	if ids(tr) != traefikID {
		t.Errorf("a changed script recreated Traefik")
	}

	if out := h.sh(dir, nil, "devopsy domains 2>&1"); strings.Contains(out, "attach is not running") {
		t.Errorf("domains with attach up:\n%s", out)
	}
	h.sh(dir, nil, "devopsy stop attach")
	if out := h.sh(dir, nil, "devopsy domains 2>&1"); !strings.Contains(out, "attach is not running") {
		t.Errorf("domains with attach down:\n%s", out)
	}
}
