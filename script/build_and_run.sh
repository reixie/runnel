#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
mode="${1:-run}"
case "$mode" in run|--verify|--logs|--debug) ;; *) echo "usage: $0 [--verify|--logs|--debug]" >&2; exit 2 ;; esac
# Build first so the role-aware controller exists even on a Stage 1 checkout.
./script/build.sh
arch="$(./script/go.sh env GOARCH)"
bundle="$PWD/dist/darwin-$arch/Runnel.app"
"$bundle/Contents/MacOS/Runnel" --internal-stop-gui
if [[ "$mode" == --debug ]]; then exec lldb -- "$bundle/Contents/MacOS/Runnel"; fi
launch=(/usr/bin/open -n)
# Internal verification override: carry it through LaunchServices as well as
# the role-aware controller, so tests never touch the real user endpoint.
if [[ -n "${RUNNEL_INTERNAL_RUNTIME:-}" ]]; then
    launch+=(--env "RUNNEL_INTERNAL_RUNTIME=$RUNNEL_INTERNAL_RUNTIME")
fi
"${launch[@]}" "$bundle"
if [[ "$mode" == --verify ]]; then
    for ((i=0; i<50; i++)); do
        if "$bundle/Contents/MacOS/Runnel" --internal-gui-running; then exit 0; fi
        sleep 0.1
    done
    echo "Runnel did not launch" >&2; exit 1
elif [[ "$mode" == --logs ]]; then
    exec /usr/bin/log stream --info --style compact --predicate 'process == "Runnel"'
fi
