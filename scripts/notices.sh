#!/bin/sh
# Rewrites THIRD_PARTY_NOTICES from the licences of every module linked into the app on Linux, macOS and Windows.
set -eu
cd "$(dirname "$0")/.."

cache=$(go env GOMODCACHE)
mods=$(for os in linux darwin windows; do
	GOOS=$os GOARCH=amd64 go list -deps -f '{{if not .Standard}}{{.Module.Path}}@{{.Module.Version}}{{end}}' ./cmd/meshcore-tui
done | grep -v '^github.com/verdantran/' | sort -uf)

{
	cat <<'EOF'
meshcore-tui includes the following third-party software and data.

Natural Earth (https://www.naturalearthdata.com), release v5.1.2, public domain.
The world base map built into the app is derived from it.

Street detail is not shipped with the app. When you ask for it, the app downloads
vector tiles from OpenFreeMap (https://openfreemap.org) in the OpenMapTiles schema
(https://openmaptiles.org), built from OpenStreetMap data (https://www.openstreetmap.org),
© OpenStreetMap contributors, available under the Open Database License (ODbL).
The map credits them whenever that detail is on screen.

The table of look-alike characters in internal/untrusted/confusables.txt is drawn from
Unicode's confusables.txt (https://www.unicode.org/Public/security/), under the
Unicode License v3, which follows.

EOF
	cat internal/untrusted/UNICODE-LICENSE
	cat <<'EOF'

Chess over direct messages follows the message format, and the way a game is rebuilt
from its messages, of meshcore-companion by edleeman17
(https://github.com/edleeman17/meshcore-companion), under the MIT License, which follows.

EOF
	cat internal/chess/MESHCORE-COMPANION-LICENSE
	cat <<'EOF'

The Go standard library is compiled into the app under the licence below, followed by
the Go modules the app uses, each with its licence in full.
EOF
	printf '\n\n================================================================================\n%s (%s)\n================================================================================\n\n' "Go standard library" "LICENSE"
	golicence="$(go env GOROOT)/LICENSE"
	[ -f "$golicence" ] || golicence="$(go env GOROOT)/../LICENSE" # Homebrew keeps it beside libexec
	cat "$golicence"
	for mv in $mods; do
		path=${mv%@*}
		dir="$cache/$(printf '%s' "$path" | perl -pe 's/([A-Z])/"!".lc($1)/ge')@${mv#*@}"
		for f in $(ls "$dir" | grep -iE '^(licen[cs]e|copying)'); do
			printf '\n\n================================================================================\n%s (%s)\n================================================================================\n\n' "$path" "$f"
			cat "$dir/$f"
		done
	done
} > THIRD_PARTY_NOTICES
