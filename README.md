# Reolink SIP Gateway for Home Assistant

Community Home Assistant app that turns a Reolink Video Doorbell event into a SIP call and bridges bidirectional audio between the SIP endpoint and the doorbell.

> **Community project:** This repository is not affiliated with, endorsed by, or supported by Reolink or the Home Assistant project.

## Current release

**v0.5.10** is the final 0.5.x stabilization release. It keeps the hardware-proven audio/AEC path from the 0.5.x series and finalizes configuration migration, documentation, and public-repository packaging.

Highlights:

- SIP registration and outbound calls using G.711 A-law/µ-law.
- Home Assistant visitor binary sensor as call trigger.
- Reolink standalone and NVR media profiles.
- Bidirectional audio via RTSP/ONVIF or Reolink Baichuan, depending on profile.
- Native WebRTC AudioProcessing echo cancellation.
- Automatic acoustic startup-delay calibration.
- Fixed calibrated coarse AEC delay during calls; no competing live Go delay-control loop.
- Five grouped Home Assistant configuration sections: Reolink, SIP, Audio, Call, Diagnostics.
- Transparent PNG icon/logo without the former white outer canvas.

The current build targets **amd64** Home Assistant hosts.

## Installation

This repository is structured as a Home Assistant app repository. Home Assistant requires a `repository.yaml` file at repository root and keeps each app in its own subdirectory.

1. In Home Assistant, open **Settings → Apps → App store**.
2. Open the repository menu and add:

   `https://github.com/vothmarkus/reolink-sip-gateway`

3. Refresh the app store.
4. Install **Reolink SIP Gateway**.
5. Open the **Configuration** tab, enter Reolink/SIP settings, save, and restart the app.
6. Review the startup log. With echo cancellation enabled and `dry_run: false`, startup calibration plays a short coded marker through the doorbell speaker.

For the complete configuration reference and operating notes, see [`reolink_sip_gateway/DOCS.md`](reolink_sip_gateway/DOCS.md).

## Typical NVR configuration

```yaml
reolink:
  reolink_host: 192.168.1.50
  reolink_username: "reolink-user"
  reolink_password: "change-me"
  reolink_mode: nvr
  nvr_channel_number: 2
  reolink_rtsp_port: 554
  baichuan_port: 9000

sip:
  sip_registrar: 192.168.1.1
  sip_registrar_port: 5060
  sip_username: "doorbell"
  sip_password: "change-me"
  sip_destination: "**610"
  sip_local_port: 5070
  sip_display_name: "Front Door"
  sip_codec_preference: pcma

audio:
  echo_cancellation_enabled: true
  webrtc_high_pass_filter_enabled: true
  webrtc_noise_suppression_enabled: true

call:
  visitor_entity: binary_sensor.front_door_visitor
  ring_timeout_seconds: 30
  max_call_duration_seconds: 300
  debounce_seconds: 3

diagnostics:
  log_level: info
  dry_run: false
```

`nvr_channel_number` is deliberately **1-based** in the user interface. The startup adapter translates it to the internal Reolink channel representation without exposing the protocol-specific zero-based value.

## Echo cancellation

At normal startup, the gateway can measure the acoustic Reolink loop delay by transmitting a coded speech-band marker and correlating the received audio. The resulting coarse delay is held fixed during each call. The native WebRTC AEC3 implementation remains responsible for its internal fine alignment and adaptive filtering.

This design is intentional: hardware testing showed that the former Go live delay tracker could converge to a different time base and degrade echo suppression. Since v0.5.7 that live controller is disabled in production.

## Configuration migration

v0.5.10 finalizes the grouped configuration introduced in v0.5.8:

- On a direct upgrade from older flat 0.5.x options, legacy values are imported once.
- A persistent migration marker is written only after the grouped state is confirmed.
- After that marker exists, grouped values are authoritative.
- Normal starts do **not** write configuration back to the Home Assistant Supervisor.
- A compare-before-write guard protects the one-time migration from overwriting a concurrent user edit.

## Hardware status

The NVR/Baichuan path has been developed and hardware-tested with a Reolink Video Doorbell PoE behind an RLN8-410 NVR. Other Reolink firmware/device combinations may differ; detailed debug logs are useful when reporting compatibility issues.

## Development

The Go module is:

`github.com/vothmarkus/reolink-sip-gateway`

Local checks:

```bash
cd reolink_sip_gateway
gofmt -w cmd internal
go test ./...
go test -shuffle=on ./...
go vet ./...
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/gateway
```

The Docker image additionally builds a small native C++ helper against Debian's `libwebrtc-audio-processing-1` development package.

See [`reolink_sip_gateway/TESTING.md`](reolink_sip_gateway/TESTING.md) for the release regression checklist and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the media pipeline.

## Reporting issues

When opening an issue, include the app version, Reolink mode, relevant device/NVR model, and logs around startup or a call. **Remove passwords, SIP credentials, public phone numbers, tokens, and any other secrets before posting logs.**

## License

MIT. See [`LICENSE`](LICENSE). Third-party notices are in [`reolink_sip_gateway/THIRD-PARTY-NOTICES.md`](reolink_sip_gateway/THIRD-PARTY-NOTICES.md).
