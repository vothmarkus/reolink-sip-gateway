# Reolink SIP Gateway 1.3.0 – Dokumentation

## Zweck

**1.3.0** ergänzt den bisherigen SIP-/Audiopfad um ein FRITZ!Fon-kompatibles Livebild. Die FRITZ!Box ruft dafür eine stabile lokale `.jpg`-Adresse des Gateways ohne Reolink-Zugangsdaten ab. Mehrere Klingeltaster-Routen, die zwei unabhängig aktivierbaren SIP-Konten und der weiterhin einzige Reolink-Audioweg bleiben gegenüber 1.2.2 unverändert.

Die Konfigurationsoberfläche besitzt nun sechs Gruppen; **FRITZ!Fon-Livebild** enthält den standardmäßig aktiven Schalter `fritzfon_live_image_enabled`. Der flache Runtime-Vertrag bleibt bestehen. Die Liste `call_routes` steht weiterhin unmittelbar hinter **Anruf**, weil Home Assistant ihre Objekte nicht noch eine Ebene tiefer darstellen kann. Eine Neuinstallation enthält bereits eine editierbare Standardroute mit Sensor `auto`, Klingeltaster `11` und drei leeren Mobilnummern. Es gibt weiterhin nur eine konfigurierte Kamera und höchstens ein aktives Reolink-Gespräch. Die App bleibt ohne Companion-Integration vollständig funktionsfähig und erzeugt selbst keine HA-Entities oder Automationen.

Die in 1.2.1 sortierte SIP-Darstellung bleibt bestehen: Auf die beiden zusammenhängenden Konto-Blöcke folgen Registrar-Port, lokaler Tür-Port und lokaler Mobil-Port als gemeinsamer erweiterter Portblock. 1.2.2 ersetzt ausschließlich die doppelten einfachen beziehungsweise katalogbasierten Routingfelder durch die direkte Routenliste.

Das bestehende Branding verwendet PNG-Transparenz für den Außenbereich von `icon.png`, `logo.png` und dem eingebetteten Ingress-Logo. Der Go-Modulpfad entspricht dem öffentlichen Repository `github.com/vothmarkus/reolink-sip-gateway`.

Der in 0.6.0 eingeführte elastische SIP→Baichuan-Talkback-Playout bleibt ebenso unverändert wie Kamera→SIP-Smoother/PLL, Startup-Kalibrierung, fester AEC-Coarse-Delay, AEC3, der deaktivierte Go-Live-Tracker und der native Helper.

Die Home-Assistant-App überwacht alle in den Routen eingetragenen Besucher-Binärsensoren über eine gemeinsame WebSocket-Subscription. Bei Klingeln werden die Ziele der betroffenen Route parallel angerufen: optional deren FRITZ!Box-Klingeltaster und optional bis zu drei Mobilziele. Bei aktivierter Eingangsoption können beide registrierten SIP-Nebenstellen angerufen werden. Gewinner eines ausgehenden Forks und der erste eingehende Anruf verwenden denselben einzelnen bidirektionalen Audio- und AEC-Pfad zwischen SIP und Reolink Doorbell; weitere gleichzeitige Routen werden verworfen und weitere eingehende Anrufe erhalten `486 Busy Here`.

## Startablauf

Bei einem normalen Start führt das Gateway die folgenden Schritte aus:

1. Konfiguration lesen und validieren.
2. Persistente API-Instanz-ID, API-Token und unabhängiges Livebild-Pfadtoken laden beziehungsweise beim ersten Start sicher erzeugen.
3. Status-/Ingress-Seite, optionalen Livebildpfad, Healthcheck und Integrations-API starten; Steuerbefehle bleiben bis zum fertigen Runtimeaufbau gesperrt.
4. Bei `reolink_mode: auto` ein vollständiges Reolink-Medienprofil erkennen.
5. Bei aktivierter AEC die akustische Reolink-Latenz automatisch messen.
6. Erfolgreiche Kalibrierung persistent speichern bzw. bei Messfehler einen passenden Cache oder 1450 ms verwenden.
7. Die aktivierten Tür- und/oder Mobilruf-Konten registrieren; eingehende Anrufe an jedes aktivierte Konto in die gemeinsame Anrufsteuerung führen.
8. Die Besucher-Sensoren aller aufgelösten Routen primär über eine gemeinsame Home-Assistant-WebSocket-Subscription überwachen; REST bleibt interner Fallback.

`dry_run: true` verhindert SIP-Anrufe und den hörbaren Kalibrierungsmarker. Bei explizitem `standalone` oder `nvr` kann die Statusseite trotzdem den vorgesehenen Medienweg anzeigen.

## Reolink-Modi

### `auto`

Die App erkennt beim Start genau ein Profil. Zuerst wird geprüft, ob der konfigurierte RTSP-Endpunkt einen kompatiblen ONVIF-Audio-Backchannel bereitstellt. Wenn nicht, wird der Baichuan/NVR-Weg geprüft. Das Ergebnis gilt für Hin- und Rückkanal aller folgenden Calls.

### `standalone`

- Doorbell → Telefon: RTSP-Audio → FFmpeg → PCM 8 kHz → SIP G.711.
- Telefon → Doorbell: SIP G.711 → ONVIF-RTSP-Backchannel.

### `nvr`

- Doorbell → Telefon: Baichuan `sub` → AAC → PCM 8 kHz → SIP G.711.
- Telefon → Doorbell: SIP G.711 → PCM → Reolink IMA-ADPCM → Baichuan Live Talk.

Der Baichuan-Empfang verwendet fest das `sub`-Profil. Die bisherigen unabhängigen Optionen `connection_mode`, `receive_mode` und `baichuan_receive_stream` existieren nicht mehr.

## Elastischer SIP→Baichuan-Talkback-Puffer

Der Talkback-FIFO bleibt auf vier ausgehandelte Reolink-Blöcke begrenzt und verwirft bei Überlauf weiterhin die ältesten Samples. 0.6.0 ändert ausschließlich, wie der jeweils fällige Block aus den aktuell vorhandenen Samples erzeugt wird:

- Bei knapper beziehungsweise fallender Versorgung werden höchstens 2 % weniger Samples verbraucht und auf die volle Blocklänge gedehnt.
- Bei wachsendem Rückstand oder einer nach Dehnung verbliebenen Reserve werden höchstens 3 % mehr Samples verbraucht und auf die Blocklänge gestaucht.
- Eine darüber hinausgehende Unterdeckung bleibt echte Stille. Der letzte gültige Signalrand und die spätere Rückkehr erhalten je eine kausale 5-ms-Half-Hann-Blende.
- Nach einem unvermeidbaren Drop-oldest-Überlauf verbindet ein kausaler 5-ms-Splice die letzte Ausgabe mit dem neuen FIFO-Anfang.

Der Regler arbeitet ohne Lookahead. Er wartet nicht auf das nächste RTP-Paket, führt keinen zusätzlichen Startpuffer ein, vergrößert den FIFO nicht und verändert weder Blockgröße noch Baichuan-Schreibtakt. Eine kleine durch Dehnung gerettete Reserve wird bei normalisiertem Zulauf per sanfter Stauchung wieder abgebaut und kann daher keine dauerhafte Zusatzlatenz erzeugen.

Die AEC-Referenz bleibt playout-synchron: Erst nach der elastischen Verarbeitung wird der Block als IMA-ADPCM kodiert und geschrieben; aus genau diesem kodierten Block wird zum tatsächlichen Schreibzeitpunkt die Renderreferenz rekonstruiert. Der AEC sieht somit dasselbe Signal wie der Reolink-Lautsprecherpfad.

## Automatische akustische Kalibrierung

Bei aktivierter `echo_cancellation_enabled` wird beim normalen App-Start ein codierter Sprachbandmarker über den aktiven Talkback-Pfad gesendet. Gleichzeitig zeichnet die App den aktiven Doorbell-Empfangspfad auf und bestimmt per normalisierter Kreuzkorrelation die akustische Schleifenlaufzeit.

Die Messung umfasst Reolink-Lautsprecher, Raum-/Nahfeldpfad, Doorbell-Mikrofon und den gewählten Reolink-Transport, aber bewusst nicht SIP/PBX/Telefon.

Der Marker dauert ungefähr eine Sekunde und ist an der Doorbell hörbar. Er wird im Passivmodus nicht ausgesendet.

### Persistenz und Fallback

Eine erfolgreiche Messung wird mit einem Profil-Fingerprint in `/data/aec-calibration.json` gespeichert. Der Fingerprint berücksichtigt Host, aktiven Modus, RTSP-Port/Pfad, Baichuan-Port und NVR-Kanal.

Reihenfolge bei einem Messfehler:

1. passende gespeicherte Messung,
2. sonst eingebauter Startwert 1450 ms.

Eine Kalibrierung eines anderen Kanals oder Transportprofils wird nicht übernommen.

## WebRTC Acoustic Echo Cancellation

Der Call-Pfad verwendet weiterhin die in 0.4.3 eingeführte direkte WebRTC-AudioProcessing-Anbindung:

```text
SIP-Far-End
  ↓
Reolink-Playout
  ↓
playout-synchrone 8-kHz-Renderhistorie
  ↓
automatisch kalibrierter, während des Calls fester Long-Delay
  ↓
80 Samples Render / 10 ms
  ↓
ProcessReverseStream()
set_stream_delay_ms(0)
ProcessStream()
  ↓
SIP-Near-End
```

Die lange Reolink-Laufzeit wird nur einmal in Go kompensiert. WebRTC erhält bereits das zum Capture passende historische Renderfenster und deshalb `set_stream_delay_ms(0)`.

### Benutzeroptionen

- `echo_cancellation_enabled`: AEC insgesamt an/aus.
- `webrtc_high_pass_filter_enabled`: WebRTC-Hochpass an/aus.
- `webrtc_noise_suppression_enabled`: WebRTC-Rauschunterdrückung an/aus.

Fest im Code:

- Go-Live-Delaytracking: aus (Startkalibrierung bleibt für den Call fest),
- Noise-Suppression-Level: `moderate`,
- APM-Stream-Delay: 0 ms,
- AGC/VAD: aus.

## WebRTC-Statistiken

0.5.0 korrigiert die Go-Bitmaske für die native APM-Statistik. C++ und Go verwenden explizit dieselben Bits 0…7 für:

1. ERL,
2. ERLE,
3. Divergent Filter Fraction,
4. Residual Echo Likelihood,
5. Residual Echo Likelihood Recent Max,
6. Delay,
7. Delay Median,
8. Delay Standard Deviation.

Die Werte werden nur auf Debug-Protokollstufe detailliert ausgegeben. Die eigene Gateway-ERLE-Schätzung bleibt als unabhängige Vergleichsgröße erhalten.

## Home Assistant Trigger

Der WebSocket-State-Stream ist der normale Triggerweg. Alle konfigurierten Entity-IDs werden in einer Subscription zusammengefasst; das ausgelöste Ereignis trägt die zugehörige Routen-ID bis in den Call-Controller. Jede Route besitzt ihren eigenen Entprellzustand. Bei Verbindungsproblemen bleibt eine REST-Abfrage als Fallback aktiv; die Sensorzustände werden parallel abgefragt und das Intervall ist intern auf eine Sekunde festgelegt.

## Routen und Klingeltaster

Die Konfigurationsoberfläche enthält unmittelbar unter **Anruf** die Liste **Anrufrouten / Klingeltaster**. Jede Route enthält einen stabilen Bezeichner, Anzeigenamen, Besucher-Sensor, die optionale FRITZ!Box-Klingeltaster-Nummer und bis zu drei direkte Mobilrufnummern. Die vorausgefüllte Standardroute reicht bereits für den üblichen Einfamilienhausbetrieb aus und kann vollständig bearbeitet werden.

```yaml
call_routes:
  - id: wohnung_1
    name: Wohnung 1
    visitor_entity: binary_sensor.klingeltaste_wohnung_1
    doorbell_number: "11"
    mobile_number_1: "0163..."
    mobile_number_2: "0151..."
    mobile_number_3: ""
  - id: wohnung_2
    name: Wohnung 2
    visitor_entity: binary_sensor.klingeltaste_wohnung_2
    doorbell_number: "12"
    mobile_number_1: "0163..."
    mobile_number_2: ""
    mobile_number_3: ""
```

Eine ID beginnt mit einem Kleinbuchstaben und darf anschließend Kleinbuchstaben, Ziffern und Unterstriche enthalten. Sie sollte nach der Einrichtung nicht geändert werden, weil die Companion-Integration sie für stabile Testanruf-Entities verwendet. `mobile_number_1` bis `mobile_number_3` enthalten die Rufnummern direkt. Eine Route kann ausschließlich den FRITZ!Box-Türweg, ausschließlich Mobilziele oder beide Wege verwenden. Leere Felder bleiben als leere Zeichenfolge erhalten.

Die FRITZ!Box 4050 interpretiert unterschiedliche `doorbell_number`-Werte als die dort konfigurierten Klingeltaster; beispielsweise steht `11` häufig für Klingeltaster 1 und `12` für Klingeltaster 2. Die tatsächliche Zuordnung in der FRITZ!Box ist maßgeblich. Routen-ID, Besucher-Sensor und nicht leere Klingeltaster-Nummer müssen jeweils eindeutig sein. Dieselbe Mobilrufnummer darf dagegen bewusst in mehreren Routen vorkommen; nur eine doppelte Nummer innerhalb derselben Route ist ungültig. Eine Route ohne irgendeinen aktivierten Rufweg führt absichtlich zu einem Startfehler.

Alle Routen teilen dieselbe Reolink-Konfiguration, Kalibrierung und Mediensitzung. Eine Kamera-, NVR-Kanal- oder Türöffnerauswahl je Route ist nicht Bestandteil von 1.2.2. Wird während eines laufenden oder gerade aufgebauten Gesprächs eine weitere Route ausgelöst, wird sie verworfen und nicht später nachgeholt.

## SIP und RTP

Für Neuinstallationen ist `sip_registrar: auto` voreingestellt. Der Home-Assistant-Startadapter liest dafür `/proc/net/route`, wählt eine aktive IPv4-Default-Route mit Gateway (bei mehreren die niedrigste Metrik) und setzt die ermittelte Gateway-Adresse nur im privaten Runtime-Snapshot als SIP-Registrar. Dadurch ist in einer typischen FRITZ!Box-Installation automatisch die Router-/FRITZ!Box-Adresse aktiv.

Eine manuell eingetragene IP-Adresse oder ein DNS-Name wird unverändert verwendet und überspringt die Auto-Erkennung vollständig. Bestehende gespeicherte Registrarwerte werden beim Update nicht auf `auto` geändert. Ist `auto` gewählt und keine nutzbare IPv4-Gateway-Route vorhanden, beendet der Adapter den Start mit einer klaren Meldung, den Registrar manuell einzutragen.

Der Standardwert für `reolink_username` ist bei Neuinstallationen `admin`; bestehende gespeicherte Benutzernamen bleiben unangetastet.

Der SIP-Signalisierungsport bleibt über `sip_local_port` konfigurierbar. Für jedes Gespräch reserviert das Gateway vor dem INVITE einen freien UDP-RTP-Port über Port `0` und trägt den tatsächlich zugewiesenen Port in das SDP ein. `rtp_port` wurde deshalb entfernt.

PCMA, PCMU und `auto` bleiben als Codecpräferenz verfügbar.

### Unabhängige SIP-Konten und Mobil-Parallelruf

`door_call_enabled: true` aktiviert das erste SIP-Konto. Es wird in der FRITZ!Box 4050 als **IP-Türsprechanlage** eingerichtet. `doorbell_number` in der jeweils ausgelösten Route ist das dort verwendete Türziel beziehungsweise die Klingeltaste; beispielsweise steht `11` typischerweise für Klingeltaster 1. Maßgeblich ist die tatsächliche Zuordnung in der FRITZ!Box.

`parallel_call_enabled: true` aktiviert ein weiteres SIP-Konto am identischen Registrar. Dieses wird unter **Telefonie → Telefoniegeräte → Neues Gerät → Telefon → LAN/WLAN** als normales IP-Telefon angelegt. Die 4050 ist dabei Registrar und Telefonanlage. Eine vorgeschaltete FRITZ!Box 6690 kann ausschließlich das Kabelmodem bereitstellen. Bei deaktiviertem Tür-Konto kann das Mobilkonto allein betrieben werden; dann sind Tür-Benutzername, -Passwort und -Ziel nicht erforderlich. Im Live-Betrieb muss mindestens eines der Konten aktiviert sein.

Das Mobilkonto verwendet `parallel_username`, `parallel_password` und den separaten lokalen Signalisierungsport `parallel_local_port` (Standard 5071). Registraradresse, Registrarport und Codecpräferenz werden von beiden Konten gemeinsam genutzt. Gleiche lokale Ports werden abgewiesen, wenn beide Konten gleichzeitig aktiv sind.

Jede Route enthält mit `mobile_number_1` bis `mobile_number_3` bis zu drei gleichzeitig zu wählende Nummern direkt. Leerzeichen an den Rändern werden beim Laden entfernt; kanonisch doppelte Nummern innerhalb derselben Route werden als mehrdeutige Konfiguration abgewiesen. Alle Mobilzweige verwenden dasselbe Konto und damit die in der FRITZ!Box diesem IP-Telefon zugewiesene ausgehende Rufnummer.

Das Gateway startet die `INVITE`-Zweige aller aktivierten Konten parallel. Der erste erfolgreiche `200 OK` wird Gewinner. Noch klingelnde Zweige erhalten `CANCEL`. Ein nahezu gleichzeitig angenommener Verlierer wird zwingend mit `ACK` bestätigt und anschließend mit `BYE` beendet. Für jeden Wählzweig existiert bis zur Gewinnerentscheidung ein eigener dynamischer RTP-Port; nur der Gewinner startet die Kamera-/AEC-Mediensitzung.

### DTMF-Aushandlung und Erkennung

Ausgehende SDP-Angebote enthalten zusätzlich Payloadtyp 101 als
`telephone-event/8000`. Eine SDP-Antwort muss genau diesen Payloadtyp bestätigen;
andernfalls bleibt DTMF für das Gespräch deaktiviert. Bei eingehenden Anrufen
übernimmt die Antwort einen angebotenen dynamischen Payloadtyp zwischen 96 und
127, sofern dessen Clockrate 8 kHz beträgt.

Beide Talkback-Wege trennen ausgehandelte Telephone-Event-Pakete vor der
G.711-Audiodekodierung ab. Ein Ereignis entsteht erst beim Endbit und genau
einmal pro Kombination aus SSRC, RTP-Zeitstempel und Eventcode. Unterstützt
werden `0`–`9`, `*`, `#` und `A`–`D`; hörbare In-Band-Töne werden nicht
analysiert. DTMF-Pakete setzen den Audio-Inaktivitätswächter bewusst nicht
zurück.

## Eingehende SIP-Anrufe

`incoming_calls_enabled: true` aktiviert die automatische Annahme auf jedem aktivierten Gateway-Konto. Bei einer FRITZ!Box wird die interne Nummer des gewünschten Telefoniegeräts gewählt, beispielsweise `**620`; maßgeblich ist die tatsächlich unter **Telefonie → Telefoniegeräte** angezeigte Nummer. Tür- und Mobilkonto verwenden dieselbe globale Anrufsteuerung: Der erste Anruf gewinnt, ein weiterer gleichzeitiger Anruf an eines der Konten erhält `486 Busy Here`.

Der Aufbau erfolgt kontrolliert:

1. Das Gateway akzeptiert `INVITE` ausschließlich von IP-Adresse und UDP-Port des konfigurierten SIP-Registrars.
2. Der SIP-User aus dem `From`-Header wird normalisiert und gegen `incoming_allowed_callers` geprüft. Ein nicht erlaubter Anrufer erhält `403 Forbidden`, bevor SDP, Call-Slot oder Kameraressourcen belegt werden.
3. Das SDP-Angebot muss eine nutzbare IPv4-RTP-Adresse sowie PCMA oder PCMU enthalten. Die konfigurierte Codecpräferenz entscheidet, wenn beide angeboten werden.
4. Das Gateway sendet `100 Trying`, reserviert einen dynamischen RTP-Port und startet den festen Reolink-Talkback.
5. Bei `incoming_connection_tone_enabled: true` werden über diesen realen Talkback die ersten vier Symbole des Kalibrierungsmarkers abgespielt. Der Hinweis dauert 256 ms und wird nicht ausgewertet oder als neue Kalibrierung gespeichert.
6. Anschließend wird der Kameraempfang vorbereitet. Erst wenn beide Medienrichtungen bereit sind, folgt die automatische Annahme mit `200 OK` und dem ausgewählten G.711-Codec.
7. Kann der Medienweg nicht vorbereitet werden, erhält der Anrufer `480 Temporarily Unavailable`. Ein zweiter Anruf an Tür- oder Mobilkonto während eines laufenden oder gerade aufgebauten Gesprächs erhält `486 Busy Here`.

`ACK`, `CANCEL` und `BYE` werden dialogbezogen verarbeitet. Ein erfolgreiches `200 OK` wird über UDP bis zum `ACK` kontrolliert wiederholt; bleibt das `ACK` aus, wird der Medienweg beendet. Ein `CANCEL` vor der Annahme beantwortet das Gateway mit `200 OK` und beendet das ursprüngliche `INVITE` mit `487 Request Terminated`.

Die Vertrauensprüfung auf den Registrar verhindert direkte Anrufe von anderen LAN-Teilnehmern, unterscheidet aber nicht zwischen internen und von der Telefonanlage weitergeleiteten externen Gesprächen. Sollen ausschließlich interne FRITZ!Box-Anrufe angenommen werden, darf den aktivierten Gateway-Telefoniegeräten keine externe eingehende Rufnummer zugewiesen sein. RFC-4733-DTMF wird nach erfolgreicher SDP-Aushandlung auf beiden Konten und in beiden Anrufrichtungen identisch erkannt.

### Anruferliste

`incoming_allowed_callers` ist eine Liste. Der Eintrag `*` behält das 0.7-Verhalten „alle vom Registrar vermittelten Anrufer“ bei und darf nicht mit weiteren Einträgen kombiniert werden. Ohne `*` wird exakt gegen den SIP-User verglichen. Bei Rufnummern werden Leerzeichen, Bindestriche, Punkte, Schrägstriche und Klammern entfernt; `+49123…` und `0123…` bleiben bewusst verschiedene Identitäten. Interne Sterncodes wie `**620` sowie benannte SIP-User werden unterstützt. Eine leere Liste ist bei aktivierter Anrufannahme ungültig.

### RTP-Verbindungswächter

`rtp_inactivity_timeout_seconds` gilt für ein- und ausgehende Gespräche. Nach Medienstart beginnt ein Timer, der ausschließlich durch syntaktisch gültige RTP-Pakete mit dem ausgehandelten PCMA-/PCMU-Payloadtyp zurückgesetzt wird. Bleiben solche Pakete aus, beendet das Gateway die Medien und sendet nach Möglichkeit selbst ein `BYE`. Der Standard beträgt 15 Sekunden; zulässig sind 5 bis 120 Sekunden. Stille Audionutzlast zählt als RTP-Aktivität, weshalb normale Gesprächspausen den Wächter nicht auslösen.

Die Begleit-Integration stellt Status/Anrufer sowie Testanruf/Auflegen bereit und löst für DTMF ausschließlich `reolink_sip_gateway_dtmf` aus. Ziffernfolgen, PINs, Mehrkameraauswahl und Türöffner sind keine Gateway-Funktion und werden bei Bedarf als Home-Assistant-Automation umgesetzt.

## FFmpeg

FFmpeg ist Bestandteil des Containerimages und wird fest über `/usr/bin/ffmpeg` verwendet. Eine Benutzeroption `ffmpeg_path` ist nicht erforderlich.

## Protokollierung

- `info`: Start, Moduserkennung, Kalibrierungsergebnis, SIP-Registrierung, Call-Auf-/Abbau und relevante Warnungen/Fehler.
- `debug`: zusätzlich SIP-Pakete, RTSP/Baichuan-Diagnose, RTP/Jitter, Puffer/Smoother, AEC-Tracker, eigene ERLE und native WebRTC-Statistik.

Das Debug-Abschlusslog `Baichuan live audio bridge stopped` enthält für den elastischen Talkback unter anderem:

- `fifo_raw_shortage_samples` vor und `fifo_underrun_samples` nach der elastischen Korrektur,
- `fifo_playout_min_ms`, `fifo_playout_avg_ms` und `fifo_playout_max_ms`,
- Stretch-/Compress-Blöcke und -Samples sowie minimales, aktuelles und maximales Verhältnis,
- `elastic_supply_trend_samples`, Fade-in/-out-Zähler und `elastic_overflow_splices`.

Die bisherigen Schalter `debug_sip`, `debug_rtsp`, `debug_baichuan` sind entfernt.

## Entfernte manuelle Selbsttests

`camera_test`, `backchannel_test`, `baichuan_test` und `latency_test` sind keine Benutzeroptionen mehr. Die für den regulären Betrieb benötigte Capability-Erkennung und Latenzmessung sind in den automatischen Startablauf integriert; nicht mehr benötigter manueller Testcode wurde entfernt.

## NVR-Kanal und erweiterte Reolink-Felder

- `nvr_channel_number`: sichtbare, 1-basierte Kanalnummer wie in der Reolink-NVR-Oberfläche. Nur relevant, wenn ein NVR verwendet wird.
- `reolink_rtsp_port`, Standard 554.
- `baichuan_port`, Standard 9000.

`reolink_stream_path` und der interne 0-basierte `nvr_channel` sind keine Benutzeroptionen mehr. Das Startskript bildet die UI-Einstellung unmittelbar vor dem Gatewaystart auf die bewährte 0.5.1-Runtimekonfiguration ab. Beispiel: NVR-Kanal 2 wird intern zu `nvr_channel=1` und `/Preview_02_sub`. Im expliziten Standalone-Modus wird die NVR-Kanalnummer ignoriert und `/Preview_01_sub` verwendet.

## FRITZ!Fon-Livebild

Ist `live_image.fritzfon_live_image_enabled` aktiv, registriert der bestehende Statusserver auf Port `18099` einen geheimen Pfad der Form `/fritzfon/<token>.jpg`. Die Ingress-Seite zeigt die vollständige lokale Adresse ohne Protokollpräfix sowie einen Browsertest. Dazu ermittelt das Gateway die lokale Home-Assistant-IPv4-Adresse anhand der Route zum SIP-Registrar, ohne ein Paket zu senden. Schlägt die Ermittlung fehl, erscheint der eindeutige Platzhalter `HOME-ASSISTANT-IP`, der durch die tatsächliche lokale Adresse des Home-Assistant-Hosts ersetzt werden muss.

Einrichtung in der FRITZ!Box:

1. Unter **Telefonie → Telefoniegeräte** die als IP-Türsprechanlage angelegte Nebenstelle bearbeiten.
2. Beim Feld **Live-Bild** `http://` auswählen.
3. Den auf der Gateway-Ingress-Seite gezeigten Wert ohne vorangestelltes `http://` in das Nachbarfeld kopieren und übernehmen.
4. Zuerst den Browsertest, danach einen Türruf mit dem FRITZ!Fon prüfen.

Jeder Abruf versucht nacheinander die Reolink-Snapshot-CGI am konfigurierten Kamera-/NVR-Host über HTTPS-Port 443 und HTTP-Port 80. Die Abfrage verwendet den intern 0-basierten physischen Kanal und bittet Reolink um die dokumentierte Substream-Größe 640×480. Antwortet die CGI nicht mit einem gültigen JPEG, liest FFmpeg genau einen Frame aus dem bereits konfigurierten RTSP-Stream. Das Gateway validiert JPEG-Signatur, Dekodierbarkeit, Größe und Pixelgrenze. Bilder werden seitenverhältnistreu in einen 480×640-Rahmen eingepasst; kleinere, bereits passende Bilder bleiben unverändert. Ein 750-ms-Speicher-Cache fasst fast gleichzeitige Abrufe zusammen. Antworten tragen `Content-Type: image/jpeg` und ausdrückliche No-Cache-Header.

Das 192-Bit-Pfadtoken wird einmalig unter `/data/fritzfon-live-image-token` mit Rechten `0600` erzeugt und bleibt bei Update und Backup stabil. Es ist bewusst vom 256-Bit-Integrations-API-Token getrennt: Die Kenntnis der Bildadresse ermöglicht nur Bildabrufe und berechtigt weder zu Testanruf noch Auflegen. Trotzdem ist die komplette URL vertraulich zu behandeln. Zusätzlich weist der Server Anfragen außerhalb von Loopback-, privaten und Link-Local-Netzen ab. Zugangsdaten erscheinen weder in der URL noch in klassifizierten Fehlerantworten oder normalen Logs. Bei abgeschalteter Option wird der Pfad nicht registriert.

AVM unterstützt für das Telefonbild JPG/JPEG, PNG und GIF und verlangt eine passende Dateiendung in der URL. Die von AVM genannte sinnvolle Größenordnung von etwa 240×320 bis 480×640 Pixeln bildet direkt den Ausgabe-Rahmen. Ein typisches Reolink-Landschaftsbild wird darin beispielsweise 480×360 Pixel groß. Die Reolink-CGI-Parameter entsprechen der offiziellen Snapshot-Schnittstelle.

## Ingress-Status

Die Statusseite zeigt den konfigurierten und aktiven Modus, Medienprofil, Kalibrierungsstatus, kalibrierten Startwert, aktuellen Trackerwert, Suchfenster/-grenzen, WebRTC-Filter, SIP-/HA-Status, aktuelle/letzte Route, aktuelle/letzte Anrufrichtung, aktuelle/letzte anrufende Nummer und aktive Call-Medien. Zeitangaben werden kompakt formatiert; noch nicht vorhandene Zeitpunkte erscheinen als Gedankenstrich. Ein eigener administrativer Abschnitt zeigt den internen App-Hostnamen und das Token für die Einrichtung der Companion-Integration. Die Integration erzeugt daraus selbst `http://<Hostname>:18099/api/v1`. Ein getrennter Abschnitt zeigt ausschließlich bei aktivierter Funktion die FRITZ!Fon-Adresse samt Browsertest.

## Home-Assistant-Integrations-API v1

Die API läuft zusammen mit Statusseite und Healthcheck auf Port `18099`. Der vollständige OpenAPI-3.1-Vertrag liegt unter `docs/api-v1.openapi.yaml`. Die API-Versionsnummer ist unabhängig von der App-Version; 1.2.0 ergänzt den Routenkatalog und Routenstatus additiv. Das bestehende Feld `sip.registered` bedeutet kompatibel „mindestens ein aktiviertes Konto ist registriert“.

- `GET /api/v1/info`: API-/Gateway-Version, stabile Installations-UUID und Fähigkeiten.
- `GET /api/v1/status`: vollständiger Gateway-, SIP-, Routen-, Call-, Medien- und Befehlsstatus. `routes` enthält nur stabile ID, Anzeigename und die aktuelle Testanruf-Verfügbarkeit; Rufnummern werden nicht ausgegeben.
- `GET /api/v1/events`: Server-Sent Events vom Typ `status` und `dtmf`; das erste Ereignis ist immer der aktuelle vollständige Snapshot. Ein `dtmf`-Ereignis enthält Ziffer, Dauer, Anrufrichtung, exakt normalisierte Gegenstelle (`remote_number`), SIP-Dialog-ID (`call_id`), Empfangszeit und Installations-ID. Die Gegenstelle ist bei eingehenden Anrufen der Anrufer und bei ausgehenden Anrufen das konfigurierte SIP-Ziel. Das Ereignis besitzt keine SSE-ID, ändert keine Statusrevision und wird nicht wiederholt. 15-Sekunden-Kommentare dienen als Keepalive.
- `POST /api/v1/routes/{route_id}/test`: normaler ausgehender Fork exakt für die angegebene Route; `202` bei Annahme, `404` bei unbekannter Route, `409` bei belegtem Call-Slot und `503`, wenn ein für die Route benötigtes SIP-Konto nicht registriert beziehungsweise die Runtime noch nicht bereit ist.
- `POST /api/v1/calls/test`: kompatibler Alt-Endpunkt; startet die erste aufgelöste Route und bleibt für ältere Integrationen verfügbar.
- `POST /api/v1/calls/hangup`: beendet Wähl-, Vorbereitungs- oder Gesprächsphase beider Richtungen; im Leerlauf bestätigt `204` die idempotente Wirkung.

Beim ersten normalen Start entstehen `/data/integration-api-instance-id`, `/data/integration-api-token` und `/data/fritzfon-live-image-token`. Die geheimen Dateien werden mit Rechten `0600` angelegt und alle drei Werte über App-Updates sowie Backups erhalten. Das API-Token besteht aus 256 Zufallsbits und wird nicht protokolliert. Jeder `/api/v1`-Aufruf benötigt `Authorization: Bearer <token>`; zusätzlich sind nur Loopback-, private und Link-Local-Quelladressen zugelassen. Das separate 192-Bit-Livebildtoken ist kein API-Bearer-Token. Healthcheck und bestehende Ingress-Routen behalten ihre bisherigen Zugriffseigenschaften.

Der Status unterscheidet aktuelle und letzte Werte: `call.direction`, `call.caller_number`, `call.route_id` und `call.route_name` werden nach dem Gespräch geleert, während die jeweiligen `last_*`-Felder erhalten bleiben. Bei eingehenden Anrufen ohne ausgelöste Klingelroute bleiben die Routenfelder leer. Die letzte anrufende Nummer wird nur durch einen zugelassenen eingehenden Anruf aktualisiert. Diagnosen und Fehlerantworten enthalten niemals Token, Zugangsdaten oder konfigurierte Mobilrufnummern.

## Update von älteren Versionen

1.3.0 ergänzt die neue Gruppe `live_image` mit dem Standard `fritzfon_live_image_enabled: true`. Der Startadapter bildet sie auf den neuen flachen Runtimewert ab; Routen-, SIP-, Audio- und Reolink-Werte werden nicht migriert oder verändert. Beim ersten Start wird nur das unabhängige Livebild-Pfadtoken unter `/data` erzeugt. Wer keinen Bildabruf bereitstellen möchte, kann den Schalter vor oder nach dem Update deaktivieren.

1.2.2 führt einen einmaligen, abgesicherten Wechsel auf direkte Anrufrouten aus. Eine 1.1-Konfiguration wird als editierbare Route `default` übernommen; dabei bleiben Besucher-Sensor, Türziel und bis zu drei Mobilnummern erhalten. Vorhandene 1.2-Routen behalten ID, Name, Sensor und Klingeltaster. Ihre früheren Mobilziel-IDs werden über den vorhandenen Katalog in direkte Nummern aufgelöst. Wenn ein Benutzer bereits intuitiv eine Rufnummer direkt in ein früheres Referenzfeld eingetragen hatte, wird auch dieser Wert unverändert übernommen. Erst die vollständig erzeugte Ersatzkonfiguration wird in einem Supervisor-Schreibvorgang gespeichert. Der bestehende Vergleich-vor-Schreiben-Schutz verhindert, dass eine zeitgleiche Benutzeränderung überschrieben wird; danach bleiben normale Starts wieder schreibgeschützt.

1.2.1 ordnet vorhandene SIP-Felder lediglich neu an. Es gibt keine neue Option und keine Datenmigration; alle gespeicherten Werte werden unverändert weiterverwendet.

1.2.0 ergänzt die leeren Listen `mobile_targets` und `call_routes`. Solange keine Route eingetragen wird, bildet das Gateway `visitor_entity`, `sip_destination` und `parallel_destinations` intern auf die Route `default` ab; Registrierungen, Zielwahl, DTMF und Medienverhalten bleiben dadurch gegenüber 1.1.1 unverändert. Erst eine nicht leere Routenliste schaltet auf die explizite Matrix um. Der Startadapter schreibt die neuen Listen ausschließlich in den privaten Runtime-Snapshot und verändert keine bestehenden gespeicherten Werte.

1.1.1 ergänzt `door_call_enabled` mit dem Standard `true`. Damit bleibt das Tür-Konto bei einem Update aktiv. Erst nach expliziter Deaktivierung dürfen dessen Zugangsdaten und Ziel leer bleiben. Das Mobilkonto kann dann mit seinen ein bis drei Zielen allein betrieben werden. `incoming_calls_enabled` gilt nun für beide aktivierten Konten; die Anruferliste, der Hinweiston, DTMF und der eine globale Call-Slot gelten unverändert gemeinsam.

1.1.0 ergänzt im SIP-Block fünf neue Optionen. Der Mobil-Parallelruf ist standardmäßig deaktiviert; damit bleibt ein Update funktional identisch zu 1.0.0. Fehlende Werte werden am bestehenden gruppiert→flach-Adapter sicher mit `false`, leerer Zugangsdaten-/Zielliste und Port 5071 ergänzt. Die vorhandene API-Identität, DTMF-Aushandlung und sämtliche Reolink-/AEC-Einstellungen bleiben erhalten.

1.0.0 ergänzt ausschließlich die additive DTMF-Aushandlung und den flüchtigen
Ereignistyp. Es gibt keine neue Option oder Konfigurationsmigration. Die bereits
unter 0.9.0 erzeugte API-Identität und das Token bleiben unverändert erhalten.

0.9.0 ergänzt ausschließlich automatisch verwaltete Dateien unter `/data` und die API auf dem bereits verwendeten Statusport. Es gibt keine neue Option und keine Migration bestehender Einstellungen. Die Identität wird einmal erzeugt und bleibt danach stabil; eine ungültig veränderte Identitätsdatei führt aus Sicherheitsgründen zu einem klaren Startfehler statt zu stiller Tokenrotation.

0.8.0 ergänzt `call.incoming_allowed_callers`, `call.incoming_connection_tone_enabled` und `call.rtp_inactivity_timeout_seconds`. Für bestehende 0.7-Installationen wird `incoming_allowed_callers: ["*"]` verwendet, sodass die bereits bewusst aktivierte Anrufannahme beim Update funktional erhalten bleibt. Der Hinweiston ist standardmäßig aktiv, der RTP-Wächter verwendet 15 Sekunden. Vorhandene Werte und der abgeschlossene Gruppierungsmigrationsmarker bleiben unangetastet.

0.7.0 ergänzte `call.incoming_calls_enabled`. Fehlt der Schalter in einer älteren Konfiguration, gilt weiterhin der sichere Standard `false`; ein Update aktiviert daher niemals unbeabsichtigt die automatische Annahme.

0.6.0 übernimmt den in 0.5.10 abgeschlossenen Migrationszustand, den Visitor-Hotfix aus 0.5.13 und die Darstellung aus 0.5.14 unverändert. Bereits gespeicherte `sip_registrar`-, `reolink_username`- und manuelle `visitor_entity`-Werte bleiben bestehen; Defaults gelten nur, wenn die jeweilige Option noch nicht vorhanden ist. Es gibt keine neue oder geänderte Option für den elastischen Talkback-Puffer.

0.5.10 übernimmt bei einem direkten Upgrade von älteren 0.5.x-Ständen bestehende flache Optionen sowie frühere Kanal-Aliase einmalig in die fünf gruppierten UI-Blöcke. Solange der persistente Migrationsmarker noch fehlt, gewinnen die alten flachen Werte bewusst gegen eventuell bereits vom Supervisor materialisierte Gruppen-Defaults. Nach erfolgreicher Migration bzw. sobald bereits eine reine gruppierte Konfiguration erkannt wurde, wird der Marker gesetzt. Ab dann sind ausschließlich die gruppierten Werte maßgeblich und normale Starts führen keinen Supervisor-Options-Write mehr aus. `nvr_channel_number` bleibt in der UI 1-basiert; der private Runtime-Snapshot wird vor Programmstart wieder im bewährten flachen Format erzeugt. Das nicht mehr wirksame `echo_cancellation_search_window_ms` bleibt aus den gespeicherten Home-Assistant-Optionen entfernt.


### Bashio-Kompatibilität ab 0.5.1

Die Bereinigung liest die aktuell gemounteten Optionen direkt aus `/data/options.json`. Für das atomare Zurückschreiben wird auf neuen Bashio-Versionen `bashio::app.options` verwendet; falls diese Funktion nicht vorhanden ist, fällt das Startskript auf `bashio::addon.options` zurück. Schlägt das Schreiben dennoch fehl, wird nur die Bereinigung übersprungen; das Gateway startet unabhängig davon weiter.

## Automatische Erkennung des Besucher-Sensors

Für **Reolink-Besucher-Sensor** kann `auto` verwendet werden. Das Gateway fragt beim Start die kompakte Home-Assistant-Registry-Ansicht `config/entity_registry/list_for_display` ab und sucht anhand der Felder `ei`, `pl` und `tk` einen aktivierten `binary_sensor` der Plattform `reolink` mit dem Translation-Key `visitor`. Dadurch bleibt die Erkennung auch erhalten, wenn die Entity-ID in Home Assistant umbenannt wurde. Die WebSocket-Verbindung akzeptiert dabei bis zu 16 MiB große Frames/Nachrichten, bleibt aber hart begrenzt.

Wird genau ein aktivierter Reolink-Besucher-Sensor gefunden, wird dessen Entity-ID nur in den privaten Runtime-Snapshot übernommen. Wird keiner gefunden, endet der Start mit einer klaren Fehlermeldung. Bei mehreren aktivierten Reolink-Türklingeln wird bewusst nicht geraten; in diesem Fall muss die gewünschte `binary_sensor...`-Entity manuell eingetragen werden. Ein manueller Wert hat immer Vorrang vor `auto`.

Diese Automatik darf genau eine Route verwenden. Bei der vorausgefüllten Standardroute steht deshalb `visitor_entity: auto`. Sobald weitere Klingeltaster-Routen eingerichtet werden, erhalten diese eine explizite Entity-ID; mehr als ein `auto` wird mit einer eindeutigen Startmeldung abgewiesen.

## Passivmodus

Die sichtbare Bezeichnung **Passivmodus** verwendet intern weiterhin den Schlüssel `dry_run`, damit bestehende Konfigurationen kompatibel bleiben. Im Passivmodus werden Klingelereignisse überwacht, aber SIP wird nicht registriert, es werden keine Anrufe gestartet und der akustische Kalibrierungsmarker wird nicht ausgegeben.
