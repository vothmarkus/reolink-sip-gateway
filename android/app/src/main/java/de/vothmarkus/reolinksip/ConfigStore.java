package de.vothmarkus.reolinksip;

import android.content.Context;
import android.content.SharedPreferences;
import org.json.JSONArray;
import org.json.JSONObject;
import java.util.Iterator;

final class ConfigStore {
    static SharedPreferences prefs(Context c) {
        return c.getSharedPreferences("gateway", Context.MODE_PRIVATE);
    }

    static boolean getBool(Context c, String key, boolean def) {
        return prefs(c).getBoolean(key, def);
    }

    static JSONObject read(Context c) {
        JSONObject values = new JSONObject(prefs(c).getAll());
        values.remove("gateway_requested"); // Runtime intent must never be overwritten by an old form.
        return values;
    }

    static void requestRun(Context c, boolean enabled) {
        if (!prefs(c).edit().putBoolean("gateway_requested", enabled).commit())
            throw new IllegalStateException("Gateway-Schaltzustand konnte nicht gespeichert werden");
    }

    static boolean autoStart(JSONObject preferences, boolean packageUpdated) {
        boolean requested = preferences.optBoolean("gateway_requested", preferences.optBoolean("start_on_boot", false));
        return requested && (packageUpdated || preferences.optBoolean("start_on_boot", false));
    }

    // Keep existing alpha installations on their NVR configuration. Only a new
    // setup defaults to a direct camera; changing modes never erases the NVR channel.
    static String deviceMode(JSONObject p) {
        return p.optString("device_mode", p.optString("reolink_host", "").trim().isEmpty() ? "direct" : "nvr");
    }

    static void save(Context c, JSONObject values) throws Exception {
        SharedPreferences.Editor e = prefs(c).edit();
        Iterator<String> keys = values.keys();
        while (keys.hasNext()) {
            String key = keys.next();
            if (key.equals("gateway_requested")) continue;
            Object value = values.get(key);
            if (value instanceof Boolean) e.putBoolean(key, (Boolean) value);
            else if (value instanceof Number) e.putInt(key, ((Number) value).intValue());
            else e.putString(key, value.toString());
        }
        if (!e.commit()) throw new IllegalStateException("Einstellungen konnten nicht gespeichert werden");
    }

    static String buildJson(Context c) throws Exception { return buildJson(read(c)); }

    static String buildJson(JSONObject p) throws Exception {
        JSONObject root = new JSONObject().put("schema_version", 2);
        String mode = deviceMode(p);
        if (!mode.equals("direct") && !mode.equals("nvr")) throw new IllegalArgumentException("Unbekannter Kameramodus");
        root.put("reolink", new JSONObject()
                .put("reolink_host", p.optString("reolink_host", "").trim())
                .put("reolink_username", p.optString("reolink_user", "admin").trim())
                .put("reolink_password", p.optString("reolink_password", ""))
                .put("reolink_mode", mode)
                .put("nvr_channel_number", mode.equals("direct") ? 1 : p.optInt("nvr_channel", 1))
                .put("reolink_rtsp_port", p.optInt("rtsp_port", 554))
                .put("baichuan_port", p.optInt("baichuan_port", 9000)));
        root.put("sip", new JSONObject()
                .put("sip_registrar", p.optString("sip_registrar", "").trim())
                .put("sip_registrar_port", p.optInt("sip_port", 5060))
                .put("door_call_enabled", p.optBoolean("door_enabled", true))
                .put("sip_username", p.optString("sip_user", "").trim())
                .put("sip_password", p.optString("sip_password", ""))
                .put("sip_local_port", p.optInt("sip_local_port", 5070))
                .put("sip_display_name", p.optString("display_name", "Haustür").trim())
                .put("sip_codec_preference", p.optString("codec", "pcma"))
                .put("parallel_call_enabled", p.optBoolean("parallel_enabled", false))
                .put("parallel_username", p.optString("parallel_user", "").trim())
                .put("parallel_password", p.optString("parallel_password", ""))
                .put("parallel_local_port", p.optInt("parallel_port", 5071)));
        root.put("audio", new JSONObject()
                .put("echo_cancellation_enabled", p.optBoolean("aec_enabled", false))
                .put("webrtc_high_pass_filter_enabled", p.optBoolean("aec_enabled", false) && p.optBoolean("high_pass", true))
                .put("webrtc_noise_suppression_enabled", p.optBoolean("aec_enabled", false) && p.optBoolean("noise_suppression", true)));
        JSONArray callers = new JSONArray();
        for (String value : p.optString("allowed_callers", "*").split("[,;\\s]+")) {
            if (!value.isEmpty()) callers.put(value);
        }
        root.put("call", new JSONObject()
                .put("incoming_calls_enabled", p.optBoolean("incoming_enabled", false))
                .put("incoming_allowed_callers", callers)
                .put("incoming_connection_tone_enabled", p.optBoolean("connection_tone", true))
                .put("debounce_seconds", p.optInt("debounce", 3))
                .put("ring_timeout_seconds", p.optInt("ring_timeout", 30))
                .put("rtp_inactivity_timeout_seconds", p.optInt("rtp_timeout", 15))
                .put("max_call_duration_seconds", p.optInt("max_duration", 300)));
        root.put("call_routes", routes(p));
        root.put("trigger", new JSONObject().put("source", p.optString("trigger_source", "baichuan"))
                .put("route_id", p.optString("trigger_route", "default")));
        root.put("live_image", new JSONObject().put("fritzfon_live_image_enabled", p.optBoolean("live_image", false))
                .put("http_port", p.optInt("image_port", 18099)));
        root.put("diagnostics", new JSONObject().put("dry_run", p.optBoolean("dry_run", true))
                .put("log_level", p.optString("log_level", "info")));
        return root.toString();
    }

    static JSONArray routes(JSONObject p) throws Exception {
        if (p.has("routes_json")) return new JSONArray(p.getString("routes_json"));
        JSONObject route = new JSONObject().put("id", "default").put("name", "Haustür")
                .put("visitor_entity", "").put("doorbell_number", p.optString("door_number", "11").trim());
        for (int i = 1; i <= 3; i++) route.put("mobile_number_" + i, p.optString("mobile_" + i, "").trim());
        return new JSONArray().put(route);
    }

    // Input is the fully defaulted, strictly validated schema-2 document returned
    // by Mobilebridge.normalizeConfig. Android device preferences stay local.
    static JSONObject fromJson(String normalized, JSONObject local) throws Exception {
        JSONObject root = new JSONObject(normalized);
        if (root.getInt("schema_version") != 2) throw new IllegalArgumentException("Konfigurationsversion wird nicht unterstützt");
        JSONObject p = new JSONObject();
        copy(root.getJSONObject("reolink"), p, new String[][]{
                {"reolink_host", "reolink_host"}, {"reolink_username", "reolink_user"}, {"reolink_password", "reolink_password"},
                {"reolink_mode", "device_mode"}, {"nvr_channel_number", "nvr_channel"}, {"reolink_rtsp_port", "rtsp_port"}, {"baichuan_port", "baichuan_port"}});
        copy(root.getJSONObject("sip"), p, new String[][]{
                {"sip_registrar", "sip_registrar"}, {"sip_registrar_port", "sip_port"}, {"door_call_enabled", "door_enabled"},
                {"sip_username", "sip_user"}, {"sip_password", "sip_password"}, {"sip_local_port", "sip_local_port"},
                {"sip_display_name", "display_name"}, {"sip_codec_preference", "codec"}, {"parallel_call_enabled", "parallel_enabled"},
                {"parallel_username", "parallel_user"}, {"parallel_password", "parallel_password"}, {"parallel_local_port", "parallel_port"}});
        copy(root.getJSONObject("audio"), p, new String[][]{
                {"echo_cancellation_enabled", "aec_enabled"}, {"webrtc_high_pass_filter_enabled", "high_pass"},
                {"webrtc_noise_suppression_enabled", "noise_suppression"}});
        JSONObject call = root.getJSONObject("call");
        copy(call, p, new String[][]{
                {"incoming_calls_enabled", "incoming_enabled"}, {"incoming_connection_tone_enabled", "connection_tone"},
                {"debounce_seconds", "debounce"}, {"ring_timeout_seconds", "ring_timeout"},
                {"rtp_inactivity_timeout_seconds", "rtp_timeout"}, {"max_call_duration_seconds", "max_duration"}});
        JSONArray callers = call.optJSONArray("incoming_allowed_callers");
        StringBuilder allowed = new StringBuilder();
        if (callers != null) for (int i = 0; i < callers.length(); i++) {
            if (i > 0) allowed.append(", ");
            allowed.append(callers.getString(i));
        }
        p.put("allowed_callers", allowed.toString());
        p.put("routes_json", root.getJSONArray("call_routes").toString());
        copy(root.getJSONObject("trigger"), p, new String[][]{{"source", "trigger_source"}, {"route_id", "trigger_route"}});
        copy(root.getJSONObject("live_image"), p, new String[][]{{"fritzfon_live_image_enabled", "live_image"}, {"http_port", "image_port"}});
        copy(root.getJSONObject("diagnostics"), p, new String[][]{{"dry_run", "dry_run"}, {"log_level", "log_level"}});
        p.put("start_on_boot", local.optBoolean("start_on_boot", false));
        return p;
    }

    private static void copy(JSONObject source, JSONObject target, String[][] mapping) throws Exception {
        for (String[] pair : mapping) target.put(pair[1], source.get(pair[0]));
    }

    static String redact(Context c, String text) {
        JSONObject p = read(c);
        for (String key : new String[]{"reolink_password", "sip_password", "parallel_password"}) {
            String secret = p.optString(key, "");
            if (!secret.isEmpty()) text = text.replace(secret, "***");
        }
        return text;
    }
    private ConfigStore() {}
}
