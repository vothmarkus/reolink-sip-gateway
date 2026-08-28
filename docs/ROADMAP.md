# Roadmap

## v1.4: FRITZ!Fon images for every NVR channel — implemented

At startup the gateway uses Reolink's read-only channel-status API to detect
online NVR channels and publishes a stable tokenized `.jpg` URL for each one.
The Ingress page presents sorted 1-based channel numbers, optional camera names,
copy controls and browser tests.

Implemented invariants:

- the v1.3 `/fritzfon/<token>.jpg` primary/door URL remains unchanged;
- exact `/fritzfon/<token>/channel-N.jpg` resources exist only for detected
  online channels plus the configured primary fallback;
- discovery errors never block startup, SIP registration, audio or the primary
  image;
- every channel uses its own snapshot source, RTSP fallback and short cache;
- the existing token, local-network boundary, JPEG validation and credential
  isolation apply to every resource;
- no configuration migration, API v1 change, camera selector in call routes or
  additional Reolink audio session.

## v1.3: FRITZ!Fon live image — implemented

The gateway exposes a current Reolink frame through a stable local URL ending
in `.jpg`, ready for the FRITZ!Box IP door-intercom live-image field. The
Ingress page provides the exact address and browser test. Camera credentials
are used only toward Reolink and never appear in the client-facing URL, which
uses an independent 192-bit path token.

Implemented invariants:

- Reolink snapshot CGI over HTTPS first, HTTP second, then one-frame
  RTSP/FFmpeg fallback;
- validated JPEG output fitted inside 480×640 while preserving aspect ratio;
- a short in-memory request-coalescing cache and explicit no-cache responses;
- a read-only `GET`/`HEAD` endpoint whose token has no API v1 call-control
  authority;
- one toggle independent of call routes, SIP registration and the exclusive
  Reolink audio session.

## v1.2: multiple doorbell buttons and routing matrix — implemented

As finalized in v1.2.2, each route maps one Home Assistant visitor entity to an
optional FRITZ!Box doorbell number and up to three direct mobile numbers:

```yaml
call_routes:
  - id: wohnung_1
    name: Wohnung 1
    visitor_entity: binary_sensor.klingeltaste_wohnung_1
    doorbell_number: "11"
    mobile_number_1: "0163..."
    mobile_number_2: "0151..."
    mobile_number_3: ""
  - id: wohnung_2
    name: Wohnung 2
    visitor_entity: binary_sensor.klingeltaste_wohnung_2
    doorbell_number: "12"
    mobile_number_1: "0163..."
    mobile_number_2: ""
    mobile_number_3: ""
```

The UI pre-creates one editable default route and places the route list directly
below the Call group. Upgrades convert the former simple fields or named target
references once and losslessly into this representation.

Implemented invariants:

- one optional door SIP account and one optional mobile SIP account;
- zero or one FRITZ!Box doorbell number plus zero to three direct mobile numbers per
  route, with at least one enabled call path;
- one Home Assistant WebSocket subscription for all route entities and an
  independent debounce state per route;
- fail-fast validation for duplicate or invalid IDs, entities, doorbell
  numbers and duplicate destinations within one route;
- one global call controller: the first route or incoming call owns the single
  Reolink media path and later concurrent calls are rejected, never queued;
- current/last route metadata and a route catalogue in API v1, without phone
  numbers or credentials;
- one route-specific test-call button per route in the companion integration;
- unchanged RFC 4733 DTMF semantics on both SIP accounts.

## Later candidates

Multiple simultaneous camera/audio sessions, route-specific camera selection,
route-specific door openers and queued calls remain intentionally outside the
current architecture. They require a different resource and permission model
rather than another field in the routing matrix. v1.4's additional read-only
snapshots do not allocate media sessions and therefore do not change that
boundary.
