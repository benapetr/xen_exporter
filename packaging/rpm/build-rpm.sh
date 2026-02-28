#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

VERSION="${VERSION:-$(cat VERSION)}"
RELEASE="${RELEASE:-1}"

make rpm VERSION="$VERSION" RELEASE="$RELEASE"
