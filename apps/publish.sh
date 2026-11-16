#!/usr/bin/env bash
# Publishes one clunck app as a housepage site of the same name: copies apps/<name>/ and the shared kit
# (apps/shared/clunck-app.js) into a temporary folder, then runs housepage publish on it.
#   apps/publish.sh <name>          e.g. clunck, snake, todo
# Uses ../bin/housepage unless HOUSEPAGE points at another binary.
set -euo pipefail
cd "$(dirname "$0")"
name=${1:?usage: apps/publish.sh <name>}
[ "$name" != shared ] && [ -f "$name/index.html" ] || { echo "error: no app at apps/$name/" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp -R "$name"/. "$tmp"/
cp shared/clunck-app.js "$tmp"/
"${HOUSEPAGE:-../bin/housepage}" publish --name "$name" "$tmp"
