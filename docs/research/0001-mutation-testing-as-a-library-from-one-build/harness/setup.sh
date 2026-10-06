#!/usr/bin/env bash
# Usage: setup.sh
# Prepares the work directory: writable copies of the measured packages at
# their released versions, the three Go mutation tools that run as binaries
# at the versions the research ran, and the prototype's measure and count
# commands. ooze is a library, so head2head.sh adds it to each copy. bitset
# and go-cmp serve only the probes.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd -P)"
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
mkdir -p "$W/targets" "$W/bin" "$W/tmp"
export GOFLAGS= GOWORK=off GOBIN="$W/bin"
for mod in github.com/dustin/go-humanize@v1.1.0 github.com/google/btree@v1.1.3 github.com/shopspring/decimal@v1.4.0 github.com/bits-and-blooms/bitset@v1.25.0 github.com/google/go-cmp@v0.7.0; do
  dir=$(go mod download -json "$mod" | python3 -c 'import json, sys; print(json.load(sys.stdin)["Dir"])')
  path=${mod%@*}
  owner_repo=${path#github.com/}
  name="${owner_repo/\//_}@${mod##*@}"
  rm -rf "$W/targets/$name"
  cp -r "$dir" "$W/targets/$name"
  chmod -R u+w "$W/targets/$name"
  echo "target $name"
done
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
go install github.com/fchimpan/mutest@v0.6.2
go install github.com/devenjarvis/kanly/cmd/kanly@v0.1.0
go -C "$HERE/../gomut" build -o "$W/bin/" ./cmd/...
ls "$W/bin"
