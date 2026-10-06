#!/usr/bin/env bash
# Optional Linux x86_64 development runtime, outside the plugin and checkout.
set -euo pipefail
umask 077

if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo 'This minimum CPU setup targets Linux x86_64; see the upstream Decider guide for other platforms.' >&2
  exit 1
fi
for command in uv curl cmake g++ sha256sum; do
  command -v "$command" >/dev/null || { echo "Missing prerequisite: $command" >&2; exit 1; }
done

runtime_dir="${AGENTAI_DECIDER_DIR:-${TMPDIR:-/tmp}/agentai-decider-2b-$(id -u)}"
mkdir -p "$runtime_dir"
runtime_dir="$(cd "$runtime_dir" && pwd)"
export UV_CACHE_DIR="$runtime_dir/uv"
export UV_PYTHON_INSTALL_DIR="$runtime_dir/python"

uv python install --no-bin 3.12.13
if [ ! -x "$runtime_dir/venv/bin/python" ]; then
  uv venv --python 3.12.13 --managed-python "$runtime_dir/venv"
fi
python_bin="$runtime_dir/venv/bin/python"
uv pip install --python "$python_bin" 'torch==2.14.1+cpu' --index-url https://download.pytorch.org/whl/cpu
# Upstream's default dependencies include CUDA/training components. Its GGUF
# inference path uses the CPU Torch tensor utilities and packages below only.
uv pip install --python "$python_bin" --no-deps 'decider-ai==1.8.1'
export CMAKE_ARGS='-DGGML_CUDA=OFF -DGGML_NATIVE=OFF -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON'
export CMAKE_BUILD_PARALLEL_LEVEL=4
uv pip install --python "$python_bin" 'llama-cpp-python==0.3.36' 'transformers==5.18.0' 'numpy==2.5.3'

revision=ff2e5e687327eda9ac34e9a3ca84d3f400672c87
model_dir="$runtime_dir/model"
mkdir -p "$model_dir"
while read -r digest filename; do
  target="$model_dir/$filename"
  if [ -f "$target" ] && printf '%s  %s\n' "$digest" "$target" | sha256sum --check --status; then
    continue
  fi
  curl --fail --location --retry 3 --remove-on-error \
    "https://huggingface.co/Mapika/decider-2b-GGUF/resolve/$revision/$filename" --output "$target.part"
  printf '%s  %s\n' "$digest" "$target.part" | sha256sum --check --status
  mv "$target.part" "$target"
done <<'CHECKSUMS'
b7c132a67934d51c81abc96bb7724800f965ff5a288aed3e1ca7d8bc349c1386 decider-2b-v11-Q4_K_M.gguf
06b9509352d2af50381ab2247e083b80d32d5c0aba91c272ca9ff729b6a0e523 tokenizer.json
171ecbe7ddae98d11840698f7df2b8d5b4722139db0f0620d3bbf429bd656250 tokenizer_config.json
6e4891f2754a1c18a10f8dadb0c04e439e7f79fab0333d56641491bd4a05e722 decider_config.json
CHECKSUMS

echo "Decider 2B Q4_K_M CPU runtime ready in $runtime_dir."
echo 'Start with: bash scripts/run-decider-local.sh'
