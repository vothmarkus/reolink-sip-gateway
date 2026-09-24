package de.vothmarkus.reolinksip;

import org.json.JSONObject;
import java.util.Locale;

/** Pure presentation of the shared API contract, also used by notifications. */
final class GatewayStatus {
    static String summary(String state, String raw, String error) {
        StringBuilder text = new StringBuilder(state);
        try {
            if (!raw.isEmpty()) {
                JSONObject s = new JSONObject(raw);
                JSONObject gateway = s.getJSONObject("gateway");
                JSONObject sip = s.getJSONObject("sip");
                JSONObject call = s.getJSONObject("call");
                boolean passive = gateway.optBoolean("dry_run");
                text.append(passive ? "\nPassivmodus: SIP und Anrufe sind ausgeschaltet." : "\nAktiver Betrieb");
                text.append("\nKlingelverbindung: ").append(gateway.optString("trigger_source").equals("manual") ? "manueller Betrieb" : gateway.optBoolean("trigger_connected") ? "verbunden" : "Verbindung wird aufgebaut");
                JSONObject events = gateway.optJSONObject("trigger_events");
                if (events != null) {
                    if (!gateway.optBoolean("trigger_connected") && !events.optString("stage").isEmpty()) {
                        text.append("\nVerbindungsschritt: ").append(stageLabel(events.optString("stage")))
                                .append(" · Versuch ").append(events.optLong("connection_attempts"));
                    }
                    String connectionError = events.optString("last_error");
                    if (!connectionError.isEmpty()) text.append("\nLetzter Kamerafehler (")
                            .append(stageLabel(events.optString("last_error_stage"))).append("): ").append(connectionError);
                    text.append("\nKameraereignisse: ").append(events.optLong("messages"))
                            .append(" · Klingelsignale: ").append(events.optLong("visitor_states"))
                            .append(" · Auslösungen: ").append(events.optLong("emitted"));
                }
                text.append("\nSIP Tür: ").append(passive ? "im Passivmodus aus" :
                        !sip.optBoolean("door_call_enabled") ? "deaktiviert" : sip.optBoolean("door_registered") ? "registriert" : "nicht registriert");
                if (sip.optBoolean("parallel_call_enabled")) text.append("\nSIP Parallelruf: ")
                        .append(passive ? "im Passivmodus aus" : sip.optBoolean("parallel_registered") ? "registriert" : "nicht registriert");
                text.append("\nAnruf: ").append(call.optBoolean("active") ? call.optString("state", "aktiv") : "kein Gespräch");
                String caller = call.optString("caller_number");
                if (!caller.isEmpty()) text.append(" · ").append(caller);
                String visitor = gateway.optString("last_visitor_event");
                if (!visitor.isEmpty()) text.append("\nLetztes Klingeln: ").append(visitor);
                JSONObject media = s.optJSONObject("media");
                if (media != null) {
                    text.append("\nAudio: ").append(media.optString("profile", "wird vorbereitet"));
                    if (!media.optString("receive_details").isEmpty()) text.append("\n").append(media.optString("receive_details"));
                    appendEchoStatus(text, media, call.optBoolean("active"));
                    JSONObject audio = media.optJSONObject("camera_audio");
                    if (audio != null && audio.optBoolean("available")) {
                        text.append(media.optBoolean("stats_current_call", call.optBoolean("active")) ? "\nKamera → Telefon:" : "\nKamera → Telefon (letzte Audioverbindung):");
                        text.append("\nEmpfang: ").append(audio.optLong("packets")).append(" Pakete")
                                .append(" · PCM: ").append(audio.optLong("pcm_samples")).append(" Samples")
                                .append("\nZum Telefon: ").append(audio.optLong("rtp_packets")).append(" RTP-Pakete")
                                .append(" · Spitzenpegel: ").append(audio.optInt("pcm_peak")).append("/32768");
                        if (audio.optLong("packets") > 0 && audio.optLong("pcm_samples") == 0) {
                            text.append("\nKamera liefert Daten, Decoder liefert noch keinen Ton.");
                        } else if (audio.optLong("pcm_samples") > 0 && audio.optInt("pcm_peak") == 0) {
                            text.append("\nDie decodierten Kamera-Audiodaten enthalten nur Stille.");
                        }
                    }
                }
                appendError(text, "Gateway", gateway.optString("last_error"));
                appendError(text, "SIP Tür", sip.optString("last_registration_error"));
                appendError(text, "SIP Parallelruf", sip.optString("parallel_last_registration_error"));
            }
        } catch (Exception e) {
            text.append("\nStatus wird geladen …");
        }
        appendError(text, "Dienst", error);
        return text.toString();
    }

    static boolean available(String raw, String control) {
        try { return new JSONObject(raw).getJSONObject("controls").optBoolean(control); }
        catch (Exception e) { return false; }
    }

    static boolean routeAvailable(String raw, String id) {
        try {
            org.json.JSONArray routes = new JSONObject(raw).getJSONArray("routes");
            for (int i = 0; i < routes.length(); i++) {
                JSONObject route = routes.getJSONObject(i);
                if (id.equals(route.getString("id"))) return route.optBoolean("test_call_available");
            }
        } catch (Exception ignored) {}
        return false;
    }

    static String overview(String raw, String error) {
        if (raw.isEmpty()) return error.isEmpty() ? "Verbindungen werden bei eingeschaltetem Gateway angezeigt." : error;
        try {
            JSONObject s = new JSONObject(raw), g = s.getJSONObject("gateway"), sip = s.getJSONObject("sip"), call = s.getJSONObject("call");
            boolean passive = g.optBoolean("dry_run");
            StringBuilder b = new StringBuilder(passive ? "Passivmodus · Anrufe ausgeschaltet" : "Aktiver Betrieb");
            b.append("\nKlingeln: ").append(g.optString("trigger_source").equals("manual") ? "nur manuell" : g.optBoolean("trigger_connected") ? "verbunden" : "Verbindung wird aufgebaut");
            b.append("\nSIP Tür: ").append(registration(sip, "door", passive));
            b.append("\nSIP Parallelruf: ").append(registration(sip, "parallel", passive));
            b.append("\nAnruf: ").append(call.optBoolean("active") ? call.optString("state", "aktiv") : "kein Gespräch");
            if (!call.optString("caller_number").isEmpty()) b.append(" · ").append(call.optString("caller_number"));
            appendError(b, "Gateway", g.optString("last_error"));
            appendError(b, "Dienst", error);
            return b.toString();
        } catch (Exception e) { return "Status wird geladen …"; }
    }

    private static String registration(JSONObject sip, String account, boolean passive) {
        return passive ? "im Passivmodus aus" : !sip.optBoolean(account + "_call_enabled") ? "deaktiviert" :
                sip.optBoolean(account + "_registered") ? "registriert" : "nicht registriert";
    }

    static String audioOverview(String raw) {
        try {
            JSONObject media = new JSONObject(raw).getJSONObject("media");
            String calibration = media.optString("calibration_status");
            boolean enabled = media.optBoolean("echo_cancellation_enabled", !calibration.isEmpty() && !calibration.equals("AEC disabled"));
            String text = media.optString("profile", "Audio wird vorbereitet") + "\nWebRTC AEC: " + (enabled ? "eingeschaltet" : "aus");
            if (enabled && !calibration.isEmpty()) {
                text += "\nMessung: " + calibrationLabel(calibration);
                if (!calibration.equals("pending") && !calibration.equals("measuring")) text += " · " + media.optInt("calibrated_delay_ms") + " ms";
            }
            return text;
        } catch (Exception e) { return "Audioprofil und Messung erscheinen nach dem Start."; }
    }

    private static void appendEchoStatus(StringBuilder text, JSONObject media, boolean callActive) {
        String calibration = media.optString("calibration_status");
        boolean enabled = media.optBoolean("echo_cancellation_enabled", !calibration.isEmpty() && !calibration.equals("AEC disabled"));
        if (!enabled) {
            text.append("\nAEC: aus");
            return;
        }
        JSONObject echo = media.optJSONObject("echo_stats");
        boolean available = echo != null && echo.optBoolean("available");
        boolean current = media.optBoolean("stats_current_call", callActive);
        text.append("\nAEC: ").append(current && available && echo.optLong("capture_frames") > 0 ? "WebRTC aktiv" : "WebRTC eingeschaltet · wartet auf Audioverbindung");
        if (!calibration.isEmpty()) {
            text.append("\nLaufzeitmessung: ").append(calibrationLabel(calibration));
            if (!calibration.equals("pending") && !calibration.equals("measuring")) {
                text.append(" · ").append(media.optInt("calibrated_delay_ms")).append(" ms");
            }
        }
        String details = media.optString("calibration_details");
        if (!details.isEmpty()) text.append("\nMessdetails: ").append(details);
        if (available) {
            text.append(current ? "\nWebRTC-Verarbeitung: " : "\nWebRTC (letzte Audioverbindung): ")
                    .append(echo.optLong("capture_frames")).append(" Audioblöcke · ")
                    .append(echo.optLong("render_frames")).append(" Referenzblöcke")
                    .append("\nReferenz fehlt: ").append(echo.optLong("missing_render_frames"))
                    .append("/").append(echo.optLong("capture_frames"));
            if (echo.optBoolean("erle_valid")) text.append("\nWebRTC Echo-Dämpfung (ERLE): ")
                    .append(String.format(Locale.GERMANY, "%.1f dB", echo.optDouble("erle_db")));
            else text.append("\nWebRTC Echo-Dämpfung: noch kein Messwert");
        }
    }

    static String calibrationLabel(String state) {
        switch (state) {
            case "pending": return "wartet";
            case "measuring": return "Testsignal / Messung läuft";
            case "measured": return "gemessen";
            case "safe fallback": return "fehlgeschlagen · Ersatzwert";
            case "cached fallback": return "fehlgeschlagen · gespeicherter Messwert";
            case "cached (dry run)": return "Passivmodus · gespeicherter Messwert";
            case "skipped (dry run)": return "Passivmodus · Ersatzwert";
            default: return state;
        }
    }

    static String stageLabel(String stage) {
        switch (stage) {
            case "connecting": return "TCP-Verbindung";
            case "nonce": return "Anmeldeantwort anfordern";
            case "login": return "Benutzer anmelden";
            case "subscribing": return "Klingelereignisse abonnieren";
            case "listening": return "Klingelereignisse empfangen";
            case "keepalive": return "Verbindung prüfen";
            case "retry_wait": return "Warte auf nächsten Versuch";
            case "stopped": return "Gestoppt";
            default: return stage;
        }
    }

    static String pollAge(long receivedAt, long now) {
        if (receivedAt == 0) return "Statusabruf: noch keine Antwort";
        long age = Math.max(0, (now - receivedAt) / 1000);
        return "Statusabruf: vor " + age + " s" + (age >= 10 ? " · Aktualisierung ausstehend" : "");
    }

    private static void appendError(StringBuilder text, String label, String error) {
        if (!error.isEmpty()) text.append("\n").append(label).append(": ").append(error);
    }
    private GatewayStatus() {}
}
