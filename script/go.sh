#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=go1.27.1
export GOMODCACHE="$PWD/.cache/gomod"
export GOCACHE="$PWD/.cache/go-build"
export GOPATH="$PWD/.cache/go"
export GOFLAGS=-buildvcs=false
export CGO_ENABLED=0
exec go "$@"
