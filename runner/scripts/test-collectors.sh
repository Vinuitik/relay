#!/usr/bin/env bash
# Runs the collectors tests, including the REAL bwrap sandbox tests, in a
# golang container with bubblewrap + python3 installed. Docker's default
# seccomp/AppArmor profiles and masked /proc paths block what bwrap needs, so
# this container runs with them relaxed (test only - nothing is exposed).
#
#   runner/scripts/test-collectors.sh            # all collectors + api tests
#   runner/scripts/test-collectors.sh -run Real  # extra go test args
#
# On a Linux host with Go, bwrap and python3, plain `go test ./internal/collectors/...`
# runs the same real-sandbox tests (they skip when bwrap can't run).
set -euo pipefail
cd "$(dirname "$0")/../.."
docker run --rm \
  --security-opt seccomp=unconfined --security-opt apparmor=unconfined --security-opt systempaths=unconfined \
  -v "$PWD":/app -v relay-gomod:/go/pkg/mod -v relay-gocache:/root/.cache/go-build \
  -w /app/runner golang:1.22 sh -c '
    (apt-get update -qq && apt-get install -y -qq bubblewrap python3 >/dev/null) || exit 1
    useradd -m tester 2>/dev/null || true
    chmod -R a+rwX /go /root/.cache 2>/dev/null; chmod a+x /root
    # as root AND as an unprivileged user (the real runner is never root)
    go test -count=1 "$@" ./internal/collectors/... ./internal/api/ &&
    su tester -c "HOME=/home/tester GOCACHE=/root/.cache/go-build GOPATH=/go PATH=$PATH go test -count=1 -v -run Real $* ./internal/collectors/..."
  ' sh "$@"
