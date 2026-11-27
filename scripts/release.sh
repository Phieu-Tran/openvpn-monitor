#!/usr/bin/env bash
# Build the release images and the per-host folders into dist/:
#   dist/ovpn-agent-<v>.image.tar.gz   docker image (docker load -i ...)
#   dist/ovpn-agent/                   docker-compose.yml + .env + data/   -> OpenVPN host
#   dist/ovpn-dash-<v>.image.tar.gz
#   dist/ovpn-dash/                    docker-compose.yml + .env + data/   -> monitor host
# Both files come from deploy/<app>/ (your real ones, not in git), or from the .example
# files when there are none (then fill .env in on the host).
# Usage: scripts/release.sh 0.1.0
set -euo pipefail
v=${1:?usage: scripts/release.sh <version>, e.g. 0.1.0}
cd "$(dirname "$0")/.."

if command -v go >/dev/null; then
  go vet ./...
  go test ./...
fi

rm -rf dist
mkdir -p dist
for app in agent dash; do
  img="vihat/ovpn-$app:$v"
  out="dist/ovpn-$app"
  docker build --build-arg "APP=ovpn-$app" --build-arg "VERSION=$v" -t "$img" .
  docker save "$img" | gzip -n > "dist/ovpn-$app-$v.image.tar.gz"
  mkdir -p "$out/data"
  src="deploy/$app/docker-compose.yml"
  [ -f "$src" ] || src="deploy/$app/docker-compose.example.yml"
  sed "s#image: vihat/ovpn-$app:.*#image: $img#" "$src" > "$out/docker-compose.yml"
  env="deploy/$app/.env"
  [ -f "$env" ] || env="deploy/$app/.env.example"
  cp "$env" "$out/.env"
done
(cd dist && sha256sum ./*.tar.gz > SHA256SUMS)
echo
ls -l dist dist/ovpn-agent dist/ovpn-dash
