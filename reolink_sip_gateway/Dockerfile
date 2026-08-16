FROM golang:1.23-bookworm AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/reolink-sip-gateway ./cmd/gateway

# Build a deliberately tiny bridge to Debian's WebRTC AudioProcessing library.
# The long Reolink delay is aligned in Go; this helper receives one matching
# 10 ms render/capture pair at a time and therefore avoids GStreamer's second
# timestamp/latency layer entirely.
FROM debian:trixie-slim AS aec-builder
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        g++ \
        pkg-config \
        libwebrtc-audio-processing-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY native/aec-helper/main.cc ./main.cc
# Debian's public WebRTC 1.3 header itself contains an intentionally unused
# constructor parameter. Keep -Werror for our helper, but classify the packaged
# third-party headers as system headers so their internal warnings do not become
# build failures. pkg-config's non-include flags are preserved.
RUN set -eu; \
    mkdir -p /out; \
    WEBRTC_INCLUDE_DIR="$(pkg-config --variable=includedir webrtc-audio-processing-1)/webrtc-audio-processing-1"; \
    test -f "$WEBRTC_INCLUDE_DIR/modules/audio_processing/include/audio_processing.h"; \
    c++ -std=c++17 -O2 -DNDEBUG -Wall -Wextra -Wpedantic -Werror \
       -isystem "$WEBRTC_INCLUDE_DIR" \
       $(pkg-config --cflags-only-other webrtc-audio-processing-1) \
       main.cc -o /out/reolink-aec-helper \
       $(pkg-config --libs webrtc-audio-processing-1); \
    strip /out/reolink-aec-helper

FROM ghcr.io/home-assistant/base-debian:trixie
ARG BUILD_VERSION
ARG BUILD_ARCH
LABEL \
    io.hass.version="${BUILD_VERSION}" \
    io.hass.type="app" \
    io.hass.arch="${BUILD_ARCH}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        ffmpeg \
        libwebrtc-audio-processing-1-3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/reolink-sip-gateway /usr/bin/reolink-sip-gateway
COPY --from=aec-builder /out/reolink-aec-helper /usr/bin/reolink-aec-helper
COPY LICENSE THIRD-PARTY-NOTICES.md /usr/share/doc/reolink-sip-gateway/
COPY rootfs /
RUN chmod 0755 /usr/bin/reolink-sip-gateway \
    /usr/bin/reolink-aec-helper \
    /etc/services.d/reolink-sip-gateway/run
