# Reolink SIP Gateway for Home Assistant

Community Home Assistant app that bridges bidirectional audio between a Reolink Video Doorbell and SIP. A Home Assistant visitor event can place a call, and the registered gateway extension can optionally be called directly.

> **Community project:** This repository is not affiliated with, endorsed by, or supported by Reolink or the Home Assistant project.

## Current release

**v0.8.0** hardens the incoming-call path introduced in v0.7. Allowed telephone numbers or internal SIP usernames can now be whitelisted before any dialog or camera resources are reserved. A short acoustic indication is played at the doorbell before an incoming call is answered, and an RTP inactivity watchdog cleans up abandoned calls even when no SIP `BYE` arrives.

The existing visitor-triggered outbound call and the proven v0.6 media path remain unchanged. Incoming and outgoing calls share the same G.711/RTP, AEC and Reolink implementation; the watchdog observes valid negotiated RTP packets rather than audio level, so ordinary silent conversation intervals do not end a call.

Highlights:

- SIP registration plus outbound and opt-in automatically answered incoming calls using G.711 A-law/µ-law.
- Exact normalized incoming-caller whitelist, with an explicit `*` compatibility setting.
- Short pre-answer acoustic connection indication using the established coded marker generator.
- Configurable SIP RTP inactivity watchdog for deterministic cleanup of broken calls.
- Home Assistant Reolink visitor binary sensor as call trigger, with entity-registry auto-discovery or manual override.
- Reolink standalone and NVR media profiles.
- Bidirectional audio via RTSP/ONVIF or Reolink Baichuan, depending on profile.
- Native WebRTC AudioProcessing echo cancellation.
- Automatic acoustic startup-delay calibration.
- Fixed calibrated coarse AEC delay during calls; no competing live Go delay-control loop.
- Zero-lookahead elastic SIP-to-Baichuan talkback playout with bounded ± correction and soft residual discontinuities.
- Five grouped Home Assistant configuration sections: Reolink, SIP, Audio, Call, Operation & diagnostics.
- Transparent PNG icon/logo without the former white outer canvas.
- `sip_registrar: auto` resolves the Home Assistant host's IPv4 default gateway at startup; a manual IP/DNS registrar remains supported.
- Fresh installs default the Reolink username to `admin`.
- `visitor_entity: auto` uses Home Assistant's compact enabled-entity registry view; ambiguous multi-doorbell setups require manual selection.
- The UI calls `dry_run` **Passive mode / Passivmodus**; the internal key remains unchanged for configuration compatibility.

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
  reolink_username: admin
  reolink_password: "change-me"
  reolink_mode: nvr
  nvr_channel_number: 2
  reolink_rtsp_port: 554
  baichuan_port: 9000

sip:
  sip_registrar: auto
  sip_username: "doorbell"
  sip_password: "change-me"
  sip_destination: "**610"
  sip_display_name: "Front Door"
  sip_codec_preference: pcma
  sip_registrar_port: 5060
  sip_local_port: 5070

audio:
  echo_cancellation_enabled: true
  webrtc_high_pass_filter_enabled: true
  webrtc_noise_suppression_enabled: true

call:
  visitor_entity: auto
  incoming_calls_enabled: false
  incoming_allowed_callers:
    - "*"
  incoming_connection_tone_enabled: true
  debounce_seconds: 3
  ring_timeout_seconds: 30
  rtp_inactivity_timeout_seconds: 15
  max_call_duration_seconds: 300

diagnostics:
  dry_run: false
  log_level: info
```

`nvr_channel_number` is deliberately **1-based** in the user interface. The startup adapter translates it to the internal Reolink channel representation without exposing the protocol-specific zero-based value.

With `sip_registrar: auto`, the startup adapter reads the Home Assistant host's IPv4 routing table and uses its default-gateway address (commonly the FRITZ!Box). Set an IP address or DNS name instead to override auto-detection. Existing saved registrar values are not replaced during an update.

With `visitor_entity: auto`, the adapter queries Home Assistant's compact `config/entity_registry/list_for_display` view and selects the single enabled `binary_sensor` from platform `reolink` with translation key `visitor`. Renamed entity IDs are therefore supported. If none or more than one are enabled, startup asks for an explicit manual entity instead of guessing. Existing manual visitor entities are retained during updates. WebSocket frames and complete messages remain bounded to 16 MiB.

Set **Allow incoming SIP calls** (`incoming_calls_enabled`) in the **Call** section to call the camera through the gateway. With a FRITZ!Box, dial the internal number assigned to the gateway's IP telephone, for example `**620`; use the actual number shown by the FRITZ!Box. The option defaults to `false` so upgrades never begin auto-answering unexpectedly. Signalling is accepted only from the configured registrar IP and UDP port, and the normalized SIP caller user must match `incoming_allowed_callers`. The compatibility value `*` permits every caller; replace it with the telephone numbers or internal extensions that should be accepted. Country-code variants are intentionally not inferred.

Before an accepted incoming call is answered, `incoming_connection_tone_enabled` plays the first four symbols (256 ms) of the existing acoustic marker through the actual Reolink talkback path. `rtp_inactivity_timeout_seconds` then ends either call direction when no valid negotiated RTP audio packet is received for the configured interval. If only internal calls should reach the camera, do not assign external incoming numbers to this IP telephone in the FRITZ!Box, because forwarded external calls also originate from the trusted registrar.

Planned sequencing: native Home Assistant status/caller sensors and test-call/hang-up buttons are reserved for v0.9 as a companion integration; DTMF follows separately in v1.0.

The **Passive mode / Passivmodus** toggle in **Operation & diagnostics / Betrieb & Diagnose** keeps the internal key `dry_run` for backwards compatibility. It monitors visitor events but suppresses SIP registration, outbound calls and the audible startup calibration marker.

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
