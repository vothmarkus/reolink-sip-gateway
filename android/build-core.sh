#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MOBILE_VERSION="v0.0.0-20260908204917-8b95e45f8d3e"

cd "$ROOT/reolink_sip_gateway"
go get "golang.org/x/mobile@$MOBILE_VERSION"
go install "golang.org/x/mobile/cmd/gomobile@$MOBILE_VERSION"
go install "golang.org/x/mobile/cmd/gobind@$MOBILE_VERSION"
gomobile init
mkdir -p "$ROOT/android/app/libs"
gomobile bind -androidapi 26 \
  -target=android/arm64,android/arm \
  -javapkg de.vothmarkus.reolinksip.core \
  -o "$ROOT/android/app/libs/reolink-core.aar" \
  ./mobilebridge
echo "Built android/app/libs/reolink-core.aar"
