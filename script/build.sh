#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
./script/go.sh build -o .cache/bin/mygo github.com/egoist/mygo/cmd/mygo
# Keep the CLI's native downloads in the project, too. This HOME applies only
# to the builder; Runnel is launched separately with the user's real account.
mkdir -p .cache/build-home
export GOTOOLCHAIN=go1.27.1
export GOFLAGS=-buildvcs=false
export CGO_ENABLED=0
export GOMODCACHE="$PWD/.cache/gomod" GOCACHE="$PWD/.cache/go-build" GOPATH="$PWD/.cache/go"
HOME="$PWD/.cache/build-home" .cache/bin/mygo build -skip-dmg -skip-notarize "$@"
