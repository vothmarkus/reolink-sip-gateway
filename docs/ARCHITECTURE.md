# Architecture

## Purpose

Reolink SIP Gateway connects three domains:

1. Home Assistant supplies the doorbell/visitor trigger for outbound calls.
2. SIP provides outbound and optional incoming call setup plus telephone audio.
3. Reolink RTSP/ONVIF or Baichuan provides doorbell receive/talkback audio.

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
Reolink profile detection / fixed profile
        |
        v
acoustic AEC startup calibration
        |
        v
SIP registration + HA visitor subscription
```

### Configuration boundary

The Home Assistant UI exposes five groups. The Go runtime deliberately retains the proven flat configuration contract. This keeps UI evolution away from the media implementation.

v0.5.10 uses a persistent marker for the grouped-layout migration. Before the marker exists, old flat values may take precedence over Supervisor-materialized defaults so direct upgrades preserve user configuration. After migration, grouped values are authoritative and normal starts are read-only with respect to Supervisor options.

v0.5.11 resolves the public `sip_registrar: auto` value at this boundary. The adapter reads the host-visible IPv4 routing table and replaces `auto` only in the private flat runtime snapshot with the selected default-gateway address. Manual registrar values bypass this step, and no resolved address is written back to Home Assistant.

v0.5.13 resolves `visitor_entity: auto` at the same boundary. A short-lived helper command uses the existing Supervisor-authenticated Home Assistant WebSocket transport to request the compact, enabled-only `config/entity_registry/list_for_display` view. It decodes `ei`, `pl`, and `tk`, filters for `binary_sensor` entries with `platform=reolink` and `translation_key=visitor`, and writes the concrete entity ID only into the private runtime snapshot. Zero or multiple matches fail explicitly; manual entity IDs bypass discovery. Frames and complete messages are hard-limited to 16 MiB.

v0.7.0 adds `call.incoming_calls_enabled` at this boundary. The default is `false`, including when an older grouped configuration has no such key. The adapter passes an explicit user opt-in unchanged to the flat Go runtime.

## SIP call control

The existing outbound path reserves a dynamic RTP socket, places an authenticated `INVITE` after a visitor event and starts the shared media session after the remote endpoint answers.

The optional incoming path acts as a small SIP user agent server on the same registered UDP socket:

1. Only an `INVITE` from the configured registrar address and port is eligible.
2. The SDP offer is reduced to one supported PCMA/PCMU stream according to the configured preference.
3. The dialog and its single-call slot are reserved and `100 Trying` is returned.
4. The application reserves its RTP socket and starts the same Reolink `media.Session` used by outbound calls.
5. Only `media.Session.Ready()` permits the `200 OK` SDP answer. Setup failure is returned as `480`; a concurrent call receives `486`.
6. `ACK`, pre-answer `CANCEL`, in-dialog `BYE`, 2xx retransmission and missing-ACK cleanup close the SIP transaction and media lifetime deterministically.

Inbound and outbound setup share one mutual-exclusion slot. DTMF and multi-camera routing are deliberately outside the v0.7.0 call-control layer; every accepted incoming call uses the one startup-resolved Reolink profile.

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
