# Roadmap

## v1.2: multiple doorbell buttons and routing matrix

The v1.1 SIP fork deliberately accepts a generic list of call legs, but its
public Home Assistant configuration still has one visitor entity and at most
one door destination. v1.2 can extend only the routing layer while retaining
the two optional SIP identities, first-answer-wins controller and single
Reolink media session.

The planned data model separates reusable mobile targets from doorbell-button
routes:

```yaml
mobile_targets:
  markus: "0163..."
  maria: "0176..."
  bereitschaft: "0151..."

door_buttons:
  - id: wohnung_1
    visitor_entity: binary_sensor.klingeltaste_wohnung_1
    door_destination: "**610"
    mobile_targets: [markus, bereitschaft]

  - id: wohnung_2
    visitor_entity: binary_sensor.klingeltaste_wohnung_2
    door_destination: "**611"
    mobile_targets: [maria]
```

This is a bipartite matrix: each button selects exactly one FRITZ!Box
door-mode destination and zero to three entries from the shared mobile-target
list. Different door destinations let the FRITZ!Box interpret calls as
different doorbell buttons, while the mobile matrix determines which external
numbers ring for each apartment.

Implementation constraints for v1.2:

- retain at most one door SIP account and one mobile SIP account; routes with a
  FRITZ!Box door-button destination require the door account, while mobile-only
  routes remain valid;
- keep the v1.1 maximum of three simultaneous mobile legs per button event;
- subscribe to all configured Home Assistant entities and preserve the entity
  ID as the route key;
- debounce each button independently;
- reject duplicate entity IDs, route IDs, door destinations and unknown mobile
  target references during startup;
- migrate the v1.1 single `visitor_entity`, `sip_destination` and
  `parallel_destinations` values into one default route without changing its
  behavior;
- expose the active/last route ID additively through API v1 without putting
  telephone numbers or routing secrets into logs;
- continue allowing only one winning Reolink conversation globally.

The route may later gain a media/camera selector, but that is intentionally
outside the first v1.2 scope. Multi-button routing should first reuse the one
already configured Reolink door audio path.
