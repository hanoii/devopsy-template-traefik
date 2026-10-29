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
// two runs never meet. Two hosts, each its own Docker: the template's
// networks have fixed names (traefik, tunnel), so the script tests, which
// make and remove them, never share a Docker with the Traefik the
// scenarios deploy.
var (
	network string
	mirror  string
	// scripts runs the script tests: nothing deploys Traefik there.
	scripts string
	// server runs the scenarios' Traefik (traefik()) and their sites.
	server string
)

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
	scripts, server = label+"-scripts-"+id, label+"-server-"+id
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
	errs := make([]error, 2)
	for i, h := range []string{scripts, server} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = startHost(h, run, filepath.Dir(dir))
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// startHost starts host h, waits for its dockerd, installs devopsy and
// copies the template from root.
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
	if err := installDevopsy(h); err != nil {
		return err
	}
	return copyTemplate(h, root)
}

// installDevopsy puts DEVOPSY_BIN on host h, else the latest release.
func installDevopsy(h string) error {
	if bin := os.Getenv("DEVOPSY_BIN"); bin != "" {
		if out, err := docker("cp", bin, h+":/usr/local/bin/devopsy"); err != nil {
			return fmt.Errorf("copying %s: %v\n%s", bin, err, out)
		}
	} else if out, err := docker("exec", h, "sh", "-c",
		"curl -fsSL https://raw.githubusercontent.com/hanoii/devopsy-cli/main/install.sh | DEVOPSY_INSTALL_DIR=/usr/local/bin sh"); err != nil {
		return fmt.Errorf("installing devopsy: %v\n%s", err, out)
	}
	out, err := docker("exec", h, "devopsy", "--version")
	if err != nil {
		return fmt.Errorf("devopsy on %s: %v\n%s", h, err, out)
	}
	if h == server {
		fmt.Fprintf(os.Stderr, "tests: %s", out)
	}
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

// The hosts run at once, each one test at a time: tests on one host share
// its Docker (the fixed networks, the ports). A test takes its host's lock
// first (project, traefik), parallel to the other host's.
var scriptsMu, serverMu sync.Mutex

// one runs the rest of t alone on its host, beside the other host's.
func one(t *testing.T, mu *sync.Mutex) {
	t.Helper()
	t.Parallel()
	mu.Lock()
	t.Cleanup(mu.Unlock)
}

// project makes a copy of the template on the scripts host (newProject),
// taken down when the test ends: what it started, its networks. The
// test's first call: it takes the host (one).
func project(t *testing.T, name string, env map[string]string) string {
	t.Helper()
	one(t, &scriptsMu)
	dir, err := newProject(scripts, name, env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = shErrOn(scripts, dir, nil, "devopsy down -v --remove-orphans") })
	return dir
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

// sh runs script on the scripts host (shOn).
func sh(t *testing.T, dir string, env map[string]string, script string, args ...string) string {
	t.Helper()
	return shOn(t, scripts, dir, env, script, args...)
}

// shErr is sh returning the error instead.
func shErr(dir string, env map[string]string, script string, args ...string) (string, error) {
	return shErrOn(scripts, dir, env, script, args...)
}

// shOn runs script as the deploy user in dir on host h, with env added
// (the caller's environment, which wins over .env), and fails the test
// when it fails. It returns stdout and stderr together.
func shOn(t *testing.T, h, dir string, env map[string]string, script string, args ...string) string {
	t.Helper()
	out, err := shErrOn(h, dir, env, script, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

// shErrOn is shOn returning the error instead.
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
