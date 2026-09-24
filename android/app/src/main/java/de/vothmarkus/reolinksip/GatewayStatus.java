package de.vothmarkus.reolinksip;

import org.json.JSONObject;

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
                text.append("\nKlingelverbindung: ").append(gateway.optBoolean("trigger_connected") ? "verbunden" : "Verbindung wird aufgebaut");
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
                    String calibration = media.optString("calibration_status");
                    if (!calibration.isEmpty()) text.append("\nAEC: ").append(calibration.equals("AEC disabled") ? "aus" : calibration)
                            .append(calibration.equals("AEC disabled") ? "" : " · " + media.optInt("current_delay_ms") + " ms");
                    JSONObject audio = media.optJSONObject("camera_audio");
                    if (audio != null && audio.optBoolean("available")) {
                        text.append(call.optBoolean("active") ? "\nKamera → Telefon:" : "\nKamera → Telefon (letzter Anruf):");
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
