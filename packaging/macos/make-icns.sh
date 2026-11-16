#!/bin/sh
# Build an .icns from a single source PNG. Needs macOS (sips + iconutil), so
# it runs in CI on the macOS runner and in `make app-macos` on a mac.
set -eu

src=$1
out=$2

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

base="$tmp/base-1024.png"
sips -s format png -z 1024 1024 "$src" --out "$base" >/dev/null

iconset="$tmp/AppIcon.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
	sips -z "$size" "$size" "$base" --out "$iconset/icon_${size}x${size}.png" >/dev/null
	double=$((size * 2))
	sips -z "$double" "$double" "$base" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done

iconutil -c icns "$iconset" -o "$out"
