#!/usr/bin/env bash
# Собирает LocalMeetAssist.app (universal: arm64 + amd64) и .dmg для macOS.
#
# Подпись: по умолчанию ad-hoc ("-"), этого достаточно для локального запуска.
# Если задан SIGN_IDENTITY (например, "Developer ID Application: Name (TEAMID)"),
# приложение подписывается этим сертификатом, а при наличии NOTARY_APPLE_ID,
# NOTARY_TEAM_ID и NOTARY_PASSWORD ещё и нотаризуется со stapling — тогда оно
# запускается двойным кликом у любого пользователя без обхода Gatekeeper.
#
# Использование:
#   scripts/make-app.sh <version> [output-dir]
set -euo pipefail

version="${1:-}"
out="${2:-dist}"
if [ -z "$version" ]; then
  echo "usage: $0 <version> [output-dir]" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

# Минимальная поддерживаемая macOS. Задаётся здесь, а не в cgo-флагах: иначе
# линковщик предупреждает о каждом объекте стандартной библиотеки Go, собранном
# под более новую систему. -Wl,-w гасит эти предупреждения, не влияя на линковку.
export MACOSX_DEPLOYMENT_TARGET=14.2
quiet_ldflags="-extldflags=-Wl,-w"

app_name="LocalMeetAssist"
bundle="$out/$app_name.app"
contents="$bundle/Contents"
staging="$out/app-staging"

rm -rf "$bundle" "$staging" "$out/dmg-root"
mkdir -p "$contents/MacOS" "$contents/Resources" "$staging" "$out/dmg-root"

# --- Универсальный бинарник -------------------------------------------------
# Два слайса собираются отдельно: cgo-код платформенный, lipo лишь склеивает
# готовые Mach-O.
for arch in arm64 amd64; do
  echo "building darwin/$arch"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w $quiet_ldflags -X localmeetassist/internal/version.Version=$version" \
    -o "$staging/localmeetassist-$arch" ./cmd/localmeetassist
done
lipo -create -output "$contents/MacOS/localmeetassist" \
  "$staging/localmeetassist-arm64" "$staging/localmeetassist-amd64"
chmod 0755 "$contents/MacOS/localmeetassist"

# Проверяем, что бандл действительно запустится на 14.2: MACOSX_DEPLOYMENT_TARGET
# легко потерять при правке скриптов, а заметить это можно уже у пользователя.
minos="$(otool -l "$contents/MacOS/localmeetassist" | awk '/LC_BUILD_VERSION/{found=1} found && /minos/{print $2; exit}')"
if [ -z "$minos" ]; then
  echo "cannot read the minimum macOS version from the binary" >&2
  exit 1
fi
if [ "$minos" != "14.2" ]; then
  echo "unexpected minimum macOS version: $minos (want 14.2)" >&2
  exit 1
fi
echo "minimum macOS version: $minos"

# --- Info.plist и локализация ----------------------------------------------
sed "s/__VERSION__/$version/g" packaging/macos/Info.plist > "$contents/Info.plist"
for lproj in en ru; do
  mkdir -p "$contents/Resources/$lproj.lproj"
  cp "packaging/macos/$lproj.lproj/InfoPlist.strings" "$contents/Resources/$lproj.lproj/"
done

# --- Иконка ----------------------------------------------------------------
iconset="$staging/AppIcon.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" packaging/macos/icon-1024.png \
    --out "$iconset/icon_${size}x${size}.png" >/dev/null
  sips -z "$((size * 2))" "$((size * 2))" packaging/macos/icon-1024.png \
    --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$contents/Resources/AppIcon.icns"

# --- Подпись ---------------------------------------------------------------
identity="${SIGN_IDENTITY:--}"
sign_args=(--force --sign "$identity" --identifier app.localmeetassist)
if [ "$identity" != "-" ]; then
  # Нотаризация требует hardened runtime; для ad-hoc подписи он не нужен.
  sign_args+=(--options runtime --timestamp)
fi
echo "signing with: $identity"
codesign "${sign_args[@]}" "$bundle"
codesign --verify --strict --verbose=1 "$bundle"

# --- Нотаризация (необязательно) -------------------------------------------
if [ "$identity" != "-" ] && [ -n "${NOTARY_APPLE_ID:-}" ] && [ -n "${NOTARY_TEAM_ID:-}" ] && [ -n "${NOTARY_PASSWORD:-}" ]; then
  echo "notarizing"
  ditto -c -k --keepParent "$bundle" "$staging/notarize.zip"
  xcrun notarytool submit "$staging/notarize.zip" \
    --apple-id "$NOTARY_APPLE_ID" \
    --team-id "$NOTARY_TEAM_ID" \
    --password "$NOTARY_PASSWORD" \
    --wait
  xcrun stapler staple "$bundle"
  xcrun stapler validate "$bundle"
else
  echo "notarization skipped: set SIGN_IDENTITY and NOTARY_* to enable it"
fi

# --- DMG -------------------------------------------------------------------
if [ "${NO_DMG:-0}" = "1" ]; then
  echo "dmg skipped: NO_DMG=1"
  echo "built: $bundle"
  exit 0
fi
dmg="$out/localmeetassist_${version}_macos_universal.dmg"
cp -R "$bundle" "$out/dmg-root/"
ln -s /Applications "$out/dmg-root/Applications"
if ! hdiutil create -quiet -volname "$app_name" -srcfolder "$out/dmg-root" \
  -ov -format UDZO "$dmg"; then
  echo "hdiutil could not create the dmg: .app собран, но образ не создан" >&2
  echo "в средах без доступа к DiskImages используйте NO_DMG=1" >&2
  exit 1
fi
rm -rf "$out/dmg-root" "$staging"

echo "built: $bundle"
echo "built: $dmg"
