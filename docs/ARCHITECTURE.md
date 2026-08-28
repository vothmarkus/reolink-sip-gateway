# Architecture

## Purpose

Reolink SIP Gateway connects three domains:

1. Home Assistant supplies one or more doorbell/visitor triggers and, through the companion integration, consumes status and sends route-specific call-control commands.
2. SIP provides outbound and optional incoming call setup plus telephone audio.
3. Reolink CGI/RTSP provides camera images, while RTSP/ONVIF or Baichuan provides doorbell receive/talkback audio.

The gateway intentionally keeps profile selection, long-delay alignment, and real-time media transport separate.

## Startup

```text
Home Assistant grouped options
        |
        v
one-time legacy migration (only when needed)
        |
        v
UI -> flat runtime adapter
        |
        +--> public NVR channel 2 -> internal channel 1
        |
        v
load/create persistent API and live-image identities
        |
        +--> ingress + authenticated /api/v1 + secret JPEG + health
        |
        v
Reolink profile detection / fixed profile
        |
        v
acoustic AEC startup calibration
        |
        v
SIP registration + HA visitor subscription
```

### Configuration boundary

The Home Assistant UI exposes six groups plus the top-level `call_routes` list immediately after the Call group. The sixth group contains the FRITZ!Fon live-image toggle. A list of route mappings already reaches Home Assistant's supported nesting limit, so it cannot be embedded one level deeper inside `call`. The Go runtime deliberately retains the proven flat configuration contract. This keeps UI evolution away from the media implementation.

v0.5.10 uses a persistent marker for the grouped-layout migration. Before the marker exists, old flat values may take precedence over Supervisor-materialized defaults so direct upgrades preserve user configuration. After migration, grouped values are authoritative and normal starts are read-only with respect to Supervisor options.

v0.5.11 resolves the public `sip_registrar: auto` value at this boundary. The adapter reads the host-visible IPv4 routing table and replaces `auto` only in the private flat runtime snapshot with the selected default-gateway address. Manual registrar values bypass this step, and no resolved address is written back to Home Assistant.

v0.5.13 resolves `visitor_entity: auto` at the same boundary. A short-lived helper command uses the existing Supervisor-authenticated Home Assistant WebSocket transport to request the compact, enabled-only `config/entity_registry/list_for_display` view. It decodes `ei`, `pl`, and `tk`, filters for `binary_sensor` entries with `platform=reolink` and `translation_key=visitor`, and writes the concrete entity ID only into the private runtime snapshot. Zero or multiple matches fail explicitly; manual entity IDs bypass discovery. Frames and complete messages are hard-limited to 16 MiB.

v0.7.0 adds `call.incoming_calls_enabled` at this boundary. The default is `false`, including when an older grouped configuration has no such key. The adapter passes an explicit user opt-in unchanged to the flat Go runtime.

v0.8.0 adds the caller list, pre-answer indication and RTP watchdog in the same Call group. Existing v0.7 installations receive the explicit compatibility list `["*"]`; the gateway still fails closed if incoming calls are enabled with an empty list. The grouped-to-flat adapter preserves list order and values without interpreting telephone numbers.

v0.9.0 deliberately adds no option. A stable UUID and random bearer token are runtime identity, persisted under `/data` with mode `0600`; they are neither part of the grouped public options nor the flat media configuration.

v1.0.0 also adds no option. RFC 4733 reception is enabled only when the SIP
offer/answer negotiates `telephone-event/8000`; all interpretation remains on
the Home Assistant side of the API boundary.

v1.1.0 added one optional second SIP identity on the same registrar. In that
release, the door identity stayed limited to one dialog and retained all
incoming-call handling, while the mobile identity permitted at most the number
of configured mobile destinations, hard-limited to three. Separate local UDP
ports keep both registrations and their transactions unambiguous.

v1.1.1 adds `sip.door_call_enabled`, defaulting to `true` for upgrade
compatibility. Door-only, mobile-only and combined live configurations are
valid; at least one identity must be enabled. Incoming-call handling is copied
to every enabled identity, while one application-level controller remains the
authority for the single Reolink media path. The API's legacy
`sip.registered` field therefore represents any enabled registered identity.

v1.2.2 makes `call_routes` the only public trigger/target model. A fresh
installation starts with one editable `default` route. Each route holds its
visitor entity, optional doorbell number and up to three direct mobile numbers;
Go validates and resolves it to runtime call legs. The shell adapter performs a
one-time, compare-before-write migration of the v1.1 simple fields and the
v1.2.0/1.2.1 target catalogue, preserving existing route identity and values.
A second persistent marker makes subsequent starts read-only again. No route
contains a camera, NVR channel, media, DTMF or opener selector.

v1.3.0 adds `live_image.fritzfon_live_image_enabled`, defaulting to `true`.
The adapter passes it as a flat boolean without altering route or media values.
A separate random path token is persisted under `/data` with mode `0600`; it
is not accepted as an integration API bearer token.

## Home Assistant integration boundary

The companion integration talks only to the versioned local HTTP API. It cannot construct SIP messages, reserve RTP ports, open Reolink sessions or mutate the media pipeline.

- `/api/v1/info` is the compatibility handshake: API version, gateway version, stable instance UUID and additive capability names.
- `/api/v1/status` maps the internal snapshot to a purpose-built v1 DTO. Its additive route catalogue exposes only stable IDs, display names and command availability; telephone numbers stay behind the gateway boundary.
- `/api/v1/events` carries complete `status` snapshots plus transient `dtmf` events. Each keypress carries an exact normalized `remote_number` (incoming caller or configured outgoing destination) and the SIP `call_id`, allowing downstream input to remain scoped to one remote party and one call. The store assigns monotonically increasing revisions only when the comparable snapshot actually changes; DTMF has no SSE ID and never changes or replays status. Bounded subscriber queues prevent a slow client from back-pressuring real-time call work.
- `/api/v1/routes/{route_id}/test`, the compatible `/api/v1/calls/test`, and `/api/v1/calls/hangup` pass requests into a command interface that remains unavailable until startup has completed. Request contexts never become call lifetimes; accepted test calls use the process call context. The companion integration creates one stable test-call button for every advertised route.
- A 256-bit bearer token protects every v1 route. Constant-time comparison and a private/local source-address boundary protect the command surface; ingress-only legacy routes retain their stricter proxy/loopback rule.

One Store remains the source of truth. The integration can use SSE for low-latency changes and `GET status` after reconnect as reconciliation, without creating a second state machine.

## FRITZ!Fon live-image boundary

The status server registers exactly one tokenized path ending in `.jpg` when
the option is enabled. It accepts only `GET` and `HEAD` from loopback, private
or link-local source addresses, returns validated JPEG bytes with no-cache
headers, and exposes neither a directory nor a query-based credential. The
FRITZ!Box therefore receives a read-only image capability, not the more
powerful API v1 bearer token.

The source is deliberately independent of call control and the exclusive
audio session. It tries Reolink's snapshot CGI over local HTTPS first and local
HTTP second. Cross-host redirects are refused. If neither result is a valid
JPEG, a bounded FFmpeg process captures one frame from the existing configured
RTSP URL. Transport errors are classified before logging so URLs containing
camera credentials cannot leak.

Decoded dimensions and total pixel count are bounded before allocation.
Images outside a 480×640 bounding box are resized with their aspect ratio preserved; a
750 ms in-memory cache coalesces nearly simultaneous phone requests. The
server-facing provider interface contains only `FetchJPEG(context)`, keeping
HTTP authorization and response behavior separate from camera transport.

## SIP call control

### Outbound first-answer-wins fork

One resolved route produces a list of independent call legs:

- zero or one door-station leg on the original SIP account, when enabled and registered;
- zero to three mobile legs on the optional second account, when enabled and registered.

Every leg reserves its own dynamic RTP socket before sending `INVITE`. A small
account-level call map keys dialogs by `Call-ID`; the door account capacity is
one and the mobile-account capacity is at most three. The first fully
established dialog is selected atomically. Its RTP socket becomes the only
input to the existing Reolink `media.Session`; no camera or audio path is
created for a ringing leg.

Selecting the winner cancels the shared dialing context. A leg in provisional
state sends `CANCEL` and ACKs its final non-2xx response. A `200 OK` that crosses
the decision is always ACKed because it already created a SIP dialog, then the
losing dialog receives `BYE`. Loser cleanup runs concurrently with winner media
startup, while the global call slot remains reserved until that cleanup has
finished or reached its bounded timeout.

The existing outbound path reserves a dynamic RTP socket, places an authenticated `INVITE` after a visitor event and starts the shared media session after the remote endpoint answers.

Each enabled account's optional incoming path acts as a small SIP user agent server on its own registered UDP socket:

1. Only an `INVITE` from the configured registrar address and port is eligible.
2. The normalized SIP `From` user must match the exact caller allowlist before SDP parsing, dialog reservation or camera work.
3. The SDP offer is reduced to one supported PCMA/PCMU stream according to the configured preference.
4. The dialog and its single-call slot are reserved and `100 Trying` is returned.
5. The application reserves its RTP socket and starts the same Reolink `media.Session` used by outbound calls.
6. If enabled, the first four symbols of the shared acoustic marker are paced through the opened Reolink talkback before the receive side and SIP answer are exposed.
7. Only `media.Session.Ready()` permits the `200 OK` SDP answer. Setup failure is returned as `480`; a concurrent call to either account receives `486` from the shared controller.
8. `ACK`, pre-answer `CANCEL`, in-dialog `BYE`, 2xx retransmission and missing-ACK cleanup close the SIP transaction and media lifetime deterministically.

All route entities share one Home Assistant trigger subscription but retain independent edge/debounce state. Visitor events carry their route ID into the same threadsafe call controller as route test calls and accepted incoming INVITEs from both accounts. Its cancelable context spans dialing, media preparation, the active conversation and cleanup. The first event reserves the slot; another visitor route is rejected without queueing, while a later INVITE on either UDP socket receives `486 Busy Here`. The slot is released only after the runner returns, so an API hang-up cannot make a second call overlap delayed SIP/RTSP/Baichuan cleanup. Each SIP client retains its own dialog-level busy checks as a second boundary. Negotiated RFC 4733 DTMF follows the winning dialog and is handled identically for either account and call direction; route metadata changes diagnostics, not DTMF semantics.

Both live talkback readers attach an RTP watchdog to valid packets of the negotiated codec; expiry returns a media error and the common call controller performs local SIP cleanup. It does not inspect PCM level and therefore does not confuse silence with a broken transport.

The companion integration owns proper registered status/caller entities and call-control buttons; the gateway only provides their source data and commands. In v1.0 it additionally translates each validated `dtmf` SSE item into `reolink_sip_gateway_dtmf`. It does not create DTMF state or action logic. Every call continues to use the one startup-resolved Reolink profile.

## NVR media path

```text
Doorbell/NVR microphone
        |
        | Baichuan sub / AAC 16 kHz
        v
AAC decode
        |
        v
camera playout smoother / virtual media clock
        |
        v
PCM 8 kHz
        |
        +--> WebRTC AEC capture
        |
        v
G.711 packetizer -> SIP RTP -> telephone

telephone
        |
        | SIP RTP G.711 8 kHz
        v
PCM
        |
        v
16 kHz conversion / bounded elastic FIFO playout
        |
        v
Reolink IMA-ADPCM
        |
        +--> AEC render reference at actual Reolink write
        |
        v
Baichuan Live Talk -> NVR/doorbell speaker
```

## Standalone media path

Receive audio uses RTSP and FFmpeg. Talkback uses the ONVIF RTSP backchannel when the device/profile supports it.

## Echo cancellation

The large Reolink acoustic/transport delay is measured during startup with a coded speech-band marker. Go then selects the corresponding historical render frame before each capture frame reaches WebRTC AudioProcessing.

The native helper processes 10 ms / 8 kHz frames:

```text
ProcessReverseStream(render)
set_stream_delay_ms(0)
ProcessStream(capture)
```

`set_stream_delay_ms(0)` is intentional because the large delay has already been compensated by selecting the matching historical render frame.

### Why the Go live tracker is disabled

Hardware testing found that the former live correlation tracker could report a stable candidate roughly 200 ms away from the startup-aligned value. Following that candidate degraded ERLE and raised residual echo likelihood. With the startup delay held fixed, long calls showed stable echo suppression while AEC3 handled the remaining internal fine alignment. Production live Go delay tracking therefore remains disabled.

## Buffering

The camera receive path includes a smoother/PLL to turn bursty decoder output into a stable media clock. This camera-to-SIP controller is independent of talkback and remains unchanged in v0.6.0.

SIP-to-Baichuan talkback retains its four-Reolink-block FIFO and drop-oldest overflow rule so latency cannot grow without limit. When a block is due, v0.6.0 consumes adaptively from that FIFO and maps the result onto exactly one negotiated block:

- up to 2% time expansion when the queue is short or its supply trend predicts a shortage,
- up to 3% time compression while draining a backlog or repaying a temporary reserve,
- a causal 5 ms half-Hann fade at residual silence boundaries,
- a causal 5 ms boundary splice after samples had to be dropped on overflow.

There is no lookahead, startup prebuffer, extra timer tick or larger FIFO. Every block is still written on the existing Baichuan cadence. The AEC render reference is reconstructed from the encoded ADPCM block after this processing and observed at the actual transport write, so it continues to describe what the doorbell really received.
