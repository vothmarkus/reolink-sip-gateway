package de.vothmarkus.reolinksip;

import android.content.Context;
import android.content.SharedPreferences;
import org.json.JSONArray;
import org.json.JSONObject;

final class ConfigStore {
    private static final String PREFS = "gateway";

    static SharedPreferences prefs(Context context) {
        return context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    static String get(Context c, String key, String def) {
        return prefs(c).getString(key, def);
    }

    static boolean getBool(Context c, String key, boolean def) {
        return prefs(c).getBoolean(key, def);
    }

    static void save(Context c, String host, String user, String password, int channel,
                     String registrar, String sipUser, String sipPassword,
                     String doorNumber, boolean dryRun, boolean startOnBoot) {
        prefs(c).edit()
                .putString("reolink_host", host)
                .putString("reolink_user", user)
                .putString("reolink_password", password)
                .putInt("nvr_channel", channel)
                .putString("sip_registrar", registrar)
                .putString("sip_user", sipUser)
                .putString("sip_password", sipPassword)
                .putString("door_number", doorNumber)
                .putBoolean("dry_run", dryRun)
                .putBoolean("start_on_boot", startOnBoot)
                .apply();
    }

    static String buildJson(Context c) throws Exception {
        SharedPreferences p = prefs(c);
        JSONObject root = new JSONObject();
        root.put("schema_version", 2);

        JSONObject reolink = new JSONObject();
        reolink.put("reolink_host", p.getString("reolink_host", ""));
        reolink.put("reolink_username", p.getString("reolink_user", "admin"));
        reolink.put("reolink_password", p.getString("reolink_password", ""));
        reolink.put("reolink_mode", "nvr");
        reolink.put("nvr_channel_number", p.getInt("nvr_channel", 1));
        reolink.put("reolink_rtsp_port", 554);
        reolink.put("baichuan_port", 9000);
        root.put("reolink", reolink);

        JSONObject sip = new JSONObject();
        sip.put("sip_registrar", p.getString("sip_registrar", ""));
        sip.put("sip_registrar_port", 5060);
        sip.put("door_call_enabled", true);
        sip.put("sip_username", p.getString("sip_user", ""));
        sip.put("sip_password", p.getString("sip_password", ""));
        sip.put("sip_local_port", 5070);
        sip.put("sip_display_name", "Haustür");
        sip.put("sip_codec_preference", "pcma");
        sip.put("parallel_call_enabled", false);
        sip.put("parallel_username", "");
        sip.put("parallel_password", "");
        sip.put("parallel_local_port", 5071);
        root.put("sip", sip);

        JSONObject audio = new JSONObject();
        audio.put("echo_cancellation_enabled", false);
        audio.put("webrtc_high_pass_filter_enabled", false);
        audio.put("webrtc_noise_suppression_enabled", false);
        root.put("audio", audio);

        JSONObject call = new JSONObject();
        call.put("incoming_calls_enabled", false);
        call.put("incoming_allowed_callers", new JSONArray().put("*"));
        call.put("incoming_connection_tone_enabled", true);
        call.put("debounce_seconds", 3);
        call.put("ring_timeout_seconds", 30);
        call.put("rtp_inactivity_timeout_seconds", 15);
        call.put("max_call_duration_seconds", 300);
        root.put("call", call);

        JSONObject route = new JSONObject();
        route.put("id", "default");
        route.put("name", "Standardroute");
        route.put("visitor_entity", "");
        route.put("doorbell_number", p.getString("door_number", "11"));
        route.put("mobile_number_1", "");
        route.put("mobile_number_2", "");
        route.put("mobile_number_3", "");
        root.put("call_routes", new JSONArray().put(route));

        JSONObject trigger = new JSONObject();
        trigger.put("source", "baichuan");
        trigger.put("route_id", "default");
        root.put("trigger", trigger);

        JSONObject liveImage = new JSONObject();
        liveImage.put("fritzfon_live_image_enabled", false);
        root.put("live_image", liveImage);

        JSONObject diagnostics = new JSONObject();
        diagnostics.put("dry_run", p.getBoolean("dry_run", true));
        diagnostics.put("log_level", "info");
        root.put("diagnostics", diagnostics);
        return root.toString();
    }

    private ConfigStore() {}
}
