#!/usr/bin/env bash
# Build the SkyLoader sample plugins into real Windows x64 DLLs.
#
# Output lands next to each plugin.json. The script cross-compiles from any OS
# with Zig (the toolchain SkyLoader itself uses), or falls back to MinGW-w64.
# On Windows, prefer plugins/examples/build.cmd inside an MSVC x64 prompt.
#
# usage: ./build.sh                # build all samples
#        ./build.sh 01-field-settings
#        ./build.sh 01-field-settings 03-struct-state
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
include_abi="-I$repo/libraries/SkyBootstrap/include"
include_kit="-I$here/common"
all_samples="01-field-settings 02-list-collection 03-struct-state"

flags=(-std=c++17 -O2 -shared)

detect_toolchain() {
  if command -v zig >/dev/null 2>&1; then
    echo zig
  elif command -v x86_64-w64-mingw32-g++ >/dev/null 2>&1; then
    echo mingw
  else
    echo ""
  fi
}

toolchain="$(detect_toolchain)"
if [ -z "$toolchain" ]; then
  echo "error: no cross compiler found." >&2
  echo "  Install Zig (https://ziglang.org/download) and put it on PATH," >&2
  echo "  or install the x86_64-w64-mingw32-g++ toolchain." >&2
  exit 1
fi
echo "toolchain: $toolchain"

samples="$*"
if [ -z "$samples" ]; then
  samples="$all_samples"
fi

for sample in $samples; do
  dir="$here/$sample"
  if [ ! -d "$dir" ]; then
    echo "error: unknown sample '$sample'" >&2
    exit 1
  fi
  # The DLL name follows the lone .cpp source, matching the bundle manifest.
  source_file="$dir/"*.cpp
  for source_path in $source_file; do
    name="$(basename "$source_path" .cpp)"
    output="$dir/$name.dll"
    echo "building $sample -> $name.dll"
    case "$toolchain" in
      zig)
        zig c++ "${flags[@]}" -target x86_64-windows-gnu \
          "$include_abi" "$include_kit" "$source_path" -o "$output"
        ;;
      mingw)
        # Static runtime keeps the sample DLL free of libstdc++ dependencies.
        x86_64-w64-mingw32-g++ "${flags[@]}" -static \
          "$include_abi" "$include_kit" "$source_path" -o "$output"
        ;;
    esac
  done
done

echo "done. import each sample folder into SkyLoader to test it."
