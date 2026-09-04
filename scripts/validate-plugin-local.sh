#!/usr/bin/env bash
set -euo pipefail

export npm_config_cache="${TMPDIR:-/tmp}/agent-ai-app-npm-cache"

npm run clean:review-artifacts
npm ci --ignore-scripts

tracked_artifacts="$(git ls-files \
  'dist/*' \
  '*.zip' \
  '*.tar' \
  '*.tar.gz' \
  '*.tgz' \
  '*.rar' \
  '*.7z' \
  '*.exe' \
  '*.dll' \
  '*.so' \
  '*.dylib' \
  '*.apk' \
  '*.deb' \
  '*.rpm' \
  '*.msi' \
  '*.jar' \
  '*.war' \
  '*.class' \
  '*.wasm')"
if [ -n "$tracked_artifacts" ]; then
  echo "Do not commit generated archives, dist output, or binary artifacts:"
  echo "$tracked_artifacts"
  exit 1
fi

sensitive_files="$(git ls-files | grep -Ei '(^|/)(\.env($|\.)|id_rsa|id_dsa|id_ecdsa|id_ed25519|.*\.(pem|key|p12|pfx|crt|cer))$' | grep -Ev '(^|/)\.env\.example$' || true)"
if [ -n "$sensitive_files" ]; then
  echo "Sensitive-looking files must not be committed:"
  echo "$sensitive_files"
  exit 1
fi

bad_urls="$(jq -r '.. | objects | .resolved? // empty' package-lock.json | grep -Ev '^https://registry\.npmjs\.org/' || true)"
if [ -n "$bad_urls" ]; then
  echo "package-lock.json contains package tarballs outside registry.npmjs.org:"
  echo "$bad_urls"
  exit 1
fi

gofmt_files="$(gofmt -l Magefile.go pkg)"
if [ -n "$gofmt_files" ]; then
  echo "Go files are not formatted:"
  echo "$gofmt_files"
  exit 1
fi

go vet ./pkg/...
go mod verify
go test -v -race -count=1 ./pkg/...
go run github.com/securego/gosec/v2/cmd/gosec@latest ./pkg/...
go run golang.org/x/vuln/cmd/govulncheck@latest ./pkg/...

osv_dir="$(mktemp -d)"
cp go.mod go.sum "$osv_dir/"
if ! docker run --rm -v "${osv_dir}:/src:ro,Z" ghcr.io/google/osv-scanner@sha256:8108ae94eadea5a02c9bec6e646909d5b790b44bd62d7f5b7f0b1d6d0ffc7734 scan --format vertical -L /src/go.mod; then
  docker run --rm -v "${osv_dir}:/src:ro" ghcr.io/google/osv-scanner@sha256:8108ae94eadea5a02c9bec6e646909d5b790b44bd62d7f5b7f0b1d6d0ffc7734 scan --format vertical -L /src/go.mod
fi
rm -rf "$osv_dir"

go run github.com/zricethezav/gitleaks/v8@latest detect --source . --no-git --redact --verbose
npm audit --audit-level=moderate
npm audit --omit=dev --audit-level=moderate
npm run lint
npm run typecheck
npm run test:ci
npm run build

go run github.com/magefile/mage@v1.17.2 -v
chmod 0755 dist/gpx_*

if grep -R "__SECRET_INTERNALS" dist/*.js dist/*.map 2>/dev/null; then
  echo "Bundle contains React private internals and may break on Grafana React 19."
  exit 1
fi

src_dependency="$(jq -r '.dependencies.grafanaDependency' src/plugin.json)"
dist_dependency="$(jq -r '.dependencies.grafanaDependency' dist/plugin.json)"
if [ "$src_dependency" != "$dist_dependency" ]; then
  echo "dist/plugin.json does not match src/plugin.json grafanaDependency."
  echo "src:  $src_dependency"
  echo "dist: $dist_dependency"
  exit 1
fi

if [ -f dist/go_plugin_build_manifest ] && grep -Eq ':(\.cache|node_modules)/' dist/go_plugin_build_manifest; then
  echo "dist/go_plugin_build_manifest contains local cache or dependency paths:"
  grep -E ':(\.cache|node_modules)/' dist/go_plugin_build_manifest
  exit 1
fi

plugin_id="$(jq -r '.id' src/plugin.json | tr -cd 'a-zA-Z0-9._-')"
version="$(jq -r '.info.version' src/plugin.json)"
archive_name="${plugin_id}-${version}-local.zip"
package_dir="$(mktemp -d)"
trap 'rm -rf "$package_dir"' EXIT

mkdir -p "${package_dir}/${plugin_id}"
cp -R dist/. "${package_dir}/${plugin_id}/"
(cd "$package_dir" && zip -qr "${OLDPWD}/${archive_name}" "$plugin_id")

npx --cache "$npm_config_cache" -y @grafana/plugin-validator@latest -jsonOutput "$archive_name"
