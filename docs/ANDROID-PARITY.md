# Android V1.0 – Vergleich mit HA und Standalone

Verglichen am 24.09.2026: `main` bei `6198acb6` (HA 1.5.0),
`feature/v2-standalone` bei `f0ca0310` und Android vor V1.0 bei `69dc6fbd`.
Beide Vergleichszweige sind Vorfahren des Android-Zweigs. Es fehlen keine
Commits aus diesen Ständen; Unterschiede liegen in den Plattformadaptern und
der Bedienoberfläche. Der Betrieb direkt an der Kamera ohne NVR bleibt der
Standard für neue Android-Konfigurationen.

| Funktion | HA / Linux-Standalone | Android 1.0 |
| --- | --- | --- |
| Kamera direkt, ohne NVR | Im gemeinsamen Kern vorhanden | Ja, Baichuan für Ereignisse und beide Audiorichtungen |
| Ausgehende, eingehende und parallele SIP-Anrufe | Ja | Ja, gleicher Anrufcontroller; erstes angenommenes Ziel gewinnt |
| Türkonto optional, zweites SIP-Konto für Parallelruf | Ja | Ja |
| Mehrere benannte Rufrouten, bis zu 3 Parallelziele je Route | Ja | Neu im Menü; bis zu 32 Routen |
| Einzelne Rufroute testen | Ja | Neu auswählbar in der Übersicht |
| Rufroute für Kamera-Klingeltaste auswählen | Standalone | Neu im Menü |
| Manueller Auslöser, Passivmodus, Log-Level | Standalone | Alle drei im Menü; manueller Betrieb und Passivmodus sind getrennt |
| WebRTC AEC, Hochpass, Rauschfilter und akustische Messung | Ja | Ja; nativer WebRTC-Prozessor und Android-AAC-Decoder, seit alpha6 ohne FFmpeg-Abhängigkeit für Baichuan |
| Gesprächsgrenzen, Anruferliste, Verbindungston | Ja | Ja |
| Primäres FRITZ!Fon-JPEG und Vorschau | Ja | Ja, HTTP(S)-Snapshot; Vorschau alle 3 Sekunden |
| NVR-Bildkatalog mit weiteren Kanal-/Kamera-URLs | HA / Linux | Noch nicht im Android-Menü/LAN-Bildlistener; Android zeigt das konfigurierte Hauptbild |
| RTSP/FFmpeg-Empfang und Bild-Fallback | Linux | Nicht enthalten; direkte Kamera/NVR über Baichuan, Bilder über HTTP(S) |
| Konfiguration exportieren/importieren | Standalone, Schema 2 | Neu über Android-Dateiauswahl; kompatible Direct-/NVR-Profile mit explizitem SIP-Registrar |
| Sicherung der vorherigen Konfiguration beim Speichern | Standalone | Kein automatisches Zurückrollen; expliziter JSON-Export/Import |
| SIP-Registrar `auto` | Linux: Standardgateway als Heuristik | Nicht enthalten; Telefonanlage wird ausdrücklich eingetragen |
| Einrichtung und Steuerungs-API aus dem LAN | Linux | Android-Menü; Steuerungs-API nur intern auf Loopback, LAN-Port nur für das Hauptbild |
| HA-Entitäten/Automationen als Klingelauslöser | HA | Nicht enthalten; direkte Kameraereignisse oder manuell |
| RFC4733-DTMF-Ereignisse für HA | HA-Integration | Keine Android-Aktionskonfiguration; eigenständige DTMF-Aktionen fehlen auch in Standalone |
| Dauerbetrieb, Autostart | Supervisor/systemd | Android-Vordergrunddienst, CPU-/WLAN-Sperren und optionaler Gerätestart |

Auch die anderen Zweige bieten derzeit keinen kontinuierlichen WebRTC-Videoplayer,
keine frei konfigurierbaren Standalone-DTMF-Türöffneraktionen und keine parallelen
Gespräche über mehrere Kameras. Mehrere Rufrouten sind unterschiedliche Zielgruppen
für dieselbe konfigurierte Kamera, kein Mehrkamera-Gateway.

Bestehende Android-Einstellungen werden übernommen. Die bisherige einzelne Route
heißt weiterhin **Haustür** mit der Kennung `default`. Die getrennte Android-
Versionsnummer 1.0.0 ändert weder HA-Versionsnummern noch die Kernkennung
2.0.0-beta.1. Alle V1.0-Arbeiten bleiben auf `feature/v2-android`.
