# Architecture

## Purpose

Reolink SIP Gateway connects three domains:

1. Home Assistant supplies the doorbell/visitor trigger.
2. SIP provides call setup and telephone audio.
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
        +--> AEC render reference at actual Reolink playout
        |
        v
16 kHz conversion / Reolink IMA-ADPCM
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

The camera receive path includes a smoother/PLL to turn bursty decoder output into a stable media clock. SIP-to-Baichuan talkback uses a bounded FIFO so latency cannot grow without limit. Current 0.5.x behavior keeps that transport unchanged; more advanced concealment or cross-fading is intentionally deferred to a later functional release.
