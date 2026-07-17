#!/usr/bin/env sh
# HexRead CLI installer. Downloads the latest `hexread` release, verifies it, and installs the binary.
# Verification is REQUIRED BY DEFAULT: a cosign keyless signature over the checksums (needs cosign on
# PATH) PLUS the per-archive sha256. Set HEXREAD_REQUIRE_COSIGN=0 to fall back to sha256-only (NOT
# recommended: the checksums come from the same release, so sha256 alone gives no integrity against a
# tampered release). Any verification mismatch aborts before anything is installed.
#
#   curl -fsSL https://hexread.com/install | sh
#   curl -fsSL https://hexread.com/install | sh -s -- --bin-dir ~/.local/bin
#   HEXREAD_REQUIRE_COSIGN=0 curl -fsSL https://hexread.com/install | sh   # sha256-only (discouraged)
set -eu
# pipefail is not POSIX (dash lacks it); enable it where the shell supports it (bash/ksh/zsh) so a
# broken curl in a pipe fails the step instead of feeding a truncated file downstream.
# shellcheck disable=SC3040
(set -o pipefail) 2>/dev/null && set -o pipefail || true

# The whole installer runs inside main() invoked on the LAST line, so `curl … | sh` executes nothing
# until the entire script has been read - a truncated download can never run a partial install.
main() {
  REPO="HexWorldEU/hexread-cli"
  BIN_DIR="${HEXREAD_BIN_DIR:-/usr/local/bin}"
  # Signature verification is ON by default; only an explicit HEXREAD_REQUIRE_COSIGN=0 disables it.
  REQUIRE_COSIGN="${HEXREAD_REQUIRE_COSIGN:-1}"
  [ "$REQUIRE_COSIGN" = "0" ] && REQUIRE_COSIGN=""
  # The cosign keyless identity that signed the release (GitHub Actions OIDC).
  CERT_IDENTITY_RE="https://github.com/${REPO}/.github/workflows/release.yml@refs/tags/v.*"
  CERT_OIDC_ISSUER="https://token.actions.githubusercontent.com"

  while [ $# -gt 0 ]; do
    case "$1" in
      --bin-dir)
        [ $# -ge 2 ] || { echo "error: --bin-dir needs a directory argument" >&2; exit 2; }
        BIN_DIR="$2"; shift 2 ;;
      --require-cosign) REQUIRE_COSIGN=1; shift ;;
      *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
  done

  need() { command -v "$1" >/dev/null 2>&1 || { echo "error: '$1' is required" >&2; exit 1; }; }
  need curl; need tar
  # sha256 tool: sha256sum (Linux) or shasum (macOS).
  if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | cut -d' ' -f1; }
  elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
  else
    echo "error: sha256sum or shasum is required" >&2; exit 1
  fi

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac
  case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os (use scoop or winget on Windows)" >&2; exit 1 ;; esac

  # Fetch fully before grepping: `curl | grep -m1` makes grep close the pipe on first match,
  # and curl then reports a spurious `curl: (23)` write error to the user.
  release_json=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")
  tag=$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | cut -d'"' -f4)
  [ -n "$tag" ] || { echo "could not determine the latest release" >&2; exit 1; }
  ver=${tag#v}
  base="https://github.com/${REPO}/releases/download/${tag}"
  archive="hexread_${ver}_${os}_${arch}.tar.gz"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cd "$tmp"
  echo "Downloading hexread ${tag} (${os}/${arch})…"
  curl -fsSL -o "$archive"    "${base}/${archive}"
  curl -fsSL -o checksums.txt "${base}/checksums.txt"

  # 1) Verify the checksums file's cosign signature. Required by default: if cosign is not installed the
  #    install ABORTS (never a silent warn-and-continue) unless HEXREAD_REQUIRE_COSIGN=0 was set.
  if command -v cosign >/dev/null 2>&1; then
    curl -fsSL -o checksums.txt.sigstore.json "${base}/checksums.txt.sigstore.json"
    echo "Verifying signature (cosign)…"
    cosign verify-blob \
      --bundle checksums.txt.sigstore.json \
      --certificate-identity-regexp "$CERT_IDENTITY_RE" \
      --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
      checksums.txt >/dev/null || { echo "error: signature verification FAILED - aborting" >&2; exit 1; }
  elif [ -n "$REQUIRE_COSIGN" ]; then
    echo "error: cosign is required to verify this download but is not installed." >&2
    echo "       Install it (https://docs.sigstore.dev/cosign/installation/) and re-run, or -" >&2
    echo "       accepting sha256-only integrity - re-run with HEXREAD_REQUIRE_COSIGN=0." >&2
    exit 1
  else
    echo "warning: HEXREAD_REQUIRE_COSIGN=0 - skipping signature verification (sha256 only; no" >&2
    echo "         protection against a tampered release). Install cosign for full verification." >&2
  fi

  # 2) Verify the archive's sha256 against the checksums file.
  echo "Verifying checksum…"
  want=$(grep "  ${archive}\$" checksums.txt | cut -d' ' -f1)
  [ -n "$want" ] || { echo "error: ${archive} not in checksums.txt" >&2; exit 1; }
  got=$(sha256 "$archive")
  [ "$got" = "$want" ] || { echo "error: checksum mismatch - aborting" >&2; exit 1; }

  # 3) Install.
  tar -xzf "$archive" hexread
  mkdir -p "$BIN_DIR" 2>/dev/null || true
  chmod 0755 hexread
  if ! mv hexread "${BIN_DIR}/hexread" 2>/dev/null; then
    echo "error: cannot write to ${BIN_DIR} - re-run with sudo, or pick a user dir:" >&2
    echo "       curl -fsSL https://hexread.com/install | sh -s -- --bin-dir ~/.local/bin" >&2
    exit 1
  fi
  echo "✓ Installed hexread ${tag} → ${BIN_DIR}/hexread"
  "${BIN_DIR}/hexread" version || true
}

main "$@"
