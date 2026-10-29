//go:build e2e

package tests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// proxy is the Traefik the scenarios share on the server host, deployed
// once per run as a server's is: `devopsy deploy` in its project, compose project traefik,
// its ports the host's 80, 443 and 127.0.0.1:8080.
const proxy = "/home/devopsy/traefik"

var (
	proxyOnce sync.Once
	proxyErr  error
)

// traefik deploys the shared Traefik the first time a test asks.
func traefik(t *testing.T) {
	t.Helper()
	proxyOnce.Do(func() {
		if _, proxyErr = newProject(server, "traefik", nil); proxyErr != nil {
			return
		}
		if out, err := shErrOn(server, proxy, nil, "devopsy deploy"); err != nil {
			proxyErr = fmt.Errorf("devopsy deploy: %v\n%s", err, out)
		}
	})
	if proxyErr != nil {
		t.Fatal(proxyErr)
	}
}

// site runs a container routed through the shared Traefik for host, on
// the traefik network, with labels added to its router's; removed when
// the test ends. image runs args, or traefik/whoami without.
func site(t *testing.T, name, host string, labels []string, image string, args ...string) {
	t.Helper()
	cmd := []string{"docker", "run", "-d", "--name", name, "--network", "traefik", "-l", "traefik.enable=true",
		"-l", fmt.Sprintf("traefik.http.routers.%s.rule=Host(`%s`)", name, host)}
	for _, l := range labels {
		cmd = append(cmd, "-l", fmt.Sprintf("traefik.http.%s", strings.ReplaceAll(l, "<name>", name)))
	}
	if image == "" {
		image = "traefik/whoami:v1.11"
	}
	cmd = append(append(cmd, image), args...)
	shOn(t, server, "/", nil, `"$@" >/dev/null`, cmd...)
	t.Cleanup(func() { _, _ = shErrOn(server, "/", nil, `docker rm -f "$1" >/dev/null`, name) })
	routed(t, name+"@docker")
}

// routed waits for router to be in Traefik's API (it reads the
// containers a moment after they start).
func routed(t *testing.T, router string) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		var routers []struct{ Name, Status string }
		// The API may not answer yet: deploy opens it to the host once
		// Traefik runs (api_access).
		out, err := shErrOn(server, "/", nil, `curl -sf "http://127.0.0.1:8080/api/http/routers?per_page=1000"`)
		if err == nil && json.Unmarshal([]byte(out), &routers) == nil {
			for _, r := range routers {
				if r.Name == router {
					return
				}
			}
		}
		if time.Now().After(end) {
			t.Fatalf("no router %s in Traefik's API", router)
		}
	}
}

// api is Traefik's API at path, from the host.
func api(t *testing.T, path string) string {
	t.Helper()
	return shOn(t, server, "/", nil, `curl -sf "http://127.0.0.1:8080$1"`, path)
}

// request is one visitor's request to url on the host's ports, host
// resolved to the host itself: the code, its time and Location, with
// curl's extra arguments.
type answer struct {
	Code     int
	Time     float64
	Location string
}

func request(t *testing.T, url string, extra ...string) answer {
	t.Helper()
	host := regexp.MustCompile(`^https?://([^/:]+)`).FindStringSubmatch(url)[1]
	out := shOn(t, server, "/", nil, `url=$1 host=$2; shift 2
		curl -sk -o /dev/null --max-time 120 --resolve "$host:443:127.0.0.1" --resolve "$host:80:127.0.0.1" \
			-w '%{http_code} %{time_total} %{redirect_url}' "$@" "$url"`, append([]string{url, host}, extra...)...)
	var a answer
	fmt.Sscan(out, &a.Code, &a.Time, &a.Location)
	return a
}

// Traefik as deploy leaves it: every service healthy, every router and
// middleware enabled (the hold's YAML among them, as Traefik reads it),
// no error in its log but ACME's (no certificate can be issued here),
// http redirected, a site routed.
func TestStart(t *testing.T) {
	traefik(t)
	site(t, "start", "start.example.com", nil, "")

	ps := shOn(t, server, proxy, nil, `docker ps --filter label=com.docker.compose.project=traefik --format '{{.Names}} {{.Status}}'`)
	for _, line := range strings.Split(strings.TrimSpace(ps), "\n") {
		if !strings.Contains(line, "(healthy)") {
			t.Errorf("not healthy: %s", line)
		}
	}
	for _, kind := range []string{"routers", "middlewares", "services"} {
		var items []struct{ Name, Status string }
		if err := json.Unmarshal([]byte(api(t, "/api/http/"+kind+"?per_page=1000")), &items); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, it := range items {
			names[it.Name] = true
			if it.Status != "enabled" {
				t.Errorf("%s %s: %s", kind, it.Name, it.Status)
			}
		}
		want := map[string][]string{
			"routers":     {"hold@file", "hold-websecure@file", "hold-tunnel@file", "devopsy-api@file", "start@docker"},
			"middlewares": {"hold@file", "hold-wait@file", "hold-websecure@file", "hold-tunnel@file", "forwarded@file"},
			"services":    {"hold@file"},
		}[kind]
		for _, n := range want {
			if !names[n] {
				t.Errorf("no %s %s", kind, n)
			}
		}
	}
	logs := shOn(t, server, proxy, nil, `docker logs traefik-traefik-1 2>&1`)
	for _, line := range strings.Split(logs, "\n") {
		if (strings.Contains(line, " ERR ") || strings.Contains(line, "level=error")) && !strings.Contains(strings.ToLower(line), "acme") {
			t.Errorf("Traefik's log: %s", line)
		}
	}
	if a := request(t, "http://start.example.com/x"); a.Code != 301 && a.Code != 308 || a.Location != "https://start.example.com/x" {
		t.Errorf("http: %+v", a)
	}
	if a := request(t, "https://start.example.com/"); a.Code != 200 {
		t.Errorf("https: %+v", a)
	}
}
