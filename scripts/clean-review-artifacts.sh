#!/usr/bin/env bash
set -euo pipefail

rm -rf .cache plugin-dist
find . -maxdepth 1 -type f \( -name '*.zip' -o -name '*.tar.gz' -o -name '*.tgz' \) -delete

if [ -f dist/go_plugin_build_manifest ] && grep -Eq ':(\.cache|node_modules)/' dist/go_plugin_build_manifest; then
  rm -f dist/go_plugin_build_manifest
fi
