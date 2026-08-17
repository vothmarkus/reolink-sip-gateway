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

# The Call UI keeps capability switches first and timing details afterwards.
# config.yaml contains the order twice (options and schema); translations and
# the public test fixture must expose the identical sequence.
CALL_ORDER=(visitor_entity: incoming_calls_enabled: incoming_allowed_callers: incoming_connection_tone_enabled: debounce_seconds: ring_timeout_seconds: rtp_inactivity_timeout_seconds: max_call_duration_seconds:)
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 1 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/config.yaml" 2 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/de.yaml" 1 "${CALL_ORDER[@]}"
assert_key_order "${ROOT}/reolink_sip_gateway/translations/en.yaml" 1 "${CALL_ORDER[@]}"
CALL_JSON_ORDER=('"visitor_entity":' '"incoming_calls_enabled":' '"incoming_allowed_callers":' '"incoming_connection_tone_enabled":' '"debounce_seconds":' '"ring_timeout_seconds":' '"rtp_inactivity_timeout_seconds":' '"max_call_duration_seconds":')
assert_key_order "${ROOT}/reolink_sip_gateway/testdata/options.valid.json" 1 "${CALL_JSON_ORDER[@]}"

# Fresh-install defaults exposed by the HA adapter.
fresh="$(normalize_public_options '{}' false)"
assert_eq "$(jq -r .reolink.reolink_username <<<"${fresh}")" "admin"
assert_eq "$(jq -r .sip.sip_registrar <<<"${fresh}")" "auto"
assert_eq "$(jq -r .call.visitor_entity <<<"${fresh}")" "auto"
assert_eq "$(jq -r .call.incoming_calls_enabled <<<"${fresh}")" "false"
assert_eq "$(jq -c .call.incoming_allowed_callers <<<"${fresh}")" '["*"]'
assert_eq "$(jq -r .call.incoming_connection_tone_enabled <<<"${fresh}")" "true"
assert_eq "$(jq -r .call.rtp_inactivity_timeout_seconds <<<"${fresh}")" "15"

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
auto_visitor_options="$(jq -c '.call.visitor_entity="auto" | .sip.sip_registrar="pbx.local"' <<<"${normalized}")"
build_runtime_options "${auto_visitor_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .visitor_entity <<<"${runtime}")" "binary_sensor.front_door_visitor"

# A manual visitor entity bypasses registry discovery completely.
resolve_reolink_visitor_entity(){ echo called > "${TMP}/resolver-called"; return 1; }
manual_visitor_options="$(jq -c '.call.visitor_entity="binary_sensor.custom_visitor" | .sip.sip_registrar="pbx.local"' <<<"${normalized}")"
build_runtime_options "${manual_visitor_options}"
runtime="$(cat /tmp/reolink-sip-gateway-runtime-options.json)"
assert_eq "$(jq -r .visitor_entity <<<"${runtime}")" "binary_sensor.custom_visitor"
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
assert_eq "$(jq -r .call.visitor_entity <<<"${existing_normalized}")" "binary_sensor.existing_visitor"

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
GROUPED_OPTIONS_MARKER="${TMP}/marker"
WRITES=0
LAST_WRITE=''
supervisor_options_write(){ WRITES=$((WRITES+1)); LAST_WRITE="$1"; return 0; }

grouped='{"reolink":{"reolink_host":"10.0.0.2","reolink_username":"u","reolink_password":"p","reolink_mode":"nvr","nvr_channel_number":2,"reolink_rtsp_port":554,"baichuan_port":9000},"sip":{"sip_registrar":"10.0.0.1","sip_registrar_port":5060,"sip_username":"s","sip_password":"x","sip_destination":"100","sip_local_port":5070,"sip_display_name":"Door","sip_codec_preference":"pcma"},"audio":{"echo_cancellation_enabled":true,"webrtc_high_pass_filter_enabled":true,"webrtc_noise_suppression_enabled":true},"call":{"visitor_entity":"binary_sensor.door","incoming_calls_enabled":false,"incoming_allowed_callers":["*"],"incoming_connection_tone_enabled":true,"debounce_seconds":3,"ring_timeout_seconds":30,"rtp_inactivity_timeout_seconds":15,"max_call_duration_seconds":300},"diagnostics":{"log_level":"info","dry_run":false}}'
printf '%s\n' "${grouped}" > "${OPTIONS_FILE}"
normalized="$(normalize_public_options "${grouped}" true)"
assert_eq "$(jq -r .reolink.reolink_username <<<"${normalized}")" "u"
assert_eq "$(jq -r .sip.sip_registrar <<<"${normalized}")" "10.0.0.1"
complete_grouping_migration "${grouped}" "${normalized}"
[[ -e "${GROUPED_OPTIONS_MARKER}" ]]
[[ "${WRITES}" -eq 0 ]]

# Once marked, normal operation is read-only even if input mismatches.
printf '%s\n' '{"changed":true}' > "${OPTIONS_FILE}"
complete_grouping_migration '{"old":true}' '{"new":true}'
[[ "${WRITES}" -eq 0 ]]

# Legacy migration writes exactly once and then marks completion.
rm -f "${GROUPED_OPTIONS_MARKER}"
WRITES=0
printf '%s\n' "${legacy}" > "${OPTIONS_FILE}"
normalized="$(normalize_public_options "${legacy}" true)"
complete_grouping_migration "${legacy}" "${normalized}"
[[ "${WRITES}" -eq 1 ]]
[[ -e "${GROUPED_OPTIONS_MARKER}" ]]
assert_eq "$(jq -r .sip.sip_username <<<"${LAST_WRITE}")" "legacy-user"
assert_eq "$(jq -r .reolink.nvr_channel_number <<<"${LAST_WRITE}")" "2"

# A concurrent user edit cancels the stale write and leaves migration pending.
rm -f "${GROUPED_OPTIONS_MARKER}"
WRITES=0
printf '%s\n' '{"sip_username":"new-user"}' > "${OPTIONS_FILE}"
complete_grouping_migration "${legacy}" "${normalized}"
[[ "${WRITES}" -eq 0 ]]
[[ ! -e "${GROUPED_OPTIONS_MARKER}" ]]

printf 'config adapter/migration tests: PASS\n'
