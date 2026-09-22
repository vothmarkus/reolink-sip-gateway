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

    private static void appendError(StringBuilder text, String label, String error) {
        if (!error.isEmpty()) text.append("\n").append(label).append(": ").append(error);
    }
    private GatewayStatus() {}
}
