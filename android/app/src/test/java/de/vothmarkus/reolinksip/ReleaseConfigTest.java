package de.vothmarkus.reolinksip;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;
import static org.junit.Assert.*;

public class ReleaseConfigTest {
    @Test public void alphaDestinationsMigrateWithoutChangingRouting() throws Exception {
        JSONObject old = new JSONObject().put("door_number", "12").put("mobile_1", "**610")
                .put("mobile_2", "+4912345").put("mobile_3", "**613");
        JSONArray routes = ConfigStore.routes(old);
        assertEquals(1, routes.length());
        assertEquals("default", routes.getJSONObject(0).getString("id"));
        assertEquals("12", routes.getJSONObject(0).getString("doorbell_number"));
        old.put("routes_json", routes.toString());
        old.put("mobile_1", "stale-alpha-value");
        JSONObject root = new JSONObject(ConfigStore.buildJson(old));
        assertEquals("**610", root.getJSONArray("call_routes").getJSONObject(0).getString("mobile_number_1"));
        assertEquals("+4912345", root.getJSONArray("call_routes").getJSONObject(0).getString("mobile_number_2"));
        assertEquals("**613", root.getJSONArray("call_routes").getJSONObject(0).getString("mobile_number_3"));
        assertEquals("default", root.getJSONObject("trigger").getString("route_id"));
    }

    @Test public void exportImportPreservesAllGatewaySettingsAndExactSecrets() throws Exception {
        JSONObject p = new JSONObject().put("device_mode", "nvr").put("nvr_channel", 7)
                .put("reolink_host", "192.0.2.50").put("reolink_user", "camerauser")
                .put("reolink_password", "  camera \"secret\"\\\n ").put("rtsp_port", 8554).put("baichuan_port", 9010)
                .put("sip_registrar", "192.0.2.1").put("sip_port", 5062).put("door_enabled", false)
                .put("sip_user", "door").put("sip_password", " door secret ").put("sip_local_port", 5074)
                .put("display_name", "Eingang").put("codec", "pcmu").put("parallel_enabled", true)
                .put("parallel_user", "parallel").put("parallel_password", " parallel secret ").put("parallel_port", 5075)
                .put("aec_enabled", true).put("high_pass", false).put("noise_suppression", true)
                .put("incoming_enabled", true).put("allowed_callers", "**610,**612").put("connection_tone", false)
                .put("debounce", 4).put("ring_timeout", 40).put("rtp_timeout", 17).put("max_duration", 500)
                .put("trigger_source", "manual").put("trigger_route", "garden")
                .put("live_image", true).put("image_port", 18098).put("dry_run", false).put("log_level", "debug");
        JSONArray routes = ConfigStore.routes(new JSONObject());
        routes.put(new JSONObject().put("id", "garden").put("name", "Garten").put("visitor_entity", "binary_sensor.garden")
                .put("doorbell_number", "12").put("mobile_number_1", "**611").put("mobile_number_2", "+491234567").put("mobile_number_3", ""));
        p.put("routes_json", routes.toString());
        String exported = ConfigStore.buildJson(p);
        JSONObject imported = ConfigStore.fromJson(exported, new JSONObject().put("start_on_boot", true).put("gateway_requested", true));
        assertEquals(jsonValue(new JSONObject(exported)), jsonValue(new JSONObject(ConfigStore.buildJson(imported))));
        assertTrue(imported.getBoolean("start_on_boot"));
        assertFalse(imported.has("gateway_requested"));
        assertFalse(exported.contains("start_on_boot"));
        assertEquals(p.getString("reolink_password"), imported.getString("reolink_password"));
        assertEquals("garden", imported.getString("trigger_route"));
        assertEquals("**611", ConfigStore.routes(imported).getJSONObject(1).getString("mobile_number_1"));
    }

    @Test public void explicitOffSurvivesRebootAndUpgradeAndLegacyAutostartStillWorks() throws Exception {
        JSONObject p = new JSONObject();
        assertFalse(ConfigStore.autoStart(p, false));
        assertFalse(ConfigStore.autoStart(p, true));
        p.put("start_on_boot", true);
        assertTrue(ConfigStore.autoStart(p, false));
        assertTrue(ConfigStore.autoStart(p, true));
        p.put("gateway_requested", false);
        assertFalse(ConfigStore.autoStart(p, false));
        assertFalse(ConfigStore.autoStart(p, true));
        p.put("gateway_requested", true).put("start_on_boot", false);
        assertFalse(ConfigStore.autoStart(p, false));
        assertTrue(ConfigStore.autoStart(p, true));
        p.put("start_on_boot", true);
        assertTrue(ConfigStore.autoStart(p, false));
    }

    @Test public void selectedTestRouteHonorsRuntimeAvailabilityAndManualModeIsNotDisconnected() throws Exception {
        JSONObject raw = new JSONObject().put("gateway", new JSONObject().put("trigger_source", "manual").put("trigger_connected", false))
                .put("sip", new JSONObject().put("parallel_call_enabled", true).put("parallel_registered", true))
                .put("call", new JSONObject()).put("routes", new JSONArray()
                        .put(new JSONObject().put("id", "default").put("test_call_available", false))
                        .put(new JSONObject().put("id", "garden").put("test_call_available", true)));
        assertFalse(GatewayStatus.routeAvailable(raw.toString(), "default"));
        assertTrue(GatewayStatus.routeAvailable(raw.toString(), "garden"));
        assertFalse(GatewayStatus.routeAvailable(raw.toString(), "deleted"));
        assertFalse(GatewayStatus.routeAvailable("", "garden"));
        assertTrue(GatewayStatus.overview(raw.toString(), "").contains("Klingeln: nur manuell"));
        assertFalse(GatewayStatus.overview(raw.toString(), "").contains("Verbindung wird aufgebaut"));
        assertTrue(GatewayStatus.overview(raw.toString(), "").contains("SIP Parallelruf: registriert"));
        assertTrue(GatewayStatus.summary("Gateway läuft", raw.toString(), "").contains("manueller Betrieb"));
    }

    // Android's JSONObject API does not provide JSON-java's similar(). Compare
    // all nested values structurally without relying on object-key order.
    private static Object jsonValue(Object value) throws Exception {
        if (value instanceof JSONObject) {
            JSONObject object = (JSONObject) value;
            java.util.Map<String, Object> result = new java.util.TreeMap<>();
            java.util.Iterator<String> keys = object.keys();
            while (keys.hasNext()) { String key = keys.next(); result.put(key, jsonValue(object.get(key))); }
            return result;
        }
        if (value instanceof JSONArray) {
            JSONArray array = (JSONArray) value;
            java.util.List<Object> result = new java.util.ArrayList<>();
            for (int i = 0; i < array.length(); i++) result.add(jsonValue(array.get(i)));
            return result;
        }
        return value;
    }
}
