#!/usr/bin/env bash
# Собирает .deb и (если доступен rpmbuild) .rpm для локальной установки через
# штатный пакетный менеджер:
#
#   sudo apt install ./localmeetassist_0.2.0_amd64.deb
#   sudo dnf install ./localmeetassist-0.2.0-1.x86_64.rpm
#
# Репозиторий не нужен: зависимости (libayatana-appindicator3, X11, PulseAudio)
# ставятся из дистрибутивных репозиториев.
#
# Использование:
#   scripts/make-linux-package.sh <version> <arch> <binary> [output-dir]
#     arch: amd64 | arm64
set -euo pipefail

version="${1:-}"
arch="${2:-}"
binary="${3:-}"
out="${4:-dist}"
if [ -z "$version" ] || [ -z "$arch" ] || [ -z "$binary" ]; then
  echo "usage: $0 <version> <amd64|arm64> <path-to-binary> [output-dir]" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

case "$arch" in
  amd64) deb_arch="amd64"; rpm_arch="x86_64" ;;
  arm64) deb_arch="arm64"; rpm_arch="aarch64" ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

# Путь к бинарнику нужен абсолютным: spec копирует его в buildroot.
binary_abs="$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")"
# Каталог вывода тоже приводим к абсолютному: rpmbuild получает _topdir как есть.
mkdir -p "$out"
out="$(cd "$out" && pwd)"

stage="$out/linux-stage-$arch"
rm -rf "$stage"
mkdir -p "$stage/usr/bin" \
  "$stage/usr/share/applications" \
  "$stage/usr/share/icons/hicolor/256x256/apps" \
  "$stage/usr/share/doc/localmeetassist"

install -m 0755 "$binary" "$stage/usr/bin/localmeetassist"
install -m 0644 packaging/linux/localmeetassist.desktop \
  "$stage/usr/share/applications/localmeetassist.desktop"
install -m 0644 packaging/linux/icon-256.png \
  "$stage/usr/share/icons/hicolor/256x256/apps/localmeetassist.png"
install -m 0644 README.md "$stage/usr/share/doc/localmeetassist/README.md"
install -m 0644 LICENSE "$stage/usr/share/doc/localmeetassist/copyright"

# --- .deb ------------------------------------------------------------------
deb="$out/localmeetassist_${version}_${deb_arch}.deb"
mkdir -p "$stage/DEBIAN"
cat > "$stage/DEBIAN/control" <<EOF
Package: localmeetassist
Version: $version
Architecture: $deb_arch
Maintainer: LocalMeetAssist <noreply@github.com>
Section: sound
Priority: optional
Installed-Size: $(du -sk "$stage/usr" | cut -f1)
Depends: libayatana-appindicator3-1, libx11-6
Recommends: libpulse0 | libasound2
Homepage: https://github.com/a3ak/LocalMeetAssist
Description: Local meeting recorder with transcription and minutes
 Records the microphone and system audio as separate tracks, transcribes
 speech locally, separates remote participants by voice and builds meeting
 minutes with a local model. Everything runs on this computer.
EOF
dpkg-deb --build --root-owner-group "$stage" "$deb" >/dev/null
echo "built: $deb"

# --- .rpm ------------------------------------------------------------------
# Зависимости не перечисляем вручную: rpmbuild сам добавляет Requires по
# SONAME (libX11.so.6, libayatana-appindicator3.so.1 и т.д.), а пакетный
# менеджер находит их в репозиториях дистрибутива.
if command -v rpmbuild >/dev/null 2>&1; then
  spec="$out/localmeetassist.spec"
  topdir="$out/rpmbuild-$arch"
  rm -rf "$topdir"
  mkdir -p "$topdir"/{BUILD,RPMS,SOURCES,SPECS,SRPMS}
  cat > "$spec" <<EOF
Name: localmeetassist
Version: $version
Release: 1
Summary: Local meeting recorder with transcription and minutes
License: MIT
URL: https://github.com/a3ak/LocalMeetAssist
BuildArch: $rpm_arch
AutoReqProv: yes

%description
Records the microphone and system audio as separate tracks, transcribes speech
locally, separates remote participants by voice and builds meeting minutes with
a local model. Everything runs on this computer.

%install
mkdir -p %{buildroot}/usr/bin %{buildroot}/usr/share/applications \\
  %{buildroot}/usr/share/icons/hicolor/256x256/apps \\
  %{buildroot}/usr/share/doc/localmeetassist
install -m 0755 $binary_abs %{buildroot}/usr/bin/localmeetassist
install -m 0644 $root/packaging/linux/localmeetassist.desktop \\
  %{buildroot}/usr/share/applications/localmeetassist.desktop
install -m 0644 $root/packaging/linux/icon-256.png \\
  %{buildroot}/usr/share/icons/hicolor/256x256/apps/localmeetassist.png
install -m 0644 $root/README.md %{buildroot}/usr/share/doc/localmeetassist/README.md
install -m 0644 $root/LICENSE %{buildroot}/usr/share/doc/localmeetassist/copyright

%post
# Иконка и меню обновляются только если инструменты установлены.
command -v gtk-update-icon-cache >/dev/null 2>&1 && \\
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
command -v update-desktop-database >/dev/null 2>&1 && \\
  update-desktop-database -q /usr/share/applications || true
exit 0

%postun
command -v gtk-update-icon-cache >/dev/null 2>&1 && \\
  gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
command -v update-desktop-database >/dev/null 2>&1 && \\
  update-desktop-database -q /usr/share/applications || true
exit 0

%files
/usr/bin/localmeetassist
/usr/share/applications/localmeetassist.desktop
/usr/share/icons/hicolor/256x256/apps/localmeetassist.png
/usr/share/doc/localmeetassist/README.md
/usr/share/doc/localmeetassist/copyright
EOF
  rpmbuild -bb --quiet \
    --define "_topdir $topdir" \
    --define "debug_package %{nil}" \
    "$spec" >/dev/null
  rpm_file="$(find "$topdir/RPMS" -name '*.rpm' -type f | head -1)"
  if [ -n "$rpm_file" ]; then
    cp "$rpm_file" "$out/localmeetassist-${version}-1.${rpm_arch}.rpm"
    echo "built: $out/localmeetassist-${version}-1.${rpm_arch}.rpm"
  fi
else
  echo "rpmbuild not found: rpm skipped" >&2
fi

rm -rf "$stage" "$topdir" "$out/localmeetassist.spec"
