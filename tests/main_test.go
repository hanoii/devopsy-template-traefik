//go:build e2e

package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// image is the host's image (Dockerfile here).
const image = "devopsy-traefik-tests:1"

// label marks every container and network of a run with its process id,
// so tearDown finds them all.
const label = "devopsy-traefik-tests"

// The run's containers and network: names end with its process id, so
// two runs never meet.
var (
	network string
	host    string
	mirror  string
)

// src is the template as the host has it, pristine: projects copy it.
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

// setUp builds the image, starts the mirror and the host, installs
// devopsy there and copies the template in.
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
	network, host, mirror = label+"-"+id, label+"-host-"+id, label+"-mirror-"+id
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
	if out, err := docker("run", "-d", "--privileged", "--name", host, "--hostname", "host", "--label", run,
		"--network", network, "-v", "/var/lib/docker", image); err != nil {
		return fmt.Errorf("starting the host: %v\n%s", err, out)
	}
	for end := time.Now().Add(time.Minute); ; time.Sleep(250 * time.Millisecond) {
		if _, err := docker("exec", host, "docker", "version"); err == nil {
			break
		}
		if time.Now().After(end) {
			out, _ := docker("exec", host, "tail", "-20", "/var/log/dockerd.log")
			return fmt.Errorf("the host's dockerd did not start:\n%s", out)
		}
	}
	if err := installDevopsy(); err != nil {
		return err
	}
	return copyTemplate(filepath.Dir(dir))
}

// installDevopsy puts DEVOPSY_BIN on the host, else the latest release.
func installDevopsy() error {
	if bin := os.Getenv("DEVOPSY_BIN"); bin != "" {
		if out, err := docker("cp", bin, host+":/usr/local/bin/devopsy"); err != nil {
			return fmt.Errorf("copying %s: %v\n%s", bin, err, out)
		}
	} else if out, err := docker("exec", host, "sh", "-c",
		"curl -fsSL https://raw.githubusercontent.com/hanoii/devopsy-cli/main/install.sh | DEVOPSY_INSTALL_DIR=/usr/local/bin sh"); err != nil {
		return fmt.Errorf("installing devopsy: %v\n%s", err, out)
	}
	out, err := docker("exec", host, "devopsy", "--version")
	if err != nil {
		return fmt.Errorf("devopsy on the host: %v\n%s", err, out)
	}
	fmt.Fprintf(os.Stderr, "tests: %s", out)
	return nil
}

// copyTemplate copies the checkout's .devopsy/ to src on the host: what
// git has or would add (tracked and untracked, never ignored: no .env,
// no mnt), as the working tree has it.
func copyTemplate(root string) error {
	cmd := exec.Command("sh", "-c", `git ls-files -z --cached --others --exclude-standard .devopsy | xargs -0 tar -cf - |
		docker exec -i "$1" sh -c 'mkdir -p "$2" && tar -xf - -C "$2" && chown -R devopsy:devopsy "$2"' sh "$1" "$2"`, "sh", host, src)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying the template: %v\n%s", err, out)
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

// project makes a copy of the template in the deploy user's home, named
// name (its compose project too, through config.local.yaml), with
// settings and env as its .env. It returns its directory.
func project(t *testing.T, name string, env map[string]string) string {
	t.Helper()
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
	sh(t, "/home/devopsy", nil, `rm -rf "$1" && mkdir -p "$1" && cp -a /src/.devopsy "$1/" &&
		printf 'project: %s\n' "$2" >"$1/.devopsy/config.local.yaml" && printf '%s\n' "$3" >"$1/.devopsy/.env" &&
		printf '%s\n' "$4" >"$1/.devopsy/commands/t" && chmod 755 "$1/.devopsy/commands/t"`,
		dir, name, strings.Join(dotenv, "\n"), fnCommand)
	return dir
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

// sh runs script as the deploy user in dir on the host, with env added
// (the caller's environment, which wins over .env), and fails the test
// when it fails. It returns stdout and stderr together.
func sh(t *testing.T, dir string, env map[string]string, script string, args ...string) string {
	t.Helper()
	out, err := shErr(dir, env, script, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

// shErr is sh returning the error instead.
func shErr(dir string, env map[string]string, script string, args ...string) (string, error) {
	cmd := []string{"exec", "-u", "devopsy", "-w", dir}
	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd = append(cmd, "-e", k+"="+env[k])
	}
	cmd = append(cmd, host, "sh", "-c", script, "sh")
	return docker(append(cmd, args...)...)
}
