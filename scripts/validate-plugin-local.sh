#!/usr/bin/env bash
set -euo pipefail

repo_root="$(pwd)"
npm_cache_dir="$(mktemp -d)"
osv_dir=""
package_dir=""

cleanup() {
  [ -z "$osv_dir" ] || rm -rf "$osv_dir"
  [ -z "$package_dir" ] || rm -rf "$package_dir"
  rm -rf "$npm_cache_dir"
}
trap cleanup EXIT

export npm_config_cache="$npm_cache_dir"

npm run clean:review-artifacts
npm ci --ignore-scripts --no-audit --no-fund

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

readme_image_refs="$(sed -n 's/.*<img[^>]* src="\([^"]*\)".*/\1/p' README.md)"
if [ -z "$readme_image_refs" ]; then
  echo "README.md must include local image references for GitHub and Grafana plugin details."
  exit 1
fi

while IFS= read -r image_path; do
  [ -z "$image_path" ] && continue
  case "$image_path" in
    src/img/*)
      if [ ! -f "$image_path" ]; then
        echo "README.md references a missing image: $image_path"
        exit 1
      fi
      ;;
    *)
      echo "README.md image paths must use src/img/... so they render on GitHub: $image_path"
      exit 1
      ;;
  esac
done <<< "$readme_image_refs"

while IFS= read -r plugin_asset; do
  [ -z "$plugin_asset" ] && continue
  if [ ! -f "src/$plugin_asset" ]; then
    echo "src/plugin.json references a missing asset: src/$plugin_asset"
    exit 1
  fi
done < <(jq -r '.info.logos[]?, .info.screenshots[]?.path' src/plugin.json)

gofmt_files="$(gofmt -l Magefile.go pkg)"
if [ -n "$gofmt_files" ]; then
  echo "Go files are not formatted:"
  echo "$gofmt_files"
  exit 1
fi

go vet ./pkg/...
go mod verify
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 run
go test -v -race -count=1 ./pkg/...
go run github.com/securego/gosec/v2/cmd/gosec@latest ./pkg/...
go run golang.org/x/vuln/cmd/govulncheck@latest ./pkg/...

osv_dir="$(mktemp -d)"
cp go.mod go.sum "$osv_dir/"
go run github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.3.0 scan source --format vertical -L "$osv_dir/go.mod"
rm -rf "$osv_dir"
osv_dir=""

go run github.com/zricethezav/gitleaks/v8@latest detect --source . --no-git --redact --verbose
npm audit --audit-level=moderate
npm audit --omit=dev --audit-level=moderate
npm run lint
npm run typecheck
npm run test:ci
npm run build

plugin_id="$(jq -r '.id' src/plugin.json | tr -cd 'a-zA-Z0-9._-')"
expected_readme_prefix="/public/plugins/${plugin_id}/"
dist_readme_image_refs="$(sed -n 's/.*<img[^>]* src="\([^"]*\)".*/\1/p' dist/README.md)"
if [ -z "$dist_readme_image_refs" ]; then
  echo "dist/README.md must include image references for Grafana plugin details."
  exit 1
fi

while IFS= read -r image_path; do
  [ -z "$image_path" ] && continue
  case "$image_path" in
    "$expected_readme_prefix"img/*)
      dist_image_path="dist/${image_path#"$expected_readme_prefix"}"
      if [ ! -f "$dist_image_path" ]; then
        echo "dist/README.md references a missing packaged image: $image_path"
        exit 1
      fi
      ;;
    *)
      echo "dist/README.md image paths must use ${expected_readme_prefix}img/... so they render in Grafana: $image_path"
      exit 1
      ;;
  esac
done <<< "$dist_readme_image_refs"

while IFS= read -r plugin_asset; do
  [ -z "$plugin_asset" ] && continue
  if [ ! -f "dist/$plugin_asset" ]; then
    echo "dist/plugin.json references a missing packaged asset: dist/$plugin_asset"
    exit 1
  fi
done < <(jq -r '.info.logos[]?, .info.screenshots[]?.path' dist/plugin.json)

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

version="$(jq -r '.info.version' src/plugin.json)"
archive_name="${plugin_id}-${version}-local.zip"
package_dir="$(mktemp -d)"

mkdir -p "${package_dir}/${plugin_id}"
cp -R dist/. "${package_dir}/${plugin_id}/"
(cd "$package_dir" && zip -qr "${repo_root}/${archive_name}" "$plugin_id")

if zipinfo -1 "$archive_name" | grep -E '(^|/)(\.cache|node_modules)(/|$)'; then
  echo "Plugin archive contains local cache or dependency directories."
  exit 1
fi

npx --cache "$npm_config_cache" -y @grafana/plugin-validator@latest -jsonOutput "$archive_name"
