#!/usr/bin/env bash
# Guard: the archive filename GoReleaser produces is the one install.sh downloads. The installer
# builds that URL by hand, so the two are coupled by a string in two files - and the release action
# is pinned only to a major, so an inherited default template could change under it and 404 every
# `curl | sh` after the release had shipped.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

want='{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}'
tmpl=$(grep -oE 'name_template: *"[^"]+"' .goreleaser.yaml | head -1 | cut -d'"' -f2)
name=$(awk '/^project_name:/ {print $2}' .goreleaser.yaml)

fail() {
  echo "check-archive-name: $*" >&2
  exit 1
}
[ "$tmpl" = "$want" ] || fail "archives[].name_template is '$tmpl', want '$want' (pinned, not GoReleaser's default)"
[ "$name" = hexread ] || fail "project_name is '$name', but install.sh expects archives prefixed 'hexread_'"
# shellcheck disable=SC2016  # match install.sh's literal source, not an expansion
grep -qF 'archive="hexread_${ver}_${os}_${arch}.tar.gz"' scripts/install.sh ||
  fail "install.sh no longer builds hexread_<ver>_<os>_<arch>.tar.gz"
echo "check-archive-name: OK"
