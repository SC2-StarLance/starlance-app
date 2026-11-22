#!/usr/bin/env bash
# Builds the single-file Linux download from an already-built binary.
#
# Usage: packaging/linux/build-appimage.sh <binary> <output.AppImage>
#
# The bundle is deliberately thin: it carries the app, desktop entry and icon,
# and uses the host's GTK/WebKit exactly like the zip. Bundling WebKitGTK would
# break the app's first-run relocation, which copies only the binary into
# ~/.local/share/starlance-app.
set -euo pipefail

binary=${1:?usage: $0 <binary> <output.AppImage>}
output=${2:?usage: $0 <binary> <output.AppImage>}
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

appdir="$work/AppDir"
mkdir -p "$appdir/usr/bin"
install -m 0755 "$binary" "$appdir/usr/bin/starlance-app"
install -m 0644 "$repo/packaging/linux/starlance-app.desktop" "$appdir/"
install -m 0644 "$repo/packaging/linux/starlance-app.png" "$appdir/"
ln -s starlance-app.png "$appdir/.DirIcon"
cat > "$appdir/AppRun" <<'EOF'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/starlance-app" "$@"
EOF
chmod 0755 "$appdir/AppRun"

tool=${APPIMAGETOOL:-}
if [ -z "$tool" ]; then
  tool="$work/appimagetool"
  curl -fsSL -o "$tool" \
    https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage
  echo "ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0  $tool" \
    | sha256sum -c - >/dev/null
  chmod 0755 "$tool"
fi

ARCH=x86_64 "$tool" --appimage-extract-and-run --no-appstream \
  "$appdir" "$output"
