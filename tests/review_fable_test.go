//go:build e2e

package tests

import (
	"strings"
	"testing"
	"time"
)

// Review tests of attach (lib/attach), kept on the branch as the review's
// record. A failing one proves the bug its comment names.

// oneOffs is the one-off containers of the server host's Traefik project, as
// in_traefik (lib/common.sh) leaves while a command runs.
func (h host) oneOffs() string {
	h.t.Helper()
	return strings.TrimSpace(h.sh("/", nil, `docker ps -aq --filter label=com.docker.compose.project=traefik --filter label=com.docker.compose.oneoff=True`))
}

// Bug: attach takes the newest container labeled devopsy.role=proxy of its
// project for Traefik (`docker ps -aq ... | head -n 1` in pass). A one-off
// Traefik container, as in_traefik runs for `proxies refresh` (a step of every
// deploy with proxies), `letsencrypt` and `acmedns`, carries the service's
// labels and is newer, so while it runs attach connects it, not Traefik, to
// every project's network, and a project released then is unreachable until
// the next container event on the host.
func TestFableOneOffTakenForTraefik(t *testing.T) {
	h := traefik(t)
	h.isolated("oa-iso", "oa-iso.example.com")

	h.sh(proxy, nil, `devopsy run -d --no-deps --entrypoint sh traefik -c 'sleep 300' >/dev/null`)
	cleanup := func() {
		if ids := h.oneOffs(); ids != "" {
			_, _ = h.shErr("/", nil, `docker rm -f $1 >/dev/null`, strings.Join(strings.Fields(ids), " "))
		}
	}
	t.Cleanup(cleanup)
	oneOff := h.oneOffs()
	if strings.Contains(oneOff, "\n") || oneOff == "" {
		t.Fatalf("one-off containers: %q", oneOff)
	}
	networksOf := func(id string) string {
		return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$1"`, id))
	}

	// Its create event ran a pass: the one-off is now on the project's network.
	time.Sleep(2 * time.Second)
	if joined(networksOf(oneOff), "oa-iso_proxy") {
		t.Errorf("attach connected a one-off Traefik container to a project's network: %s", networksOf(oneOff))
	}

	// A project released while the one-off runs.
	h.isolated("ob-iso", "ob-iso.example.com")
	time.Sleep(3 * time.Second)
	if !joined(h.proxyNetworks(), "ob-iso_proxy") {
		t.Errorf("Traefik is not on the new project's network while a one-off runs: traefik %s; one-off %s",
			h.proxyNetworks(), networksOf(oneOff))
	}
	cleanup()
	// Once the one-off is gone, its destroy event's pass connects Traefik.
	h.waitFor("Traefik on the new project's network after the one-off", func() bool { return joined(h.proxyNetworks(), "ob-iso_proxy") })
}

// Bug: attach reads the networks' compose project labels as `|name=project|`
// text (project_of in lib/attach), so a network label holding `|` and `=`
// passes for other networks' fields, which the container labels cannot (plain
// in lib/attach refuses them). Needs the Docker API to set, so outside the
// threat model of a compromised container; kept since the network's label is
// as much anyone's text as the container's.
func TestFableNetworkLabelParsedAsFields(t *testing.T) {
	h := traefik(t)
	h.sh("/", nil, `docker network create --label 'com.docker.compose.project=x|fc-iso_proxy=traefik' fable-forge >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker network rm fable-forge >/dev/null") })
	h.isolated("fc-iso", "fc-iso.example.com")
	time.Sleep(3 * time.Second)
	if !joined(h.proxyNetworks(), "fc-iso_proxy") {
		t.Errorf("a network's label kept Traefik off another project's network: %s", h.proxyNetworks())
	}
}

// attach recreated on its own (a changed script, netproxy untouched) still
// connects what appears next: netproxy's allowfrom names it by hostname,
// resolved on each request, so a new address is let in.
func TestFableAttachRecreatedAlone(t *testing.T) {
	h, dir := project(t, "traefik", nil)
	h.sh(dir, nil, "devopsy deploy")
	id := func() string { return strings.TrimSpace(h.sh("/", nil, `docker inspect -f '{{.Id}}' traefik-attach-1`)) }
	attachID := id()
	h.sh(dir, nil, `printf '# changed by the review\n' >>.devopsy/lib/attach && devopsy deploy`)
	if id() == attachID {
		t.Fatalf("a changed script left attach running")
	}
	h.sh("/", nil, `docker network create --label com.docker.compose.project=alone alone_proxy >/dev/null &&
		docker run -d --name alone --network alone_proxy -l traefik.enable=true -l traefik.docker.network=alone_proxy -l com.docker.compose.project=alone busybox:1.37 sleep 100000 >/dev/null`)
	t.Cleanup(func() { _, _ = h.shErr("/", nil, "docker rm -f alone; docker network rm alone_proxy") })
	h.waitFor("the recreated attach connected a new network", func() bool { return joined(h.proxyNetworks(), "alone_proxy") })
}
