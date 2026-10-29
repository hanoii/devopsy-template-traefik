// Package tests runs the template on a real devopsy and a real Docker:
// one privileged Debian container per run (Dockerfile here) with its own
// dockerd, sshd and the deploy user, devopsy installed in it and the
// template's .devopsy/ copied in. Behind the e2e build tag, since every
// test needs Docker:
//
//	go test -tags e2e -count=1 -v ./tests
//
// devopsy is the latest release (install.sh), what the template runs
// against; DEVOPSY_BIN names a linux build of another one instead, for a
// devopsy change and the template together. The container pulls through
// a mirror of Docker Hub whose cache, the volume
// devopsy-traefik-tests-mirror, outlives the run. TESTS_KEEP=1 leaves
// the containers for a look afterwards, labeled devopsy-traefik-tests=<the
// run's process id>.
package tests
