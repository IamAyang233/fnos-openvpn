#!/bin/bash
set -e
SRC=/tmp/fnosbuild
OUT="/tmp/openvpn_1.0.0_x86.fpk"
WORK=/tmp/bfpkg
rm -rf "$WORK"; mkdir -p "$WORK/app_root" "$WORK/pkg"

cp -a "$SRC/app" "$WORK/app_root/app"
cp -a "$SRC/bin" "$WORK/app_root/bin" 2>/dev/null || true
cp -a "$SRC/ui" "$WORK/app_root/ui"

( cd "$WORK/app_root" && tar -cf "$WORK/app.tar" --mode=0755 ./app/bin )
( cd "$WORK/app_root" && tar -rf "$WORK/app.tar" --mode=0644 --exclude=./app/bin . )
gzip -c "$WORK/app.tar" > "$WORK/app.tgz"

cp "$WORK/app.tgz" "$WORK/pkg/app.tgz"
cp -a "$SRC/cmd" "$WORK/pkg/cmd" 2>/dev/null || true
cp -a "$SRC/config" "$WORK/pkg/config"
cp -a "$SRC/wizard" "$WORK/pkg/wizard" 2>/dev/null || true
cp "$SRC"/*.sc "$WORK/pkg/" 2>/dev/null || true
cp "$SRC"/ICON*.PNG "$WORK/pkg/"
cp -a "$SRC/ui" "$WORK/pkg/ui" 2>/dev/null || true
cp "$SRC/manifest" "$WORK/pkg/manifest"

cd "$WORK/pkg" && tar -czf "$OUT" *
echo "DONE"
ls -lh "$OUT"
