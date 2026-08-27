#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN="${ROOT}/reolink_sip_gateway/rootfs/etc/services.d/reolink-sip-gateway/run"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

bash -n "${RUN}"
awk '/^PUBLIC_OPTIONS=/{exit} {print}' "${RUN}" > "${TMP}/functions.sh"

bashio::log.info(){ :; }
bashio::log.warning(){ :; }
bashio::log.error(){ :; }
# shellcheck source=/dev/null
source "${TMP}/functions.sh"

assert_eq() {
    if [[ "$1" != "$2" ]]; then
        printf 'assertion failed: got [%s], expected [%s]\n' "$1" "$2" >&2
        exit 1
    fi
}

assert_key_order() {
    local file=${1}
    local occurrence=${2}
    shift 2
    local previous=0 key line
    for key in "$@"; do
        line="$(grep -nF "${key}" "${file}" | sed -n "${occurrence}p" | cut -d: -f1)"
        if [[ -z "${line}" ]] || (( line <= previous )); then
            printf 'key order assertion failed in %s for occurrence %s at [%s]\n' "${file}" "${occurrence}" "${key}" >&2
            exit 1
        fi
        previous=${line}
    done
}

assert_group_sequence() {
    local file=${1}
    local pattern=${2}
    local expected=${3}
    local actual

    actual="$(grep -E "${pattern}" "${file}" | sed -E 's/^[[:space:]]*"?([^":]+)"?:.*/\1/' | paste -sd, -)"
    assert_eq "${actual}" "${expected}"
}

# The Call UI keeps capability switches first and timing details afterwards.
# config.yaml contains the order twice (options and schema); translations and
# the public test fixture must expose the identical sequence.
CALL_ORDER=(incoming_calls_enabled: incoming_allowed_callers: incoming_connection_tone_enabled: debounce_seconds: ring_timeout_seconds: rtp_inactivity_timeout_seconds: max_call_duration_seconds:)
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 1 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 2 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/de.yaml" 1 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/en.yaml" 1 "${CALL_ORDER[@]}"
CALL_JSON_ORDER=('"incoming_calls_enabled":' '"incoming_allowed_callers":' '"incoming_connection_tone_enabled":' '"debounce_seconds":' '"ring_timeout_seconds":' '"rtp_inactivity_timeout_seconds":' '"max_call_duration_seconds":')
assert_key_order "${ROOT}/reolink_sip_gateway/testdata/options.valid.json" 1 "${CALL_JSON_ORDER[@]}"

# Home Assistant supports a list of route mappings at top-level depth, but not
# nested one more level inside the Call mapping. Keep it immediately adjacent
# to Call in every ordered public representation.
assert_group_sequence "${ROOT}/reolink_sip_gateway/config.yaml" '^  (call|call_routes|diagnostics):$' 'call,call_routes,diagnostics,call,call_routes,diagnostics'
assert_group_sequence "${ROOT}/reolink_sip_gateway/translations/de.yaml" '^  (call|call_routes|diagnostics):$' 'call,call_routes,diagnostics'
assert_group_sequence "${ROOT}/reolink_sip_gateway/translations/en.yaml" '^  (call|call_routes|diagnostics):$' 'call,call_routes,diagnostics'
assert_group_sequence "${ROOT}/reolink_sip_gateway/testdata/options.valid.json" '^  "(call|call_routes|diagnostics)":' 'call,call_routes,diagnostics'

# Door- and mobile-account fields stay compact; the three advanced transport
# ports form one final block in every public representation.
SIP_ORDER=(sip_registrar: door_call_enabled: sip_username: sip_password: sip_display_name: sip_codec_preference: parallel_call_enabled: parallel_username: parallel_password: sip_registrar_port: sip_local_port: parallel_local_port:)
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 1 "${SIP_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 2 "${SIP_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/de.yaml" 1 "${SIP_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/en.yaml" 1 "${SIP_ORDER[@]}"
SIP_JSON_ORDER=('"sip_registrar":' '"door_call_enabled":' '"sip_username":' '"sip_password":' '"sip_display_name":' '"sip_codec_preference":' '"parallel_call_enabled":' '"parallel_username":' '"parallel_password":' '"sip_registrar_port":' '"sip_local_port":' '"parallel_local_port":')
assert_key_order "${ROOT}/reolink_sip_gateway/testdata/options.valid.json" 1 "${SIP_JSON_ORDER[@]}"

# Fresh-install defaults exposed by the HA adapter.
fresh="$(normalize_public_options '{}' false)"
assert_eq "$(jq -r .reolink.reolink_username <<<"${fresh}")" "admin"
assert_eq "$(jq -r .sip.sip_registrar <<<"${fresh}")" "auto"
assert_eq "$(jq -r .sip.door_call_enabled <<<"${fresh}")" "true"
assert_eq "$(jq -r .call.incoming_calls_enabled <<<"${fresh}")" "false"
assert_eq "$(jq -c .call.incoming_allowed_callers <<<"${fresh}")" '["*"]'
assert_eq "$(jq -r .call.incoming_connection_tone_enabled <<<"${fresh}")" "true"
assert_eq "$(jq -r .call.rtp_inactivity_timeout_seconds <<<"${fresh}")" "15"
assert_eq "$(jq -r .sip.parallel_call_enabled <<<"${fresh}")" "false"
assert_eq "$(jq -r .sip.parallel_local_port <<<"${fresh}")" "5071"
assert_eq "$(jq -r '.call_routes | length' <<<"${fresh}")" "1"
assert_eq "$(jq -r .call_routes[0].id <<<"${fresh}")" "default"
assert_eq "$(jq -r .call_routes[0].name <<<"${fresh}")" "Standardroute"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${fresh}")" "auto"
assert_eq "$(jq -r .call_routes[0].doorbell_number <<<"${fresh}")" "11"
assert_eq "$(jq -c '[.call_routes[0].mobile_number_1,.call_routes[0].mobile_number_2,.call_routes[0].mobile_number_3]' <<<"${fresh}")" '["","",""]'
assert_eq "$(jq -r 'has("mobile_targets") or (.sip | has("parallel_destinations") or has("sip_destination")) or (.call | has("visitor_entity"))' <<<"${fresh}")" "false"

# The checked-in public fixture must translate exactly to the checked-in flat
# runtime fixture. This guards field loss as the UI model evolves.
fixture="$(cat "${ROOT}/reolink_sip_gateway/testdata/options.valid.json")"
build_runtime_options "${fixture}"
fixture_runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
expected_runtime="$(cat "${ROOT}/reolink_sip_gateway/testdata/options.runtime.valid.json")"
assert_eq "$(jq -cS . <<<"${fixture_runtime}")" "$(jq -cS . <<<"${expected_runtime}")"

# Direct legacy upgrade: old flat values must beat newly materialized grouped defaults once.
legacy='{"sip_username":"legacy-user","sip_password":"legacy-pass","nvr_channel":1,"sip":{"sip_username":"","sip_password":""},"reolink":{"nvr_channel_number":1}}'
normalized="$(normalize_public_options "${legacy}" true)"
assert_eq "$(jq -r .sip.sip_username <<<"${normalized}")" "legacy-user"
assert_eq "$(jq -r .sip.sip_password <<<"${normalized}")" "legacy-pass"
assert_eq "$(jq -r .reolink.nvr_channel_number <<<"${normalized}")" "2"

# After migration, grouped values are authoritative even if stale flat keys survive.
mixed='{"sip_username":"STALE","nvr_channel":0,"sip":{"sip_username":"current-user","sip_registrar":"10.0.0.1"},"reolink":{"nvr_channel_number":2}}'
normalized="$(normalize_public_options "${mixed}" false)"
assert_eq "$(jq -r .sip.sip_username <<<"${normalized}")" "current-user"
assert_eq "$(jq -r .reolink.nvr_channel_number <<<"${normalized}")" "2"

# Runtime boundary remains legacy-compatible for UI NVR channel 2.
SUPERVISOR_TOKEN=test-token
resolve_reolink_visitor_entity(){ printf '%s\n' 'binary_sensor.test_visitor'; }
build_runtime_options "${normalized}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .nvr_channel <<<"${runtime}")" "1"
assert_eq "$(jq -r .reolink_stream_path <<<"${runtime}")" "/Preview_02_sub"
assert_eq "$(jq -r .echo_cancellation_search_window_ms <<<"${runtime}")" "300"
assert_eq "$(jq -r .incoming_calls_enabled <<<"${runtime}")" "false"
assert_eq "$(jq -c .incoming_allowed_callers <<<"${runtime}")" '["*"]'
assert_eq "$(jq -r .incoming_connection_tone_enabled <<<"${runtime}")" "true"
assert_eq "$(jq -r .rtp_inactivity_timeout_seconds <<<"${runtime}")" "15"
assert_eq "$(jq -r .door_call_enabled <<<"${runtime}")" "true"
assert_eq "$(jq -r .parallel_call_enabled <<<"${runtime}")" "false"
assert_eq "$(jq -r .parallel_local_port <<<"${runtime}")" "5071"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${runtime}")" "binary_sensor.test_visitor"
assert_eq "$(jq -r .call_routes[0].doorbell_number <<<"${runtime}")" "11"
assert_eq "$(jq -r 'has("mobile_targets") or has("parallel_destinations") or has("sip_destination") or has("visitor_entity")' <<<"${runtime}")" "false"

# v1.2.2 direct-number routes remain at Home Assistant's supported
# list-of-mapping depth, reach the flat runtime unchanged and bypass visitor
# auto-discovery when every route names a concrete entity.
rm -f "${TMP}/resolver-called"
resolve_reolink_visitor_entity(){ echo called > "${TMP}/resolver-called"; return 1; }
matrix_options="$(jq -c '
  .call_routes=[
      {"id":"wohnung_1","name":"Wohnung 1","visitor_entity":"binary_sensor.klingel_1","doorbell_number":"11","mobile_number_1":"0163","mobile_number_2":"0176","mobile_number_3":""},
      {"id":"wohnung_2","name":"Wohnung 2","visitor_entity":"binary_sensor.klingel_2","doorbell_number":"12","mobile_number_1":"0176","mobile_number_2":"","mobile_number_3":""}
    ]
' <<<"${normalized}")"
build_runtime_options "${matrix_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r '.call_routes[0].doorbell_number' <<<"${runtime}")" "11"
assert_eq "$(jq -r '.call_routes[0].mobile_number_2' <<<"${runtime}")" "0176"
assert_eq "$(jq -r '.call_routes[1].mobile_number_1' <<<"${runtime}")" "0176"
[[ ! -e "${TMP}/resolver-called" ]]
resolve_reolink_visitor_entity(){ printf '%s\n' 'binary_sensor.test_visitor'; }

# v1.1.1 can disable the door account and retain the mobile account as the
# only SIP route. Empty dormant door credentials survive the adapter unchanged.
mobile_only_options="$(jq -c '.sip.door_call_enabled=false | .sip.sip_username="" | .sip.sip_password="" | .sip.parallel_call_enabled=true | .sip.parallel_username="mobile" | .sip.parallel_password="secret" | .call_routes[0].doorbell_number="" | .call_routes[0].mobile_number_1="0163" | .call_routes[0].mobile_number_2="0176" | .call_routes[0].mobile_number_3="0151"' <<<"${normalized}")"
build_runtime_options "${mobile_only_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .door_call_enabled <<<"${runtime}")" "false"
assert_eq "$(jq -r .sip_username <<<"${runtime}")" ""
assert_eq "$(jq -r .parallel_call_enabled <<<"${runtime}")" "true"
assert_eq "$(jq -c '[.call_routes[0].mobile_number_1,.call_routes[0].mobile_number_2,.call_routes[0].mobile_number_3]' <<<"${runtime}")" '["0163","0176","0151"]'

# Second-account settings and all three route numbers reach the private flat
# runtime unchanged. Go performs final credential and route validation.
parallel_options="$(jq -c '.sip.parallel_call_enabled=true | .sip.parallel_username="mobile" | .sip.parallel_password="secret" | .call_routes[0].mobile_number_1="0163" | .call_routes[0].mobile_number_2="0176" | .call_routes[0].mobile_number_3="0151" | .sip.parallel_local_port=5071' <<<"${normalized}")"
build_runtime_options "${parallel_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .parallel_call_enabled <<<"${runtime}")" "true"
assert_eq "$(jq -r .parallel_username <<<"${runtime}")" "mobile"
assert_eq "$(jq -r .parallel_password <<<"${runtime}")" "secret"
assert_eq "$(jq -c '[.call_routes[0].mobile_number_1,.call_routes[0].mobile_number_2,.call_routes[0].mobile_number_3]' <<<"${runtime}")" '["0163","0176","0151"]'
assert_eq "$(jq -r .parallel_local_port <<<"${runtime}")" "5071"

# v0.8 incoming-call controls reach the flat runtime unchanged.
incoming_options="$(jq -c '.call.incoming_calls_enabled=true | .call.incoming_allowed_callers=["0123 456789","**620"] | .call.incoming_connection_tone_enabled=false | .call.rtp_inactivity_timeout_seconds=25' <<<"${normalized}")"
build_runtime_options "${incoming_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .incoming_calls_enabled <<<"${runtime}")" "true"
assert_eq "$(jq -c .incoming_allowed_callers <<<"${runtime}")" '["0123 456789","**620"]'
assert_eq "$(jq -r .incoming_connection_tone_enabled <<<"${runtime}")" "false"
assert_eq "$(jq -r .rtp_inactivity_timeout_seconds <<<"${runtime}")" "25"

# Standalone ignores the public NVR channel and keeps the proven 0/Preview_01 mapping.
standalone="$(jq -c '.reolink.reolink_mode="standalone" | .reolink.nvr_channel_number=9' <<<"${normalized}")"
build_runtime_options "${standalone}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .nvr_channel <<<"${runtime}")" "0"
assert_eq "$(jq -r .reolink_stream_path <<<"${runtime}")" "/Preview_01_sub"

# SIP registrar "auto" resolves the HA host's IPv4 default gateway at runtime.
ROUTE_FILE="${TMP}/route"
cat > "${ROUTE_FILE}" <<'EOF'
Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
eth0 00000000 0100000A 0003 0 0 200 00000000 0 0 0
eth1 00000000 0101A8C0 0003 0 0 100 00000000 0 0 0
tun0 00000000 00000000 0001 0 0 1 00000000 0 0 0
EOF
IPV4_ROUTE_FILE="${ROUTE_FILE}"
auto_options="$(jq -c '.sip.sip_registrar="auto"' <<<"${normalized}")"
build_runtime_options "${auto_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .sip_registrar <<<"${runtime}")" "192.168.1.1"

# Visitor entity "auto" resolves through the Home Assistant entity-registry helper.
SUPERVISOR_TOKEN=test-token
resolve_reolink_visitor_entity(){ printf '%s\n' 'binary_sensor.front_door_visitor'; }
auto_visitor_options="$(jq -c '.call_routes[0].visitor_entity="auto" | .sip.sip_registrar="pbx.local"' <<<"${normalized}")"
build_runtime_options "${auto_visitor_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${runtime}")" "binary_sensor.front_door_visitor"

# A manual visitor entity bypasses registry discovery completely.
resolve_reolink_visitor_entity(){ echo called > "${TMP}/resolver-called"; return 1; }
manual_visitor_options="$(jq -c '.call_routes[0].visitor_entity="binary_sensor.custom_visitor" | .sip.sip_registrar="pbx.local"' <<<"${normalized}")"
build_runtime_options "${manual_visitor_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${runtime}")" "binary_sensor.custom_visitor"
[[ ! -e "${TMP}/resolver-called" ]]

# Resolver ambiguity/failure is surfaced instead of guessing.
resolve_reolink_visitor_entity(){ echo 'multiple enabled Reolink visitor binary sensors found: binary_sensor.a, binary_sensor.b' >&2; return 2; }
if build_runtime_options "${auto_visitor_options}"; then
    echo 'expected visitor auto resolution to fail on ambiguous registry result' >&2
    exit 1
fi
resolve_reolink_visitor_entity(){ printf '%s\n' 'binary_sensor.test_visitor'; }

# Existing manual 0.5.11 visitor values remain authoritative on upgrade.
existing_visitor='{"call":{"visitor_entity":"binary_sensor.existing_visitor"}}'
existing_normalized="$(normalize_public_options "${existing_visitor}" false)"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${existing_normalized}")" "binary_sensor.existing_visitor"

# A manually configured registrar is never replaced and does not require a route lookup.
IPV4_ROUTE_FILE="${TMP}/does-not-exist"
manual_options="$(jq -c '.sip.sip_registrar="pbx.example.local"' <<<"${normalized}")"
build_runtime_options "${manual_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .sip_registrar <<<"${runtime}")" "pbx.example.local"

# Auto mode fails explicitly if the host has no usable IPv4 gateway.
cat > "${ROUTE_FILE}" <<'EOF'
Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
tun0 00000000 00000000 0001 0 0 1 00000000 0 0 0
EOF
IPV4_ROUTE_FILE="${ROUTE_FILE}"
if build_runtime_options "${auto_options}"; then
    echo 'expected auto registrar resolution to fail without an IPv4 gateway' >&2
    exit 1
fi
unset IPV4_ROUTE_FILE

# One-time persistence semantics.
OPTIONS_FILE="${TMP}/options.json"
GROUPED_OPTIONS_MARKER="${TMP}/grouped-marker"
ROUTING_OPTIONS_MARKER="${TMP}/routing-marker"
WRITES=0
LAST_WRITE=''
supervisor_options_write(){ WRITES=$((WRITES+1)); LAST_WRITE="$1"; return 0; }

# A fresh v1.2.2 configuration is marked without an unnecessary write.
printf '%s\n' "${fresh}" > "${OPTIONS_FILE}"
complete_option_migrations "${fresh}" "${fresh}"
[[ -e "${GROUPED_OPTIONS_MARKER}" ]]
[[ -e "${ROUTING_OPTIONS_MARKER}" ]]
[[ "${WRITES}" -eq 0 ]]

# Once both migrations are marked, normal operation is read-only.
printf '%s\n' '{"changed":true}' > "${OPTIONS_FILE}"
complete_option_migrations '{"old":true}' '{"new":true}'
[[ "${WRITES}" -eq 0 ]]

# A v1.1 simple route becomes one editable default route without losing its
# visitor entity, doorbell number or any of its three mobile destinations.
old_simple='{"reolink":{"reolink_host":"10.0.0.2","reolink_username":"u","reolink_password":"p","reolink_mode":"nvr","nvr_channel_number":2,"reolink_rtsp_port":554,"baichuan_port":9000},"sip":{"sip_registrar":"10.0.0.1","door_call_enabled":true,"sip_username":"s","sip_password":"x","sip_destination":"12","sip_display_name":"Door","sip_codec_preference":"pcma","parallel_call_enabled":true,"parallel_username":"m","parallel_password":"y","parallel_destinations":["0163","0176","0151"],"sip_registrar_port":5060,"sip_local_port":5070,"parallel_local_port":5071},"audio":{"echo_cancellation_enabled":true,"webrtc_high_pass_filter_enabled":true,"webrtc_noise_suppression_enabled":true},"call":{"visitor_entity":"binary_sensor.legacy_door","incoming_calls_enabled":false,"incoming_allowed_callers":["*"],"incoming_connection_tone_enabled":true,"debounce_seconds":3,"ring_timeout_seconds":30,"rtp_inactivity_timeout_seconds":15,"max_call_duration_seconds":300},"call_routes":[{"id":"default","name":"Standardroute","visitor_entity":"auto","doorbell_number":"11","mobile_number_1":"","mobile_number_2":"","mobile_number_3":""}],"diagnostics":{"log_level":"info","dry_run":false}}'
rm -f "${GROUPED_OPTIONS_MARKER}" "${ROUTING_OPTIONS_MARKER}"
WRITES=0
printf '%s\n' "${old_simple}" > "${OPTIONS_FILE}"
normalized="$(normalize_public_options "${old_simple}" true)"
complete_option_migrations "${old_simple}" "${normalized}"
[[ "${WRITES}" -eq 1 ]]
[[ -e "${GROUPED_OPTIONS_MARKER}" ]]
[[ -e "${ROUTING_OPTIONS_MARKER}" ]]
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${LAST_WRITE}")" "binary_sensor.legacy_door"
assert_eq "$(jq -r .call_routes[0].doorbell_number <<<"${LAST_WRITE}")" "12"
assert_eq "$(jq -c '[.call_routes[0].mobile_number_1,.call_routes[0].mobile_number_2,.call_routes[0].mobile_number_3]' <<<"${LAST_WRITE}")" '["0163","0176","0151"]'
assert_eq "$(jq -r 'has("mobile_targets") or (.sip | has("parallel_destinations") or has("sip_destination")) or (.call | has("visitor_entity"))' <<<"${LAST_WRITE}")" "false"

# An old explicit door-only route remains authoritative even if empty legacy
# target fields were omitted by an older Supervisor serialization.
old_door_route="$(jq -c '.call_routes=[{"id":"wohnung_1","name":"Wohnung 1","visitor_entity":"binary_sensor.klingel_1","doorbell_number":"11"}]' <<<"${old_simple}")"
normalized_door_route="$(normalize_public_options "${old_door_route}" false)"
assert_eq "$(jq -r .call_routes[0].id <<<"${normalized_door_route}")" "wohnung_1"
assert_eq "$(jq -r .call_routes[0].visitor_entity <<<"${normalized_door_route}")" "binary_sensor.klingel_1"
assert_eq "$(jq -r .call_routes[0].doorbell_number <<<"${normalized_door_route}")" "11"

# A v1.2.1 matrix resolves catalogue references to numbers; an intuitively
# entered direct value in an old reference field is preserved as well.
old_matrix='{"sip":{"sip_registrar":"10.0.0.1","door_call_enabled":true,"sip_username":"s","sip_password":"x","sip_destination":"99","sip_display_name":"Door","sip_codec_preference":"pcma","parallel_call_enabled":true,"parallel_username":"m","parallel_password":"y","parallel_destinations":["ignored"],"sip_registrar_port":5060,"sip_local_port":5070,"parallel_local_port":5071},"mobile_targets":[{"id":"markus","name":"Markus","destination":"0163"}],"call_routes":[{"id":"wohnung_1","name":"Wohnung 1","visitor_entity":"binary_sensor.klingel_1","doorbell_number":"11","mobile_target_1":"markus","mobile_target_2":"0176","mobile_target_3":""}],"audio":{},"call":{},"diagnostics":{}}'
rm -f "${ROUTING_OPTIONS_MARKER}"
WRITES=0
printf '%s\n' "${old_matrix}" > "${OPTIONS_FILE}"
normalized="$(normalize_public_options "${old_matrix}" false)"
complete_option_migrations "${old_matrix}" "${normalized}"
[[ "${WRITES}" -eq 1 ]]
[[ -e "${ROUTING_OPTIONS_MARKER}" ]]
assert_eq "$(jq -r .call_routes[0].mobile_number_1 <<<"${LAST_WRITE}")" "0163"
assert_eq "$(jq -r .call_routes[0].mobile_number_2 <<<"${LAST_WRITE}")" "0176"

# A concurrent user edit cancels the stale write and leaves migration pending.
rm -f "${ROUTING_OPTIONS_MARKER}"
WRITES=0
printf '%s\n' '{"sip_username":"new-user"}' > "${OPTIONS_FILE}"
complete_option_migrations "${old_matrix}" "${normalized}"
[[ "${WRITES}" -eq 0 ]]
[[ ! -e "${ROUTING_OPTIONS_MARKER}" ]]

printf 'config adapter/migration tests: PASS\n'
