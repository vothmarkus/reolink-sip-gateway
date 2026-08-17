# Reolink SIP Gateway 0.8.0 – Dokumentation

## Zweck

**0.8.0** sichert und überwacht die in 0.7.0 ergänzten eingehenden SIP-Anrufe. Eine Rufnummern-/SIP-Benutzer-Whitelist entscheidet vor dem Kameraaufbau über die Annahme, ein kurzer vorhandener Akustikmarker weist an der Doorbell auf die Verbindung hin und ein RTP-Wächter räumt abgebrochene Gespräche ohne SIP-`BYE` auf.

Die fünf internen Konfigurationsgruppen und der flache Runtime-Vertrag bleiben bestehen. Die neuen Einstellungen liegen vollständig im Block **Anruf**; es gibt weiterhin nur eine konfigurierte Kamera und höchstens ein aktives Gespräch. 0.8 benötigt keine zusätzliche Home-Assistant-Integration und erzeugt noch keine HA-Entities.

Das bestehende Branding verwendet PNG-Transparenz für den Außenbereich von `icon.png`, `logo.png` und dem eingebetteten Ingress-Logo. Der Go-Modulpfad entspricht dem öffentlichen Repository `github.com/vothmarkus/reolink-sip-gateway`.

Der in 0.6.0 eingeführte elastische SIP→Baichuan-Talkback-Playout bleibt ebenso unverändert wie Kamera→SIP-Smoother/PLL, Startup-Kalibrierung, fester AEC-Coarse-Delay, AEC3, der deaktivierte Go-Live-Tracker und der native Helper.

Die Home-Assistant-App überwacht einen Reolink-Besucher-Binärsensor. Bei Klingeln wird ein SIP-Ziel angerufen. Bei aktivierter Eingangsoption kann zusätzlich die registrierte SIP-Nebenstelle angerufen werden. Beide Richtungen verwenden danach denselben bidirektionalen Audio- und AEC-Pfad zwischen SIP und Reolink Doorbell.

## Startablauf

Bei einem normalen Start führt das Gateway die folgenden Schritte aus:

1. Konfiguration lesen und validieren.
2. Status-/Ingress-Seite starten.
3. Bei `reolink_mode: auto` ein vollständiges Reolink-Medienprofil erkennen.
4. Bei aktivierter AEC die akustische Reolink-Latenz automatisch messen.
5. Erfolgreiche Kalibrierung persistent speichern bzw. bei Messfehler einen passenden Cache oder 1450 ms verwenden.
6. SIP registrieren und bei aktivierter Option eingehende Anrufe mit der konfigurierten Anruferregel bereitstellen.
7. Home-Assistant-Klingelereignisse primär per WebSocket überwachen; REST bleibt interner Fallback.

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

Der WebSocket-State-Stream ist der normale Triggerweg. Bei Verbindungsproblemen bleibt eine REST-Abfrage als Fallback aktiv; das Intervall ist intern auf eine Sekunde festgelegt und nicht mehr konfigurierbar.

## SIP und RTP

Für Neuinstallationen ist `sip_registrar: auto` voreingestellt. Der Home-Assistant-Startadapter liest dafür `/proc/net/route`, wählt eine aktive IPv4-Default-Route mit Gateway (bei mehreren die niedrigste Metrik) und setzt die ermittelte Gateway-Adresse nur im privaten Runtime-Snapshot als SIP-Registrar. Dadurch ist in einer typischen FRITZ!Box-Installation automatisch die Router-/FRITZ!Box-Adresse aktiv.

Eine manuell eingetragene IP-Adresse oder ein DNS-Name wird unverändert verwendet und überspringt die Auto-Erkennung vollständig. Bestehende gespeicherte Registrarwerte werden beim Update nicht auf `auto` geändert. Ist `auto` gewählt und keine nutzbare IPv4-Gateway-Route vorhanden, beendet der Adapter den Start mit einer klaren Meldung, den Registrar manuell einzutragen.

Der Standardwert für `reolink_username` ist bei Neuinstallationen `admin`; bestehende gespeicherte Benutzernamen bleiben unangetastet.

Der SIP-Signalisierungsport bleibt über `sip_local_port` konfigurierbar. Für jedes Gespräch reserviert das Gateway vor dem INVITE einen freien UDP-RTP-Port über Port `0` und trägt den tatsächlich zugewiesenen Port in das SDP ein. `rtp_port` wurde deshalb entfernt.

PCMA, PCMU und `auto` bleiben als Codecpräferenz verfügbar.

## Eingehende SIP-Anrufe

`incoming_calls_enabled: true` aktiviert automatische Anrufe an die registrierte Gateway-Nebenstelle. Bei einer FRITZ!Box wird die interne Nummer des als IP-Telefon eingerichteten Gateway-Kontos gewählt, beispielsweise `**620`; maßgeblich ist die tatsächlich unter **Telefonie → Telefoniegeräte** angezeigte Nummer.

Der Aufbau erfolgt kontrolliert:

1. Das Gateway akzeptiert `INVITE` ausschließlich von IP-Adresse und UDP-Port des konfigurierten SIP-Registrars.
2. Der SIP-User aus dem `From`-Header wird normalisiert und gegen `incoming_allowed_callers` geprüft. Ein nicht erlaubter Anrufer erhält `403 Forbidden`, bevor SDP, Call-Slot oder Kameraressourcen belegt werden.
3. Das SDP-Angebot muss eine nutzbare IPv4-RTP-Adresse sowie PCMA oder PCMU enthalten. Die konfigurierte Codecpräferenz entscheidet, wenn beide angeboten werden.
4. Das Gateway sendet `100 Trying`, reserviert einen dynamischen RTP-Port und startet den festen Reolink-Talkback.
5. Bei `incoming_connection_tone_enabled: true` werden über diesen realen Talkback die ersten vier Symbole des Kalibrierungsmarkers abgespielt. Der Hinweis dauert 256 ms und wird nicht ausgewertet oder als neue Kalibrierung gespeichert.
6. Anschließend wird der Kameraempfang vorbereitet. Erst wenn beide Medienrichtungen bereit sind, folgt die automatische Annahme mit `200 OK` und dem ausgewählten G.711-Codec.
7. Kann der Medienweg nicht vorbereitet werden, erhält der Anrufer `480 Temporarily Unavailable`. Ein zweiter Anruf während eines laufenden oder gerade aufgebauten Gesprächs erhält `486 Busy Here`.

`ACK`, `CANCEL` und `BYE` werden dialogbezogen verarbeitet. Ein erfolgreiches `200 OK` wird über UDP bis zum `ACK` kontrolliert wiederholt; bleibt das `ACK` aus, wird der Medienweg beendet. Ein `CANCEL` vor der Annahme beantwortet das Gateway mit `200 OK` und beendet das ursprüngliche `INVITE` mit `487 Request Terminated`.

Die Vertrauensprüfung auf den Registrar verhindert direkte Anrufe von anderen LAN-Teilnehmern, unterscheidet aber nicht zwischen internen und von der Telefonanlage weitergeleiteten externen Gesprächen. Sollen ausschließlich interne FRITZ!Box-Anrufe angenommen werden, darf dem Gateway-IP-Telefon keine externe eingehende Rufnummer zugewiesen sein.

### Anruferliste

`incoming_allowed_callers` ist eine Liste. Der Eintrag `*` behält das 0.7-Verhalten „alle vom Registrar vermittelten Anrufer“ bei und darf nicht mit weiteren Einträgen kombiniert werden. Ohne `*` wird exakt gegen den SIP-User verglichen. Bei Rufnummern werden Leerzeichen, Bindestriche, Punkte, Schrägstriche und Klammern entfernt; `+49123…` und `0123…` bleiben bewusst verschiedene Identitäten. Interne Sterncodes wie `**620` sowie benannte SIP-User werden unterstützt. Eine leere Liste ist bei aktivierter Anrufannahme ungültig.

### RTP-Verbindungswächter

`rtp_inactivity_timeout_seconds` gilt für ein- und ausgehende Gespräche. Nach Medienstart beginnt ein Timer, der ausschließlich durch syntaktisch gültige RTP-Pakete mit dem ausgehandelten PCMA-/PCMU-Payloadtyp zurückgesetzt wird. Bleiben solche Pakete aus, beendet das Gateway die Medien und sendet nach Möglichkeit selbst ein `BYE`. Der Standard beträgt 15 Sekunden; zulässig sind 5 bis 120 Sekunden. Stille Audionutzlast zählt als RTP-Aktivität, weshalb normale Gesprächspausen den Wächter nicht auslösen.

Native Home-Assistant-Sensoren für Status/Anrufer sowie die Tasten Testanruf/Auflegen sind für 0.9 vorgesehen und werden über eine Begleit-Integration umgesetzt. DTMF folgt getrennt in 1.0; Mehrkameraauswahl, PIN und Türöffner sind ebenfalls noch nicht Bestandteil von 0.8.

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

## Ingress-Status

Die Statusseite zeigt den konfigurierten und aktiven Modus, Medienprofil, Kalibrierungsstatus, kalibrierten Startwert, aktuellen Trackerwert, Suchfenster/-grenzen, WebRTC-Filter, SIP-/HA-Status, die letzte Anrufrichtung und aktive Call-Medien. Zeitangaben werden kompakt formatiert; noch nicht vorhandene Zeitpunkte erscheinen als Gedankenstrich.

## Update von älteren Versionen

0.8.0 ergänzt `call.incoming_allowed_callers`, `call.incoming_connection_tone_enabled` und `call.rtp_inactivity_timeout_seconds`. Für bestehende 0.7-Installationen wird `incoming_allowed_callers: ["*"]` verwendet, sodass die bereits bewusst aktivierte Anrufannahme beim Update funktional erhalten bleibt. Der Hinweiston ist standardmäßig aktiv, der RTP-Wächter verwendet 15 Sekunden. Vorhandene Werte und der abgeschlossene Gruppierungsmigrationsmarker bleiben unangetastet.

0.7.0 ergänzte `call.incoming_calls_enabled`. Fehlt der Schalter in einer älteren Konfiguration, gilt weiterhin der sichere Standard `false`; ein Update aktiviert daher niemals unbeabsichtigt die automatische Annahme.

0.6.0 übernimmt den in 0.5.10 abgeschlossenen Migrationszustand, den Visitor-Hotfix aus 0.5.13 und die Darstellung aus 0.5.14 unverändert. Bereits gespeicherte `sip_registrar`-, `reolink_username`- und manuelle `visitor_entity`-Werte bleiben bestehen; Defaults gelten nur, wenn die jeweilige Option noch nicht vorhanden ist. Es gibt keine neue oder geänderte Option für den elastischen Talkback-Puffer.

0.5.10 übernimmt bei einem direkten Upgrade von älteren 0.5.x-Ständen bestehende flache Optionen sowie frühere Kanal-Aliase einmalig in die fünf gruppierten UI-Blöcke. Solange der persistente Migrationsmarker noch fehlt, gewinnen die alten flachen Werte bewusst gegen eventuell bereits vom Supervisor materialisierte Gruppen-Defaults. Nach erfolgreicher Migration bzw. sobald bereits eine reine gruppierte Konfiguration erkannt wurde, wird der Marker gesetzt. Ab dann sind ausschließlich die gruppierten Werte maßgeblich und normale Starts führen keinen Supervisor-Options-Write mehr aus. `nvr_channel_number` bleibt in der UI 1-basiert; der private Runtime-Snapshot wird vor Programmstart wieder im bewährten flachen Format erzeugt. Das nicht mehr wirksame `echo_cancellation_search_window_ms` bleibt aus den gespeicherten Home-Assistant-Optionen entfernt.


### Bashio-Kompatibilität ab 0.5.1

Die Bereinigung liest die aktuell gemounteten Optionen direkt aus `/data/options.json`. Für das atomare Zurückschreiben wird auf neuen Bashio-Versionen `bashio::app.options` verwendet; falls diese Funktion nicht vorhanden ist, fällt das Startskript auf `bashio::addon.options` zurück. Schlägt das Schreiben dennoch fehl, wird nur die Bereinigung übersprungen; das Gateway startet unabhängig davon weiter.

## Automatische Erkennung des Besucher-Sensors

Für **Reolink-Besucher-Sensor** kann `auto` verwendet werden. Das Gateway fragt beim Start die kompakte Home-Assistant-Registry-Ansicht `config/entity_registry/list_for_display` ab und sucht anhand der Felder `ei`, `pl` und `tk` einen aktivierten `binary_sensor` der Plattform `reolink` mit dem Translation-Key `visitor`. Dadurch bleibt die Erkennung auch erhalten, wenn die Entity-ID in Home Assistant umbenannt wurde. Die WebSocket-Verbindung akzeptiert dabei bis zu 16 MiB große Frames/Nachrichten, bleibt aber hart begrenzt.

Wird genau ein aktivierter Reolink-Besucher-Sensor gefunden, wird dessen Entity-ID nur in den privaten Runtime-Snapshot übernommen. Wird keiner gefunden, endet der Start mit einer klaren Fehlermeldung. Bei mehreren aktivierten Reolink-Türklingeln wird bewusst nicht geraten; in diesem Fall muss die gewünschte `binary_sensor...`-Entity manuell eingetragen werden. Ein manueller Wert hat immer Vorrang vor `auto`.

## Passivmodus

Die sichtbare Bezeichnung **Passivmodus** verwendet intern weiterhin den Schlüssel `dry_run`, damit bestehende Konfigurationen kompatibel bleiben. Im Passivmodus werden Klingelereignisse überwacht, aber SIP wird nicht registriert, es werden keine Anrufe gestartet und der akustische Kalibrierungsmarker wird nicht ausgegeben.
