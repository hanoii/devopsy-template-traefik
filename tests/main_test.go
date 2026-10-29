//go:build e2e

package tests

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// image is the hosts' image (Dockerfile here).
const image = "devopsy-traefik-tests:1"

// label marks every container and network of a run with its process id,
// so tearDown finds them all.
const label = "devopsy-traefik-tests"

// The run's containers and network: names end with its process id, so
// two runs never meet. Hosts, each its own Docker, taken by one test at a
// time: the template's networks have fixed names (traefik, tunnel) and
// its ports are the host's. The script tests, which make and remove the
// networks or deploy a Traefik of their own, run on scripts hosts, where
// nothing stays deployed; the scenarios on server hosts, each with a
// Traefik of its own (traefik()). TESTS_LANES of each (2 scripts hosts, 3
// server hosts by default) run at once.
var (
	network string
	mirror  string
	scripts chan string
	servers chan string
)

// lanes is TESTS_LANES, else 3: the server hosts, and the scripts hosts
// but one.
func lanes() int {
	if n, err := strconv.Atoi(os.Getenv("TESTS_LANES")); err == nil && n > 0 {
		return n
	}
	return 3
}

// src is the template as each host has it, pristine: projects copy it.
const src = "/src"

func TestMain(m *testing.M) {
	code := 1
	err := setUp()
	if err == nil {
		code = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, "tests: "+err.Error())
	}
	if os.Getenv("TESTS_KEEP") == "" {
		tearDown()
	} else {
		fmt.Fprintf(os.Stderr, "tests: kept the containers labeled %s=%d\n", label, os.Getpid())
	}
	os.Exit(code)
}

// setUp builds the image, starts the mirror and the hosts, installs
// devopsy on them and copies the template in.
func setUp() error {
	if _, err := docker("version"); err != nil {
		return fmt.Errorf("docker is needed: %v", err)
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	if out, err := docker("build", "-q", "-t", image, dir); err != nil {
		return fmt.Errorf("building %s: %v\n%s", image, err, out)
	}
	id := fmt.Sprint(os.Getpid())
	network, mirror = label+"-"+id, label+"-mirror-"+id
	var hosts, scriptHosts []string
	for i := 1; i <= max(lanes()-1, 1); i++ {
		scriptHosts = append(scriptHosts, fmt.Sprintf("%s-scripts-%s-%d", label, id, i))
	}
	for i := 1; i <= lanes(); i++ {
		hosts = append(hosts, fmt.Sprintf("%s-server-%s-%d", label, id, i))
	}
	hosts = append(scriptHosts, hosts...)
	scripts, servers = make(chan string, len(scriptHosts)), make(chan string, lanes())
	run := label + "=" + id
	if out, err := docker("network", "create", "--label", run, network); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	// A pull-through cache of Docker Hub (daemon.json's registry-mirrors),
	// its cache kept between runs: a run asks Docker Hub only whether
	// tags moved, and its pull limits stop mattering.
	if out, err := docker("run", "-d", "--name", mirror, "--label", run, "--network", network, "--network-alias", "mirror",
		"-v", label+"-mirror:/var/lib/registry", "-e", "REGISTRY_PROXY_REMOTEURL=https://registry-1.docker.io", "registry:3"); err != nil {
		return fmt.Errorf("starting the registry mirror: %v\n%s", err, out)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(hosts))
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = startHost(h, run, filepath.Dir(dir))
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := installDevopsy(hosts); err != nil {
		return err
	}
	for _, h := range hosts {
		if strings.Contains(h, "-scripts-") {
			scripts <- h
		} else {
			servers <- h
		}
	}
	return nil
}

// startHost starts host h, waits for its dockerd and copies the template
// from root.
func startHost(h, run, root string) error {
	if out, err := docker("run", "-d", "--privileged", "--name", h, "--hostname", "host", "--label", run,
		"--network", network, "-v", "/var/lib/docker", image); err != nil {
		return fmt.Errorf("starting %s: %v\n%s", h, err, out)
	}
	for end := time.Now().Add(time.Minute); ; time.Sleep(250 * time.Millisecond) {
		if _, err := docker("exec", h, "docker", "version"); err == nil {
			break
		}
		if time.Now().After(end) {
			out, _ := docker("exec", h, "tail", "-20", "/var/log/dockerd.log")
			return fmt.Errorf("%s's dockerd did not start:\n%s", h, out)
		}
	}
	return copyTemplate(h, root)
}

// installDevopsy puts DEVOPSY_BIN on every host, else the latest
// release, installed on the first and copied to the others.
func installDevopsy(hosts []string) error {
	bin := os.Getenv("DEVOPSY_BIN")
	if bin == "" {
		if out, err := docker("exec", hosts[0], "sh", "-c",
			"curl -fsSL https://raw.githubusercontent.com/hanoii/devopsy-cli/main/install.sh | DEVOPSY_INSTALL_DIR=/usr/local/bin sh"); err != nil {
			return fmt.Errorf("installing devopsy: %v\n%s", err, out)
		}
		tmp, err := os.MkdirTemp("", label)
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		bin = filepath.Join(tmp, "devopsy")
		if out, err := docker("cp", hosts[0]+":/usr/local/bin/devopsy", bin); err != nil {
			return fmt.Errorf("copying devopsy: %v\n%s", err, out)
		}
	}
	for _, h := range hosts {
		if out, err := docker("cp", bin, h+":/usr/local/bin/devopsy"); err != nil {
			return fmt.Errorf("copying %s to %s: %v\n%s", bin, h, err, out)
		}
	}
	out, err := docker("exec", hosts[0], "devopsy", "--version")
	if err != nil {
		return fmt.Errorf("devopsy on %s: %v\n%s", hosts[0], err, out)
	}
	fmt.Fprintf(os.Stderr, "tests: %d hosts, %s", len(hosts), out)
	return nil
}

// copyTemplate copies the checkout's .devopsy/ to src on host h: what git
// has or would add (tracked and untracked, never ignored: no .env, no
// mnt), as the working tree has it.
func copyTemplate(h, root string) error {
	cmd := exec.Command("sh", "-c", `git ls-files -z --cached --others --exclude-standard .devopsy | xargs -0 tar -cf - |
		docker exec -i "$1" sh -c 'mkdir -p "$2" && tar -xf - -C "$2" && chown -R devopsy:devopsy "$2"' sh "$1" "$2"`, "sh", h, src)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying the template to %s: %v\n%s", h, err, out)
	}
	return nil
}

func tearDown() {
	id := fmt.Sprint(os.Getpid())
	if out, _ := docker("ps", "-aq", "--filter", "label="+label+"="+id); strings.TrimSpace(out) != "" {
		_, _ = docker(append([]string{"rm", "-fv"}, strings.Fields(out)...)...)
	}
	_, _ = docker("network", "rm", network)
}

func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return string(out), err
}

// settings is the .env every project starts with: what deploy needs and
// nothing that reaches the network (a public IP of the documentation's
// range, so domains never asks 1.1.1.1).
var settings = map[string]string{
	"PROXY_LE_ACME_EMAIL": "admin@example.com",
	"PROXY_PUBLIC_IP":     "203.0.113.10",
}

// host is the host a test holds, and the test.
type host struct {
	t    *testing.T
	name string
}

// take gives t a free host of pool for the rest of the test, which runs
// parallel to the other hosts' tests.
func take(t *testing.T, pool chan string) host {
	t.Helper()
	t.Parallel()
	h := <-pool
	// Registered first, so it runs last: what the test's other cleanups
	// remove is gone before the next test gets the host.
	t.Cleanup(func() { pool <- h })
	return host{t, h}
}

// project takes a scripts host and makes a copy of the template there
// (newProject), taken down when the test ends: what it started, its
// networks. The test's first call.
func project(t *testing.T, name string, env map[string]string) (host, string) {
	t.Helper()
	h := take(t, scripts)
	dir, err := newProject(h.name, name, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.shErr(dir, nil, "devopsy down -v --remove-orphans") })
	return h, dir
}

// newProject makes a copy of the template in the deploy user's home on
// host h, named name (its compose project too, through
// config.local.yaml), with settings and env as its .env. It returns its
// directory.
func newProject(h, name string, env map[string]string) (string, error) {
	dir := "/home/devopsy/" + name
	lines := map[string]string{}
	for k, v := range settings {
		lines[k] = v
	}
	for k, v := range env {
		lines[k] = v
	}
	var dotenv []string
	for k, v := range lines {
		dotenv = append(dotenv, k+"="+v)
	}
	sort.Strings(dotenv)
	script := `rm -rf "$1" && mkdir -p "$1" && cp -a /src/.devopsy "$1/" &&
		printf 'project: %s\n' "$2" >"$1/.devopsy/config.local.yaml" && printf '%s\n' "$3" >"$1/.devopsy/.env" &&
		printf '%s\n' "$4" >"$1/.devopsy/commands/t" && chmod 755 "$1/.devopsy/commands/t"`
	if out, err := shErrOn(h, "/home/devopsy", nil, script, dir, name, strings.Join(dotenv, "\n"), fnCommand); err != nil {
		return "", fmt.Errorf("making project %s: %v\n%s", name, err, out)
	}
	return dir, nil
}

// fnCommand is the test's own command in every project's copy, never the
// template's: lib/common.sh sourced, then its argument, so a test calls
// the library through devopsy as the template's commands do (devopsy t
// '<shell>' [<arg>...], the args its $1...).
const fnCommand = `#!/bin/sh
## Description: test only: runs its first argument with lib/common.sh sourced
set -eu
. "$DEVOPSY_PROJECT_DIR/lib/common.sh"
script=$1
shift
eval "$script"`

// sh runs script as the deploy user in dir on h, with env added (the
// caller's environment, which wins over .env), and fails the test when it
// fails. It returns stdout and stderr together.
func (h host) sh(dir string, env map[string]string, script string, args ...string) string {
	h.t.Helper()
	out, err := shErrOn(h.name, dir, env, script, args...)
	if err != nil {
		h.t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

// shErr is sh returning the error instead.
func (h host) shErr(dir string, env map[string]string, script string, args ...string) (string, error) {
	return shErrOn(h.name, dir, env, script, args...)
}

// shErrOn runs script as the deploy user in dir on host h.
func shErrOn(h, dir string, env map[string]string, script string, args ...string) (string, error) {
	cmd := []string{"exec", "-u", "devopsy", "-w", dir}
	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd = append(cmd, "-e", k+"="+env[k])
	}
	cmd = append(cmd, h, "sh", "-c", script, "sh")
	return docker(append(cmd, args...)...)
}
