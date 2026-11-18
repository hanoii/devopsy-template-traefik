//go:build e2e

package tests

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// check refuses what Traefik would take without a word, the release's
// prepare step: each refusal, and what passes.
func TestCheckSettings(t *testing.T) {
	h, dir := project(t, "check", nil)
	for _, c := range []struct {
		env  map[string]string
		want string // empty: passes
	}{
		{nil, ""},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-selfhosted", "COMPOSE_PROFILES": "acmedns", "PROXY_ACMEDNS_DOMAIN": "acme.example.com"}, ""},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-cloudflare", "PROXY_CLOUDFLARE_DNS_API_TOKEN": "x"}, ""},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "none", "PROXY_TUNNEL_ONLY": "true", "PROXY_BASE_DOMAIN": "vm1.example.com"}, ""},
		{map[string]string{"PROXY_TUNNEL_ONLY": "false"}, ""},
		{map[string]string{"PROXY_LE_ACME_EMAIL": "", "PROXY_TUNNEL_ONLY": "true"}, ""},
		{map[string]string{"PROXY_LE_ACME_EMAIL": ""}, "set PROXY_LE_ACME_EMAIL"},
		{map[string]string{"PROXY_LE_ACME_EMAIL": "", "PROXY_TUNNEL_ONLY": "false"}, "set PROXY_LE_ACME_EMAIL"},
		{map[string]string{"PROXY_CERTRESOLVER": "letsencrypt"}, "PROXY_CERTRESOLVER must be"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-http01"}, "PROXY_WILDCARD_CERTRESOLVER must be"},
		{map[string]string{"PROXY_TUNNEL_ONLY": "yes"}, "PROXY_TUNNEL_ONLY must be"},
		{map[string]string{"COMPOSE_PROFILES": "acmedns"}, "set PROXY_ACMEDNS_DOMAIN"},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-selfhosted"}, "PROXY_CERTRESOLVER is le-dns01-selfhosted: add acmedns"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-dns01-selfhosted"}, "PROXY_WILDCARD_CERTRESOLVER is le-dns01-selfhosted: add acmedns"},
		{map[string]string{"PROXY_CERTRESOLVER": "le-dns01-cloudflare"}, "PROXY_CERTRESOLVER is le-dns01-cloudflare: set PROXY_CLOUDFLARE_DNS_API_TOKEN"},
		{map[string]string{"PROXY_WILDCARD_CERTRESOLVER": "le-dns01-cloudflare"}, "PROXY_WILDCARD_CERTRESOLVER is le-dns01-cloudflare: set"},
		{map[string]string{"PROXY_BASE_DOMAIN": ".example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "example.com."}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "vm1..example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
		{map[string]string{"PROXY_BASE_DOMAIN": "VM1.example.com"}, "PROXY_BASE_DOMAIN is not a domain name"},
	} {
		out, err := h.shErr(dir, c.env, "devopsy check")
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%v: refused: %s", c.env, out)
		case c.want != "" && err == nil:
			t.Errorf("%v: passed, want %q: %s", c.env, c.want, out)
		case c.want != "" && !strings.Contains(out, c.want):
			t.Errorf("%v: want %q in: %s", c.env, c.want, out)
		}
	}
}

// The status capability shows what the server knows of its setup, and
// warns of what is left on its side: a tunnel without a connected
// connector.
func TestStatus(t *testing.T) {
	h, dir := project(t, "status", nil)
	status := func(env map[string]string, warns bool, want []string, not ...string) {
		t.Helper()
		out, err := h.shErr(dir, env, "devopsy --status")
		// A warning, and only a warning, makes --status exit 3.
		if warns != (err != nil) {
			t.Errorf("%v: warns %v, exit: %v\n%s", env, warns, err, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: want %q in:\n%s", env, w, out)
			}
		}
		for _, n := range not {
			if strings.Contains(out, n) {
				t.Errorf("%v: %q in:\n%s", env, n, out)
			}
		}
	}
	// Direct, no base domain: staging, offered production.
	status(nil, false, []string{
		"no wildcard URLs", "sites le-http01 by default", "staging, test certificates", "$ devopsy letsencrypt production",
		"proxies       none", "$ devopsy domains",
	}, "tunnel", "acme-dns", "forget")
	// A wildcard through Cloudflare, on production, acme-dns, a proxy and
	// a forget queued.
	h.sh(dir, nil, `mkdir -p .devopsy/mnt/proxies && printf 'old.example.com\n' >.devopsy/mnt/forget.pending &&
		printf '# proxies\ncloudflare CF-Connecting-IP 192.0.2.0/24\n' >.devopsy/mnt/proxies/proxies.conf`)
	status(map[string]string{
		"PROXY_BASE_DOMAIN": "vm1.example.com", "PROXY_CLOUDFLARE_DNS_API_TOKEN": "x",
		"PROXY_LE_ACME_CASERVER": "https://acme-v02.api.letsencrypt.org/directory",
		"COMPOSE_PROFILES":       "acmedns", "PROXY_ACMEDNS_DOMAIN": "acme.vm1.example.com",
	}, false, []string{
		"<project>-<environment>.vm1.example.com", "one *.vm1.example.com through le-dns01-selfhosted", "letsencrypt   production",
		"acme.vm1.example.com", "$ devopsy acmedns", "proxies       cloudflare", "queued for the next deploy or restart: old.example.com",
	}, "staging")
	h.sh(dir, nil, `rm .devopsy/mnt/forget.pending .devopsy/mnt/proxies/proxies.conf`)
	// Through a tunnel only: no Let's Encrypt, and a warning until a
	// connector is connected.
	tunnel := map[string]string{"PROXY_TUNNEL_ONLY": "true", "PROXY_WILDCARD_CERTRESOLVER": "none"}
	status(tunnel, true, []string{"at Cloudflare's edge", "no connector: release devopsy-template-cloudflared"}, "letsencrypt")
	t.Cleanup(func() { _, _ = h.shErr("/", nil, `docker rm -f status-connector; docker network rm tunnel`) })
	h.sh("/", nil, `docker network create tunnel >/dev/null &&
		docker run -d --name status-connector --network tunnel -l com.docker.compose.project=cloudflared-own-prod -l com.docker.compose.service=cloudflared \
			--health-cmd true --health-interval 1s busybox:1.37 sleep 100000 >/dev/null`)
	h.waitFor("the connector healthy", func() bool {
		out, _ := h.shErr("/", nil, `docker inspect -f '{{.State.Health.Status}}' status-connector`)
		return strings.TrimSpace(out) == "healthy"
	})
	status(tunnel, false, []string{"cloudflared-own-prod cloudflared, healthy"}, "no connector")
}

// env_set writes through .env, a link to shared/.env on a server, and
// replaces a key's line in place.
func TestEnvSet(t *testing.T) {
	h, dir := project(t, "envset", nil)
	h.sh(dir, nil, `mv .devopsy/.env shared.env && ln -s ../shared.env .devopsy/.env`)
	h.sh(dir, nil, `devopsy t 'env_set PROXY_DOCKER_GID 990; env_set PROXY_DOCKER_GID 991; env_set PROXY_ACMEDNS_IP 192.0.2.1'`)
	if out := h.sh(dir, nil, `test -L .devopsy/.env && cat shared.env`); out != "PROXY_LE_ACME_EMAIL=admin@example.com\nPROXY_PUBLIC_IP=203.0.113.10\nPROXY_DOCKER_GID=991\nPROXY_ACMEDNS_IP=192.0.2.1\n" {
		t.Fatalf("shared.env:\n%s", out)
	}
}

// write_if_changed says whether it wrote.
func TestWriteIfChanged(t *testing.T) {
	h, dir := project(t, "write", nil)
	out := h.sh(dir, nil, `devopsy t 'f=$dir/mnt/x; mkdir -p "$dir/mnt"
		write_if_changed "$f" a && echo wrote; write_if_changed "$f" a || echo same; write_if_changed "$f" b && echo wrote; cat "$f"'`)
	if out != "wrote\nsame\nwrote\nb" {
		t.Fatalf("got %q", out)
	}
	// A dynamic file's name is a link that stays put through a rewrite, since a
	// listing during a rename over a name can miss it; a regular file left
	// there becomes the link, and nothing stays after its removal.
	out = h.sh(dir, nil, `devopsy t 'd=$dir/mnt/dynamic; mkdir -p "$d"; f=$d/y.yaml; echo old >"$f"
		write_dynamic "$f" a && echo wrote; readlink "$f"; i=$(stat -c %i "$f")
		write_dynamic "$f" a || echo same
		write_dynamic "$f" b && echo wrote; [ "$(stat -c %i "$f")" = "$i" ] && echo kept; cat "$f"
		remove_dynamic "$f"; ls -A "$d"'`)
	if out != "wrote\ny.live\nsame\nwrote\nkept\nb" {
		t.Errorf("a dynamic file: %q", out)
	}
}

// static_env writes Traefik's static settings from what is enabled: the
// entrypoints' middlewares and trust, the plugins, the default
// entrypoints. Without its restart (--no-restart), so nothing starts.
func TestStaticEnv(t *testing.T) {
	h, dir := project(t, "staticenv", nil)
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "docker network rm tunnel") })
	const head = "# Written by 'devopsy deploy' and 'devopsy proxies': Traefik's static settings.\n"
	const hold = "TRAEFIK_EXPERIMENTAL_LOCALPLUGINS_HOLD_MODULENAME=github.com/hanoii/devopsy-traefik/hold\n"
	const proxies = "TRAEFIK_EXPERIMENTAL_LOCALPLUGINS_PROXIES_MODULENAME=github.com/hanoii/devopsy-traefik/proxies\n"
	const both = "TRAEFIK_ENTRYPOINTS_WEB_ASDEFAULT=true\nTRAEFIK_ENTRYPOINTS_WEBSECURE_ASDEFAULT=true\n"
	// The hosts' daemon.json pools are private ranges: nothing beyond them.
	const noProxy = "PROXY_NO_PROXY=10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,acmedns.staticenv_acmedns,dockerproxy.staticenv_dockerproxy,fd00::/8\n"
	for _, c := range []struct {
		name  string
		setup string
		env   map[string]string
		want  string
	}{
		{"nothing", "", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file\n" + hold + both + noProxy},
		{"tunnel only", "", map[string]string{"PROXY_TUNNEL_ONLY": "true"}, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file\n" + hold +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_ASDEFAULT=true\n" + noProxy},
		{"proxies", `mkdir -p .devopsy/mnt/proxies && echo 'x' >.devopsy/mnt/dynamic/proxies.yaml &&
			echo 'cloudflare' >.devopsy/mnt/proxies/proxies.conf && echo '198.51.100.0/24' >.devopsy/mnt/proxies/forwarded-ips`, nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_FORWARDEDHEADERS_TRUSTEDIPS=198.51.100.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file,proxies@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file\n" + proxies + hold + both + noProxy},
		{"proxies commented out", `mkdir -p .devopsy/mnt/proxies && echo 'x' >.devopsy/mnt/dynamic/proxies.yaml &&
			printf '# cloudflare\n\n' >.devopsy/mnt/proxies/proxies.conf`, nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file\n" + hold + both + noProxy},
		{"tunnel", "docker network create --subnet 10.99.0.0/24 tunnel >/dev/null", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=10.99.0.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file,tunnel@file\n" + proxies + hold + both + noProxy},
		// Made again on another subnet: the trust follows it.
		{"tunnel again", "docker network rm tunnel >/dev/null && docker network create --subnet 10.98.0.0/24 tunnel >/dev/null", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=10.98.0.0/24\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file,tunnel@file\n" + proxies + hold + both + noProxy},
		// A connector may reach Traefik over IPv6: the allow list and the trust have it too.
		{"tunnel on IPv6", "docker network rm tunnel >/dev/null && docker network create --ipv6 --subnet 10.97.0.0/24 --subnet fd00:97::/64 tunnel >/dev/null", nil, head +
			"TRAEFIK_ENTRYPOINTS_WEBSECURE_HTTP_MIDDLEWARES=forwarded@file\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=10.97.0.0/24,fd00:97::/64\n" +
			"TRAEFIK_ENTRYPOINTS_TUNNEL_HTTP_MIDDLEWARES=tunnel-allow@file,forwarded@file,tunnel@file\n" + proxies + hold + both + noProxy},
	} {
		h.sh(dir, nil, `rm -rf .devopsy/mnt && mkdir -p .devopsy/mnt/dynamic`)
		if c.setup != "" {
			h.sh(dir, nil, c.setup)
		}
		h.sh(dir, c.env, `devopsy t 'static_env --no-restart'`)
		if got := h.sh(dir, nil, "cat .devopsy/mnt/traefik.env"); got != c.want {
			t.Errorf("%s: traefik.env:\n%s\nwant:\n%s", c.name, got, c.want)
		}
		if got := h.sh(dir, nil, "cat .devopsy/mnt/dynamic/forwarded.yaml"); !strings.Contains(got, "Forwarded: \"\"") {
			t.Errorf("%s: forwarded.yaml:\n%s", c.name, got)
		}
		tunnel, _ := h.shErr(dir, nil, "cat .devopsy/mnt/dynamic/tunnel.yaml")
		if want := strings.HasPrefix(c.name, "tunnel") && c.name != "tunnel only"; want != strings.Contains(tunnel, "CF-Connecting-IP") {
			t.Errorf("%s: tunnel.yaml:\n%s", c.name, tunnel)
		}
		// Its loopback always, for the hold's probes; its subnet once there is one.
		allow := []string{}
		for _, ip := range regexp.MustCompile(`(?m)^ +- (\S+)$`).FindAllStringSubmatch(h.sh(dir, nil, "cat .devopsy/mnt/dynamic/tunnel-allow.yaml"), -1) {
			allow = append(allow, ip[1])
		}
		want := "127.0.0.1/32"
		if m := regexp.MustCompile(`TUNNEL_FORWARDEDHEADERS_TRUSTEDIPS=(.*)`).FindStringSubmatch(c.want); m != nil {
			want += "," + m[1]
		}
		if got := strings.Join(allow, ","); got != want {
			t.Errorf("%s: tunnel-allow.yaml has %s, want %s", c.name, got, want)
		}
	}

	// A pool outside the private ranges joins them, once.
	h.sh(dir, nil, `rm -rf .devopsy/mnt && mkdir -p .devopsy/mnt/dynamic`)
	h.sh(dir, nil, `devopsy t 'docker() { case $1 in info) echo "100.64.0.0/16 172.16.0.0/12 " ;; *) command docker "$@" ;; esac; }; static_env --no-restart'`)
	if got, want := h.sh(dir, nil, "grep ^PROXY_NO_PROXY= .devopsy/mnt/traefik.env"),
		"PROXY_NO_PROXY=10.0.0.0/8,100.64.0.0/16,172.16.0.0/12,192.168.0.0/16,acmedns.staticenv_acmedns,dockerproxy.staticenv_dockerproxy,fd00::/8\n"; got != want {
		t.Errorf("a pool of its own: %s, want %s", got, want)
	}

	// Docker failing (not a missing network) fails it, and the trust it had
	// stays: never a tunnel entrypoint trusting nothing, its visitors' IPs
	// lost.
	before := h.sh(dir, nil, "cat .devopsy/mnt/traefik.env")
	out, err := h.shErr(dir, nil, `devopsy t 'export DOCKER_HOST=unix:///nowhere.sock; static_env --no-restart'`)
	if err == nil || !strings.Contains(out, "could not read the tunnel network") {
		t.Errorf("Docker failing: %v: %s", err, out)
	}
	if after := h.sh(dir, nil, "cat .devopsy/mnt/traefik.env"); after != before {
		t.Errorf("Docker failing changed traefik.env:\n%s", after)
	}
}

// forget takes a domain and its wildcard out of every resolver's store
// and the acme-dns registrations, keeps the rest, the files' mode and a
// copy of each file it changed.
func TestForget(t *testing.T) {
	h, dir := project(t, "forget", nil)
	cert := func(main string, sans ...string) string {
		d := fmt.Sprintf(`{"main":%q}`, main)
		if len(sans) > 0 {
			d = fmt.Sprintf(`{"main":%q,"sans":["%s"]}`, main, strings.Join(sans, `","`))
		}
		return fmt.Sprintf(`{"domain":%s,"certificate":"c","key":"k","Store":"default"}`, d)
	}
	stores := map[string]string{
		"acme-le-http01.json":           `{"le-http01":{"Account":{"Email":"admin@example.com"},"Certificates":[` + cert("old.example.com", "www.old.example.com") + `,` + cert("keep.example.com") + `]}}`,
		"acme-le-dns01-selfhosted.json": `{"le-dns01-selfhosted":{"Account":null,"Certificates":[` + cert("*.old.example.com") + `]}}`,
		"acme-dns-accounts.json":        `{"old.example.com":{"subdomain":"0a1b-2c3d","fulldomain":"0a1b-2c3d.acme.example.com"},"keep.example.com":{"subdomain":"4e5f","fulldomain":"4e5f.acme.example.com"}}`,
	}
	for f, content := range stores {
		h.sh(dir, nil, `devopsy t 'in_traefik "umask 077; cat >/letsencrypt/$1" "$1"' "$1" <<EOF
$2
EOF`, f, content)
	}

	if out := h.sh(dir, nil, "devopsy forget"); !strings.Contains(out, "  old.example.com, www.old.example.com  certificate, le-http01") ||
		!strings.Contains(out, "  *.old.example.com  certificate, le-dns01-selfhosted") || !strings.Contains(out, "  old.example.com  acme-dns registration") {
		t.Fatalf("forget lists:\n%s", out)
	}
	if out, err := h.shErr(dir, nil, "devopsy forget 'bad_name.example.com'"); err == nil || !strings.Contains(out, "not a domain name") {
		t.Errorf("a bad name: %v %s", err, out)
	}
	h.sh(dir, nil, "devopsy forget '*.old.example.com'")
	out := h.sh(dir, nil, "devopsy t forget_apply")
	for _, want := range []string{
		"forgot the certificate for old.example.com, www.old.example.com (le-http01)",
		"forgot the certificate for *.old.example.com (le-dns01-selfhosted)",
		"forgot the acme-dns registration of old.example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	after := h.sh(dir, nil, `devopsy t 'in_traefik "cd /letsencrypt && stat -c \"%a %n\" acme-* | sort && cat acme-le-http01.json acme-le-dns01-selfhosted.json acme-dns-accounts.json && ls | grep -c forget-"'`)
	for _, want := range []string{"600 acme-dns-accounts.json\n", "600 acme-le-http01.json\n", "600 acme-le-dns01-selfhosted.json\n", `"keep.example.com"`, `"4e5f"`, "\n3\n"} {
		if !strings.Contains(after, want) {
			t.Errorf("want %q in the stores:\n%s", want, after)
		}
	}
	if strings.Contains(after, "old.example.com") || strings.Contains(after, "0a1b") {
		t.Errorf("old.example.com still stored:\n%s", after)
	}
	if _, err := h.shErr(dir, nil, "test -e .devopsy/mnt/forget.pending"); err == nil {
		t.Error("the queue stayed")
	}
}
