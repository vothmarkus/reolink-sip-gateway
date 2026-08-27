# Roadmap

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

Multiple cameras, separate media paths, route-specific door openers and queued
calls are intentionally outside v1.2. They require a different resource and
permission model rather than another field in the routing matrix.
