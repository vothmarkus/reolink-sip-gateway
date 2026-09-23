package de.vothmarkus.reolinksip;

import org.json.JSONObject;
import org.junit.Test;
import static org.junit.Assert.*;

public class ConfigStoreTest {
    @Test public void newSetupUsesDirectCameraAndOldSetupKeepsNVR() throws Exception {
        assertEquals("direct", ConfigStore.deviceMode(new JSONObject()));
        JSONObject old = new JSONObject().put("reolink_host", "192.0.2.50").put("nvr_channel", 7);
        JSONObject root = new JSONObject(ConfigStore.buildJson(old));
        assertEquals("nvr", root.getJSONObject("reolink").getString("reolink_mode"));
        assertEquals(7, root.getJSONObject("reolink").getInt("nvr_channel_number"));
        old.put("device_mode", "direct");
        root = new JSONObject(ConfigStore.buildJson(old));
        assertEquals("direct", root.getJSONObject("reolink").getString("reolink_mode"));
        assertEquals(1, root.getJSONObject("reolink").getInt("nvr_channel_number"));
        assertEquals(7, old.getInt("nvr_channel"));
    }
    @Test public void callFeaturesAndSecretsSurviveConversion() throws Exception {
        JSONObject p = new JSONObject().put("reolink_password", "  camera \"secret\"  ")
                .put("incoming_enabled", true).put("allowed_callers", "**610, **611;**612")
                .put("parallel_enabled", true).put("parallel_user", "phone2")
                .put("parallel_password", " sip secret ").put("mobile_1", "**612")
                .put("mobile_2", "+4912345").put("mobile_3", "**613")
                .put("dry_run", false).put("ring_timeout", 45).put("max_duration", 120);
        JSONObject root = new JSONObject(ConfigStore.buildJson(p));
        assertEquals("  camera \"secret\"  ", root.getJSONObject("reolink").getString("reolink_password"));
        assertTrue(root.getJSONObject("call").getBoolean("incoming_calls_enabled"));
        assertEquals(3, root.getJSONObject("call").getJSONArray("incoming_allowed_callers").length());
        assertTrue(root.getJSONObject("sip").getBoolean("parallel_call_enabled"));
        assertEquals(" sip secret ", root.getJSONObject("sip").getString("parallel_password"));
        assertEquals("**613", root.getJSONArray("call_routes").getJSONObject(0).getString("mobile_number_3"));
        assertEquals(45, root.getJSONObject("call").getInt("ring_timeout_seconds"));
        assertFalse(root.getJSONObject("diagnostics").getBoolean("dry_run"));
        assertFalse(root.getJSONObject("audio").getBoolean("echo_cancellation_enabled"));
    }
    @Test public void passiveStatusExplainsSIPAndRespectsUnavailableControls() throws Exception {
        String raw = new JSONObject()
                .put("gateway", new JSONObject().put("dry_run", true).put("trigger_connected", true))
                .put("sip", new JSONObject().put("door_call_enabled", true).put("door_registered", false))
                .put("call", new JSONObject().put("active", false))
                .put("controls", new JSONObject().put("test_call_available", false)).toString();
        String summary = GatewayStatus.summary("Gateway läuft", raw, "");
        assertTrue(summary.contains("SIP und Anrufe sind ausgeschaltet"));
        assertTrue(summary.contains("Klingelverbindung: verbunden"));
        assertFalse(GatewayStatus.available(raw, "test_call_available"));
        assertFalse(GatewayStatus.available("", "hangup_available"));
    }
    @Test public void lastCallAudioCountersRemainReadableAfterHangup() throws Exception {
        JSONObject audio = new JSONObject().put("available", true).put("packets", 100)
                .put("pcm_samples", 8000).put("pcm_peak", 1200).put("rtp_packets", 50);
        JSONObject state = new JSONObject().put("gateway", new JSONObject()).put("sip", new JSONObject())
                .put("call", new JSONObject().put("active", false))
                .put("media", new JSONObject().put("camera_audio", audio));
        String summary = GatewayStatus.summary("Gateway läuft", state.toString(), "");
        assertTrue(summary.contains("letzter Anruf"));
        assertTrue(summary.contains("Empfang: 100 Pakete"));
        assertTrue(summary.contains("Zum Telefon: 50 RTP-Pakete"));
        audio.put("pcm_samples", 0).put("pcm_peak", 0);
        assertTrue(GatewayStatus.summary("", state.toString(), "").contains("Decoder liefert noch keinen Ton"));
    }
    @Test public void audioAndImageOptionsAreOptInAndSurviveConversion() throws Exception {
        JSONObject defaults = new JSONObject(ConfigStore.buildJson(new JSONObject()));
        assertFalse(defaults.getJSONObject("audio").getBoolean("echo_cancellation_enabled"));
        assertFalse(defaults.getJSONObject("live_image").getBoolean("fritzfon_live_image_enabled"));
        JSONObject values = new JSONObject().put("aec_enabled", true).put("noise_suppression", false)
                .put("live_image", true).put("image_port", 18123);
        JSONObject root = new JSONObject(ConfigStore.buildJson(values));
        assertTrue(root.getJSONObject("audio").getBoolean("echo_cancellation_enabled"));
        assertTrue(root.getJSONObject("audio").getBoolean("webrtc_high_pass_filter_enabled"));
        assertFalse(root.getJSONObject("audio").getBoolean("webrtc_noise_suppression_enabled"));
        assertTrue(root.getJSONObject("live_image").getBoolean("fritzfon_live_image_enabled"));
        assertEquals(18123, root.getJSONObject("live_image").getInt("http_port"));
        values.put("aec_enabled", false);
        root = new JSONObject(ConfigStore.buildJson(values));
        assertFalse(root.getJSONObject("audio").getBoolean("webrtc_high_pass_filter_enabled"));
    }
}
