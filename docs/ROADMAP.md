# Roadmap

## v1.2: multiple doorbell buttons and routing matrix — implemented

v1.2 separates reusable mobile targets from call routes. Each route maps one
Home Assistant visitor entity to an optional FRITZ!Box doorbell number and up
to three optional mobile targets:

```yaml
mobile_targets:
  - id: markus
    name: Markus
    destination: "0163..."
  - id: bereitschaft
    name: Bereitschaft
    destination: "0151..."

call_routes:
  - id: wohnung_1
    name: Wohnung 1
    visitor_entity: binary_sensor.klingeltaste_wohnung_1
    doorbell_number: "11"
    mobile_target_1: markus
    mobile_target_2: bereitschaft
    mobile_target_3: ""
  - id: wohnung_2
    name: Wohnung 2
    visitor_entity: binary_sensor.klingeltaste_wohnung_2
    doorbell_number: "12"
    mobile_target_1: markus
    mobile_target_2: ""
    mobile_target_3: ""
```

The two-list UI is the routing matrix: phone numbers are entered once under
**Mobile targets**, while **Call routes** reference their stable IDs. Empty
`mobile_targets` and `call_routes` lists retain the v1.1.1 single-route
configuration without migration work.

Implemented invariants:

- one optional door SIP account and one optional mobile SIP account;
- zero or one FRITZ!Box doorbell number plus zero to three mobile targets per
  route, with at least one enabled call path;
- one Home Assistant WebSocket subscription for all route entities and an
  independent debounce state per route;
- fail-fast validation for duplicate or invalid IDs, entities, doorbell
  numbers, destinations and target references;
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
