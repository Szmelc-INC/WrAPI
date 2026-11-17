# Maintainer: silverx <silverx@localhost>
# Arch package for WrAPI. Builds from the GitHub repo with the vendored
# Go modules (no network needed beyond the source fetch).

pkgname=wrapi
pkgver=0.4.0
pkgrel=1
pkgdesc='Google AI Mode / Meta AI wrapper API — OpenAI-compatible localhost server with Hyprland window management'
arch=('x86_64')
url='https://github.com/Szmelc-INC/WrAPI'
license=('MIT')
depends=('glibc')
makedepends=('git' 'go')
optdepends=(
  'hyprland: invisible window placement on a dedicated workspace'
)
source=("$pkgname::git+$url.git#branch=main")
sha256sums=('SKIP')

pkgver() {
  cd "$pkgname"
  # No tags yet: version as revisions-since-import.
  printf "0.4.0.r%s.%s" "$(git rev-list --count HEAD)" "$(git rev-parse --short HEAD)"
}

prepare() {
  cd "$pkgname"
  # Vendored in-repo; keep go.sum honest.
  go mod verify
}

build() {
  cd "$pkgname"
  export CGO_ENABLED=0
  go build -trimpath -mod=vendor -ldflags="-s -w" -o wrapi ./cmd/wrapi
}

check() {
  cd "$pkgname"
  go vet ./...
}

package() {
  cd "$pkgname"

  install -Dm755 wrapi "$pkgdir/usr/bin/wrapi"
  install -Dm644 LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
  install -Dm644 README.md "$pkgdir/usr/share/doc/$pkgname/README.md"

  # Site drivers: JSON specs, tunable without rebuilding.
  install -dm755 "$pkgdir/usr/share/wrapi/drivers"
  install -Dm644 drivers/*.json "$pkgdir/usr/share/wrapi/drivers/"

  # systemd user unit with system paths (profile under ~/.local/share).
  install -Dm644 /dev/stdin "$pkgdir/usr/lib/systemd/user/wrapi.service" <<'UNIT'
[Unit]
Description=WRAPI - AI chat wrapper API (localhost)
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
WorkingDirectory=%h/.local/share/wrapi
ExecStart=/usr/bin/wrapi serve
Environment=WRAPI_MODE=workspace
Environment=WRAPI_ADDR=127.0.0.1:8787
Environment=WRAPI_DRIVERS_DIR=/usr/share/wrapi/drivers
Environment=WRAPI_PROFILE_DIR=%h/.local/share/wrapi/profile
Environment=HOME=%h
Restart=on-failure
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=graphical-session.target
UNIT
}
