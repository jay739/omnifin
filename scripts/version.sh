#!/usr/bin/env bash
# Prepare build metadata for GoReleaser, including contexts without a .git directory.
set -euo pipefail

if [[ -z "${JFA_GO_SNAPSHOT:-}" ]]; then
    export JFA_GO_SOURCEMAP=""
    export JFA_GO_COPYTS="echo skipping sourcemaps"
    export JFA_GO_STRIP=""
    export JFA_GO_MINIFY="--minify"
else
    export JFA_GO_SOURCEMAP="--sourcemap"
    export JFA_GO_COPYTS="cp -r tempts data/web/js/ts"
    export JFA_GO_STRIP="-s -w"
    export JFA_GO_MINIFY=""
fi
export INTERNAL="${INTERNAL:-on}"
if [[ "$INTERNAL" == "on" ]]; then export JFA_GO_TAG=""; else export JFA_GO_TAG="external"; fi
if [[ -z "${UPDATER:-}" ]]; then export JFA_GO_UPDATER=binary; else export JFA_GO_UPDATER="$UPDATER"; fi

export JFA_GO_VERSION="${JFA_GO_VERSION:-$(git describe --tags --exact-match HEAD 2>/dev/null || printf development)}"
export JFA_GO_VERSION="${JFA_GO_VERSION#v}"
export JFA_GO_COMMIT="${JFA_GO_COMMIT:-$(git rev-parse HEAD 2>/dev/null || printf unknown)}"
export JFA_GO_BUILD_TIME="${JFA_GO_BUILD_TIME:-$(date +%s)}"
export JFA_GO_BUILD_MODE="${JFA_GO_BUILD_MODE:-binary}"
export JFA_GO_CSS_VERSION="${JFA_GO_CSS_VERSION:-$(git describe --tags --abbrev=0 2>/dev/null || printf 'v%s' "$JFA_GO_VERSION")}"
export CSSVERSION="$JFA_GO_CSS_VERSION"
export JFA_GO_NFPM_EPOCH="${JFA_GO_NFPM_EPOCH:-$(git rev-list --all --count 2>/dev/null || printf '%s' "$JFA_GO_BUILD_TIME")}"
export JFA_GO_BUILT_BY="${JFA_GO_BUILT_BY:-local}"

if [[ "$JFA_GO_BUILD_MODE" == "docker" ]]; then
    if [[ ! "$JFA_GO_COMMIT" =~ ^[0-9a-f]{40}$ ]] || [[ ! "$JFA_GO_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || [[ ! "$JFA_GO_BUILD_TIME" =~ ^[0-9]+$ ]]; then
        printf '%s\n' 'Container builds require a release version, full source revision and Unix build time.' >&2
        exit 1
    fi
fi
exec "$@" --timeout "${JFA_GO_BUILD_TIMEOUT:-60m}"
