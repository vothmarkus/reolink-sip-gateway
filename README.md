> **Android 0.3.2-alpha6:** Direkte Kameraanbindung ohne NVR, Android-Einstellungen und Testablauf: [docs/ANDROID.md](docs/ANDROID.md).

# Reolink SIP Gateway

Community Home Assistant app that bridges bidirectional audio between a Reolink Video Doorbell and SIP. A Home Assistant visitor event can place a call, and the registered gateway extension can optionally be called directly.

> **Community project:** This repository is not affiliated with, endorsed by, or supported by Reolink or the Home Assistant project.

## Version 2 beta

The `feature/v2-standalone` branch develops **2.0.0-beta.1**. One shared gateway now supports the Home Assistant app, a standalone Docker container and a native Linux service. ARM64 builds target the Raspberry Pi Zero 2 W with a 64-bit OS; amd64 remains supported.

Standalone installations receive visitor events directly from Reolink over Baichuan and have a local configuration page for camera, SIP, routes, audio and operation. `auto` still uses the IPv4 default gateway as the presumed FRITZ!Box; manual registrar settings are available. Standalone DTMF actions are deferred; the existing API v1/HA DTMF event contract is preserved.

See [the Version 2 installation and test guide](docs/V2-STANDALONE.md) for native Debian packages, Docker, initial login, backups and hardware acceptance. This is a development beta: Zero 2 W performance, physical doorbell events and two-way audio still require hardware testing. The stable `main` installation is separate from this branch.

## Stable 1.x release

**v1.5.0** makes the multi-camera FRITZ!Fon feature resilient and faster. NVR channels are refreshed every five minutes and the last successful catalog survives restarts and temporary NVR outages. Every camera with a Reolink UID receives a one-way, UID-derived URL that follows it across NVR channel moves; the v1.3 door URL and all v1.4 numbered URLs remain unchanged. Accepted visitor events asynchronously prefetch the door frame, while the Ingress page reports discovery, online/offline and image-source diagnostics.

The direct v1.2.2 call-route model remains unchanged: a fresh installation contains one editable `Standardroute` with visitor sensor `auto`, FRITZ!Box doorbell number `11` and three optional mobile-number fields. All routes continue to share one Reolink camera/media path and the same two optional SIP accounts.

Still-ringing losers receive `CANCEL`. If two peers answer across the winner decision, every `200 OK` is acknowledged and the losing dialog is immediately closed with `BYE`. The machine-readable integration contract remains API v1 and is documented in [`docs/api-v1.openapi.yaml`](docs/api-v1.openapi.yaml).

Visitor events, incoming INVITEs and route-specific API test calls enter one shared call controller. The first event owns the single media path; another simultaneous route is rejected and is not queued. Incoming and outgoing calls still share the same G.711/RTP, AEC and Reolink implementation introduced and hardware-tested in earlier releases.

Highlights:

- Versioned `/api/v1` with bearer authentication, a stable installation UUID, complete status snapshots and Server-Sent Events.
- Up to two independent SIP registrations: an optional door station plus an optional normal IP telephone for mobile calls; at least one is required in live mode.
- One to three simultaneous mobile destinations on the second account, with first-answer-wins SIP forking and deterministic CANCEL/ACK/BYE cleanup.
- Up to 32 named call routes with three direct mobile numbers per route, for FRITZ!Box doorbell buttons or mobile-only routing.
- One companion-integration test-call button per configured route, plus current/last route diagnostics.
- Negotiated `telephone-event/8000` reception for `0`–`9`, `*`, `#` and `A`–`D`, deduplicated to one event per completed keypress.
- Integration commands for a configured test call and idempotent hang-up, both routed through the normal call lifecycle.
- Current/last call direction and normalized current/last incoming caller number in the integration status model.
- SIP registration plus outbound and opt-in automatically answered incoming calls using G.711 A-law/µ-law.
- Exact normalized incoming-caller whitelist, with an explicit `*` compatibility setting.
- Short pre-answer acoustic connection indication using the established coded marker generator.
- Configurable SIP RTP inactivity watchdog for deterministic cleanup of broken calls.
- Home Assistant Reolink visitor binary sensor as call trigger, with entity-registry auto-discovery or manual override.
- Reolink standalone and NVR media profiles.
- Optional local FRITZ!Fon live-image endpoints with periodically refreshed NVR discovery, persistent last-known channels, UID-derived camera URLs, ring-event prefetch, a persistent independent path token, JPEG validation and output fitted inside AVM's approximately 480×640-pixel frame.
- Bidirectional audio via RTSP/ONVIF or Reolink Baichuan, depending on profile.
- Native WebRTC AudioProcessing echo cancellation.
- Automatic acoustic startup-delay calibration.
- Fixed calibrated coarse AEC delay during calls; no competing live Go delay-control loop.
- Zero-lookahead elastic SIP-to-Baichuan talkback playout with bounded ± correction and soft residual discontinuities.
- Six grouped Home Assistant configuration sections plus the Call routes list placed immediately below Call, at Home Assistant's supported schema depth.
- Transparent PNG icon/logo without the former white outer canvas.
- `sip_registrar: auto` resolves the Home Assistant host's IPv4 default gateway at startup; a manual IP/DNS registrar remains supported.
- Fresh installs default the Reolink username to `admin`.
- `visitor_entity: auto` uses Home Assistant's compact enabled-entity registry view; ambiguous multi-doorbell setups require manual selection.
- The UI calls `dry_run` **Passive mode / Passivmodus**; the internal key remains unchanged for configuration compatibility.

Stable 1.x targets **amd64** Home Assistant hosts. The Version 2 beta adds **aarch64** for Home Assistant and **arm64** for standalone Docker/native Linux.

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
  door_call_enabled: true
  sip_username: "doorbell"
  sip_password: "change-me"
  sip_display_name: "Front Door"
  sip_codec_preference: pcma
  parallel_call_enabled: true
  parallel_username: "mobile-call"
  parallel_password: "change-me"
  sip_registrar_port: 5060
  sip_local_port: 5070
  parallel_local_port: 5071

audio:
  echo_cancellation_enabled: true
  webrtc_high_pass_filter_enabled: true
  webrtc_noise_suppression_enabled: true

call:
  incoming_calls_enabled: false
  incoming_allowed_callers:
    - "*"
  incoming_connection_tone_enabled: true
  debounce_seconds: 3
  ring_timeout_seconds: 30
  rtp_inactivity_timeout_seconds: 15
  max_call_duration_seconds: 300

call_routes:
  - id: default
    name: Standardroute
    visitor_entity: auto
    doorbell_number: "11"
    mobile_number_1: "01630000000"
    mobile_number_2: "01760000000"
    mobile_number_3: ""

live_image:
  fritzfon_live_image_enabled: true

diagnostics:
  dry_run: false
  log_level: info
```

For multiple buttons, edit the pre-created default route or add further entries
to the Call routes list displayed immediately below the Call section:

```yaml
call_routes:
  - id: wohnung_1
    name: Wohnung 1
    visitor_entity: binary_sensor.klingeltaste_wohnung_1
    doorbell_number: "11"
    mobile_number_1: "01630000000"
    mobile_number_2: "01510000000"
    mobile_number_3: ""
  - id: wohnung_2
    name: Wohnung 2
    visitor_entity: binary_sensor.klingeltaste_wohnung_2
    doorbell_number: "12"
    mobile_number_1: "01630000000"
    mobile_number_2: ""
    mobile_number_3: ""
```

Use stable lowercase IDs with letters, digits and underscores. Enter telephone
numbers directly in `mobile_number_1` through `mobile_number_3`. Leave
`doorbell_number` empty for a mobile-only route, or leave all mobile-number
fields empty for a FRITZ!Box-only route. At least one enabled call path is
required per route.

`nvr_channel_number` is deliberately **1-based** in the user interface. The startup adapter translates it to the internal Reolink channel representation without exposing the protocol-specific zero-based value.

### FRITZ!Fon live images

With `fritzfon_live_image_enabled: true`, the gateway exposes the existing read-only door JPEG and, in NVR or automatic mode, asks the Reolink `GetChannelstatus` API which channels are online. Open the app's Ingress page and find **FRITZ!Fon live images**. The first entry is the unchanged door link; below it, every detected channel has its own copy button and browser test. Channel URLs are stable and use the public 1-based NVR number, for example `/fritzfon/<token>/channel-1.jpg` and `/fritzfon/<token>/channel-2.jpg`. Restart the app after adding, removing or renaming NVR channels so the list is detected again.

In **Telephony → Telephony Devices**, edit the FRITZ!Box IP door intercom, select `http://` for **Live image**, and paste the displayed address **without** a scheme into the adjacent field. The values already end in `.jpg`, as required by FRITZ!OS. If automatic discovery fails or the host is a standalone camera, the configured primary image remains available and no SIP, audio or call-routing behavior changes.

All URLs share a separate random token stored with mode `0600` under `/data`; it remains stable across updates and backups. Treat every complete URL as a secret. It can fetch camera images but cannot call, hang up or use API v1, and requests from outside loopback, private or link-local networks are rejected. Camera/NVR credentials are never placed in a FRITZ!Box URL. Each channel independently prefers local HTTPS snapshot CGI, falls back to local HTTP and finally captures one frame from that channel's NVR RTSP substream. Requests for the same channel within 750 ms share one in-memory frame.

AVM documents JPEG/JPG, PNG and GIF image URLs and recommends roughly 240×320 through 480×640 pixels for FRITZ!Fon displays. This gateway fits every JPEG inside a 480×640 box without changing its aspect ratio. See AVM's [live-image instructions](https://fritz.com/apps/knowledge-base/fritz-box-7590/1602_live-bild-einer-ip-kamera-am-fritz-fon-anzeigen) and [IP door-intercom setup](https://fritz.com/apps/knowledge-base/fritz-box-4050/3513_ip-tursprechanlage-in-fritz-box-einrichten).

With `sip_registrar: auto`, the startup adapter reads the Home Assistant host's IPv4 routing table and uses its default-gateway address (commonly the FRITZ!Box). Set an IP address or DNS name instead to override auto-detection. Existing saved registrar values are not replaced during an update.

For a FRITZ!Box 4050 telephone system, configure the original account as an **IP door intercom** and the mobile account as **Telephone → LAN/WLAN (IP telephone)**. Each route's `doorbell_number` is the number assigned by the FRITZ!Box door-intercom setup; for example, `11` commonly represents doorbell button 1. Both registrations use the 4050 registrar address and port 5060, but normally listen locally on 5070 and 5071. Assign the required outgoing landline number to the mobile IP telephone. Each route accepts at most three unique mobile numbers. Set `door_call_enabled: false` for mobile-only operation; dormant door credentials are then not required.

The FRITZ!Box 6690 may remain a pure cable modem in this topology; it is not the SIP registrar or telephone system.

`visitor_entity: auto` on the pre-created default route queries Home Assistant's compact `config/entity_registry/list_for_display` view and selects the single enabled `binary_sensor` from platform `reolink` with translation key `visitor`. Renamed entity IDs are therefore supported. If none or more than one are enabled, startup asks for an explicit manual entity instead of guessing. Only one route may use `auto`; additional routes name their visitor sensor explicitly so that the mapping remains unambiguous. WebSocket frames and complete messages remain bounded to 16 MiB.

Set **Allow incoming SIP calls on all accounts** (`incoming_calls_enabled`) in the **Call** section to call the camera through either enabled gateway account. With a FRITZ!Box, dial the internal number assigned to the desired device. The first incoming call occupies the global media slot; a concurrent call to either account receives `486 Busy Here`. The option defaults to `false` so upgrades never begin auto-answering unexpectedly. Signalling is accepted only from the configured registrar IP and UDP port, and the normalized SIP caller user must match `incoming_allowed_callers`. RFC 4733 DTMF is negotiated and reported identically on both accounts. The compatibility value `*` permits every caller; replace it with the telephone numbers or internal extensions that should be accepted. Country-code variants are intentionally not inferred.

Before an accepted incoming call is answered, `incoming_connection_tone_enabled` plays the first four symbols (256 ms) of the existing acoustic marker through the actual Reolink talkback path. `rtp_inactivity_timeout_seconds` then ends either call direction when no valid negotiated RTP audio packet is received for the configured interval. If only internal calls should reach the camera, do not assign external incoming numbers to this IP telephone in the FRITZ!Box, because forwarded external calls also originate from the trusted registrar.

The separate Home Assistant integration consumes API v1 to provide status/caller sensors, a test-call button for every route, one hang-up button and the transient `reolink_sip_gateway_dtmf` event. Its `remote_number` is the normalized incoming caller or configured outgoing destination, while `call_id` scopes keypresses to one SIP dialog. DTMF handling is intentionally unchanged: route metadata may aid diagnostics, but all key meaning and actions remain in Home Assistant automations.

The **Passive mode / Passivmodus** toggle in **Operation & diagnostics / Betrieb & Diagnose** keeps the internal key `dry_run` for backwards compatibility. It monitors visitor events but suppresses SIP registration, outbound calls and the audible startup calibration marker.

## Companion integration API

Open the app's Ingress page after startup to copy the internal app hostname and generated API token. The integration builds `http://<app-hostname>:18099/api/v1` itself, verifies API version 1 and uses the stable installation UUID for its device and entity unique IDs. The token is generated once, stored with mode `0600` under `/data`, retained across app updates/backups and never written to the log.

The API is intentionally local: requests require `Authorization: Bearer <token>` and must originate from loopback, a private network or a link-local address. `/api/v1/events` pushes complete snapshots on real state changes and transient `dtmf` events for completed RFC 4733 keypresses. DTMF events have no SSE ID, do not alter status revisions and are not replayed; clients should still reconcile reconstructable state with `/api/v1/status` after reconnecting. The existing ingress-only `/api/status` remains available for the status page but is not the integration contract.

## Echo cancellation

At normal startup, the gateway can measure the acoustic Reolink loop delay by transmitting a coded speech-band marker and correlating the received audio. The resulting coarse delay is held fixed during each call. The native WebRTC AEC3 implementation remains responsible for its internal fine alignment and adaptive filtering.

This design is intentional: hardware testing showed that the former Go live delay tracker could converge to a different time base and degrade echo suppression. Since v0.5.7 that live controller is disabled in production.

## Configuration migration

v1.5.0 adds no option or data migration. In NVR/auto mode it creates `/data/fritzfon-live-image-catalog.json` automatically with mode `0600`; the file contains the last successful camera/channel mapping for the configured NVR and can be deleted safely to force a fresh catalog. Existing door and numbered image URLs remain valid.

v1.4.0 adds no option or data migration. When live images are enabled, NVR/auto mode performs read-only channel discovery at startup and derives the additional channel URLs from the existing persistent path token. The previous door URL and configured `nvr_channel_number` behavior are unchanged.

v1.3.1 only reorders the existing `live_image` group in the visible Home Assistant configuration. It introduces no option, default or data migration.

v1.3.0 adds the `live_image` group with `fritzfon_live_image_enabled: true`. The adapter supplies that default when an older configuration has no group; it does not rewrite or migrate existing Reolink, SIP, audio or route values. The independent image-path token is created automatically under `/data`.

v1.2.2 performs one guarded conversion to the direct route model:

- A v1.1 simple configuration becomes the editable `default` route while preserving its visitor sensor, doorbell number and up to three mobile numbers.
- A v1.2.0/1.2.1 route keeps its ID, name, visitor sensor and doorbell number; named target references are replaced with their corresponding numbers. Values entered directly into the former reference fields are preserved too.
- Retired simple-route fields and the named-target catalogue are removed only after the complete replacement options have been prepared.
- The existing persistent marker, compare-before-write guard and read-only normal-start behavior remain in effect.

v0.5.10 finalizes the grouped configuration introduced in v0.5.8:

- On a direct upgrade from older flat 0.5.x options, legacy values are imported once.
- A persistent migration marker is written only after the grouped state is confirmed.
- After that marker exists, grouped values are authoritative.
- Normal starts do **not** write configuration back to the Home Assistant Supervisor.
- A compare-before-write guard protects the one-time migration from overwriting a concurrent user edit.

## Hardware status

The NVR/Baichuan audio path has been developed and hardware-tested with a Reolink Video Doorbell PoE behind an RLN8-410 NVR. The primary FRITZ!Fon path and v1.4 multi-channel images have also been confirmed on the target FRITZ!Box 4050/FRITZ!Fon installation. v1.5's periodic refresh, camera-move URL and ring-prefetch extensions are covered by automated tests and await longer hardware observation. Other Reolink firmware/device combinations may differ; detailed diagnostics are useful when reporting compatibility issues.

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
go test -race -p 1 ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/gateway
```

The Docker image additionally builds a small native C++ helper against Debian's `libwebrtc-audio-processing-1` development package.

See [`reolink_sip_gateway/TESTING.md`](reolink_sip_gateway/TESTING.md) for the release regression checklist and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the media pipeline.

The implemented v1.2 routing and v1.3 live-image work, plus deliberately deferred multi-camera features, are recorded in [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Reporting issues

When opening an issue, include the app version, Reolink mode, relevant device/NVR model, and logs around startup or a call. **Remove passwords, SIP credentials, public phone numbers, tokens, and any other secrets before posting logs.**

## License

MIT. See [`LICENSE`](LICENSE). Third-party notices are in [`reolink_sip_gateway/THIRD-PARTY-NOTICES.md`](reolink_sip_gateway/THIRD-PARTY-NOTICES.md).
