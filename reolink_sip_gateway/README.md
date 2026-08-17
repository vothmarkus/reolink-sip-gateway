# Reolink SIP Gateway 1.0.0

Home-Assistant-App für Reolink Video Doorbells: Ein Klingelereignis kann einen SIP-Anruf auslösen; optional lässt sich die registrierte Gateway-Nebenstelle anrufen und direkt mit der Doorbell verbinden.

> Community-Projekt. Nicht offiziell von Reolink oder Home Assistant bereitgestellt oder unterstützt.

## 1.0.0: DTMF als reines Home-Assistant-Ereignis

1.0.0 handelt Out-of-Band-DTMF nach RFC 4733 als `telephone-event/8000` aus. Jeder vollständig empfangene Tastendruck (`0`–`9`, `*`, `#`, `A`–`D`) wird einmalig als flüchtiges `dtmf`-Ereignis über die bestehende Integrations-API übertragen. Wiederholte RTP-Endpakete erzeugen keine Duplikate.

`GET /api/v1/status` liefert weiterhin einen vollständigen Snapshot aus Gateway-, SIP-, Gesprächs- und Medienstatus. `GET /api/v1/events` überträgt Statusänderungen sowie DTMF unmittelbar als Server-Sent Events. DTMF verändert den Snapshot und dessen Revision nicht und wird nach einem Verbindungsabbruch nicht nachträglich wiederholt.

Jedes DTMF-Ereignis enthält die exakt normalisierte Gegenstelle als `remote_number` und die SIP-Dialog-ID als `call_id`. Bei eingehenden Anrufen ist die Gegenstelle der Anrufer, bei ausgehenden Anrufen das konfigurierte SIP-Ziel. Damit kann Home Assistant Berechtigungen pro Rufnummer prüfen und Eingaben sicher auf ein einzelnes Gespräch begrenzen.

`POST /api/v1/calls/test` startet einen normalen ausgehenden Anruf zum bereits unter SIP konfigurierten Ziel. `POST /api/v1/calls/hangup` beendet das aktuelle ein- oder ausgehende Gespräch; im Leerlauf ist der Aufruf bewusst folgenlos. Beide Befehle verwenden denselben neuen Call-Controller wie Besucherereignisse und eingehende SIP-Anrufe. Ein zweiter paralleler Gesprächspfad ist damit ausgeschlossen.

Beim ersten Start erzeugt die App unter `/data` eine stabile Instanz-ID und ein zufälliges 256-Bit-API-Token. Interner Add-on-Hostname und Token werden ausschließlich auf der administrativen Ingress-Seite angezeigt; die Companion-Integration erzeugt daraus selbst die feste API-Adresse. Die API erfordert Bearer-Authentifizierung und akzeptiert nur lokale beziehungsweise private Quelladressen. Es gibt keine neue Konfigurationsoption; vorhandene 0.8-Einstellungen bleiben unverändert.

Der vollständige Integrationsvertrag liegt maschinenlesbar als `docs/api-v1.openapi.yaml` im Repository. API-Version 1 bleibt unabhängig von der App-Version. Es gibt keine neue Konfigurationsoption und keine PIN-, Ziffernfolgen-, Kamera- oder Türöffnerlogik im Gateway; die Automation liegt ausschließlich in Home Assistant.

## 0.8.0: sichere und robuste eingehende Anrufe

0.8.0 ergänzt den in 0.7.0 eingeführten Telefon→Kamera-Weg um drei bewusst eigenständige Gateway-Funktionen. **Erlaubte Anrufer** enthält Rufnummern oder interne SIP-Benutzernamen, die vor SDP-Auswertung, Dialogreservierung und Kameraaufbau geprüft werden. Übliche Darstellungszeichen in Telefonnummern werden ignoriert; Landesvorwahlen werden aus Sicherheitsgründen nicht geraten. Der Kompatibilitätseintrag `*` erlaubt weiterhin alle Anrufer und muss allein in der Liste stehen. Nicht zugelassene Anrufe erhalten `403 Forbidden`.

Der standardmäßig aktivierte **Akustische Hinweiston** verwendet die ersten vier Symbole des bewährten Kalibrierungsmarkers. Der 256 ms kurze Ausschnitt wird über den tatsächlich konfigurierten Reolink-Talkback abgespielt, bevor das Gateway den eingehenden SIP-Anruf mit `200 OK` annimmt. Die vollständige Startkalibrierung und ihre Korrelation bleiben unverändert.

Der **RTP-Verbindungswächter** beendet einen Gesprächsrest, wenn trotz ausbleibendem `BYE` für die konfigurierte Zeit kein gültiges G.711-RTP-Paket der Gegenstelle mehr eintrifft. Er überwacht Paketaktivität und nicht den Audiopegel: Gesprächspausen bleiben deshalb unberührt, solange das Endgerät weiterhin RTP sendet. Der Wächter gilt für ein- und ausgehende Gespräche; die maximale Gesprächsdauer bleibt als zweite Grenze erhalten.

Die Reihenfolge unter **Anruf** lautet: Besucher-Sensor, eingehende Anrufe, erlaubte Anrufer, Hinweiston, Entprellung, Klingeldauer, RTP-Verbindungswächter und maximale Gesprächsdauer. v0.8 benötigt keine zusätzliche Home-Assistant-Integration; die dafür vorbereitete API folgt in 0.9, DTMF getrennt in 1.0.

## 0.7.0: die Kamera über SIP anrufen

Mit der neuen Option **Eingehende SIP-Anrufe zulassen** kann die registrierte SIP-Nebenstelle des Gateways angerufen werden. Bei einer FRITZ!Box wird dazu die dem IP-Telefon zugewiesene interne Nummer gewählt, beispielsweise `**620`. Das Gateway verbindet den Anrufer mit genau der bereits konfigurierten Doorbell beziehungsweise dem fest gewählten NVR-Kanal.

Der Anruf wird nicht voreilig angenommen: Nach dem `INVITE` sendet das Gateway zunächst `100 Trying`, handelt PCMA oder PCMU aus und bereitet Talkback sowie Kameraempfang vor. Erst wenn beide Reolink-Medienwege bereit sind, folgt die automatische Annahme mit `200 OK`. Schlägt der Kameraaufbau fehl, wird der Anruf mit `480 Temporarily Unavailable` abgewiesen, statt ein stummes Gespräch anzunehmen.

Unterstützt werden `INVITE`, `ACK`, `CANCEL` und `BYE`, Wiederholungen der `200 OK` bis zum `ACK`, dynamische RTP-Ports sowie symmetrisches RTP. Es bleibt bei genau einem Gespräch gleichzeitig; ein zweiter Anruf erhält `486 Busy Here`, und Klingelereignisse werden während eines laufenden Gesprächs wie bisher ignoriert. Der vorhandene Weg Besucher-Sensor → ausgehender Anruf bleibt funktional unverändert und verwendet denselben Medien-, AEC- und v0.6-Pufferpfad.

Der neue Schalter liegt im Block **Anruf** direkt nach dem Besucher-Sensor und ist standardmäßig aus. Signalisierung wird nur von IP-Adresse und UDP-Port des konfigurierten SIP-Registrars akzeptiert. Sollen ausschließlich interne FRITZ!Box-Anrufe zur Kamera gelangen, dürfen diesem IP-Telefon keine externen eingehenden Rufnummern zugewiesen werden; auch extern weitergeleitete Anrufe stammen technisch von der vertrauenswürdigen FRITZ!Box. DTMF, Mehrkameraauswahl und Türöffner sind bewusst noch nicht Bestandteil von 0.7.0.

## 0.6.0: elastischer Talkback-Puffer ohne zusätzlichen Vorpuffer

0.6.0 glättet kurze Takt- und Füllstandsschwankungen im SIP→Baichuan-Talkback. Für den gerade fälligen Reolink-Block werden vorhandene PCM-Samples abhängig von Füllstand und Versorgungstrend um höchstens 2 % gedehnt oder 3 % gestaucht. Reicht das nicht aus, werden die verbleibende Unterlaufkante und die Rückkehr des Signals mit einer kausalen 5-ms-Half-Hann-Blende geglättet. Nach einem unvermeidbaren Drop-oldest-Überlauf verbindet eine ebenso kurze kausale Überblendung den letzten ausgegebenen mit dem neuen Signalrand.

Dabei wartet der Playout-Pfad nie auf künftige Samples: Es gibt keinen zusätzlichen Vorpuffer, keinen Lookahead, keinen größeren FIFO und keinen späteren Reolink-Schreibtermin. Der bestehende Puffer bleibt auf vier ausgehandelte Reolink-Blöcke begrenzt. Die adaptive Stauchung baut einen angewachsenen Rückstand beziehungsweise eine vorübergehend durch Dehnung erhaltene Reserve wieder ab.

Die Änderung betrifft ausschließlich Telefon→Doorbell über Baichuan Live Talk. Kamera→SIP-Smoother/PLL, Startup-Kalibrierungsmarker, fester AEC-Coarse-Delay, AEC3 und der deaktivierte Go-Live-Tracker bleiben unverändert. Die AEC-Renderreferenz wird weiterhin nach der ADPCM-Kodierung zum tatsächlichen Reolink-Schreibzeitpunkt abgegriffen und enthält damit genau die hörbar gemachte Dehnung, Stauchung, Blende oder Stille.

Bei `log_level: debug` zeigt das Abschlusslog Rohfehlmenge und verbleibende Stille getrennt, FIFO-Minimum/-Mittel/-Maximum, Dehnungs-/Stauchungszähler und -verhältnisse, Versorgungstrend sowie Blenden und Überlauf-Splices. Neue Optionen gibt es nicht.

## 0.5.14: übersichtlichere Konfigurationsseite

0.5.14 übernimmt den Visitor-Hotfix aus 0.5.13 unverändert und verbessert ausschließlich die Darstellung der Home-Assistant-Konfiguration. Im SIP-Block stehen nun Registrar, Zugangsdaten, Ziel, Anzeigename und Codec vor den technischen Portangaben. **SIP-Port** und **Lokaler SIP-Port** stehen am Blockende und sind als **(erweitert)** gekennzeichnet.

Im Anruf-Block folgt die Klingelentprellung direkt auf den Besucher-Sensor. Im sichtbaren Block **Betrieb & Diagnose** steht der Passivmodus vor der Protokollstufe. Der interne Gruppenpfad bleibt `diagnostics`; alle 24 Optionskeys, Defaults, Schematypen, Migrationsregeln und Runtime-Pfade bleiben unverändert.

## 0.5.13: Visitor-Hotfix für große Home-Assistant-Installationen

0.5.13 behebt den Startfehler `websocket frame too large`, der bei `visitor_entity: auto` auf Installationen mit einer großen Entity Registry auftreten konnte. Statt der vollständigen Registry fordert das Gateway nun Home Assistants kompakte Ansicht `config/entity_registry/list_for_display` an und wertet deren Felder `ei`, `pl` und `tk` aus. Diese Ansicht enthält ausschließlich aktivierte Entities.

Die harte WebSocket-Grenze wird kontrolliert von 2 MiB auf 16 MiB angehoben. Übergroße Frames werden weiterhin anhand ihrer Längenangabe verworfen, bevor ein Payload-Puffer angelegt wird. Die Auswahl bleibt strikt: genau ein Reolink-`binary_sensor` mit `translation_key=visitor` wird verwendet; bei keinem oder mehreren Treffern ist weiterhin eine manuelle Auswahl erforderlich.

Audio, AEC, Kalibrierung, Baichuan, RTP, Media und Startup bleiben gegenüber 0.5.12 unverändert.

## 0.5.12: automatische Visitor-Erkennung und Passivmodus

Für Neuinstallationen steht `visitor_entity` standardmäßig auf `auto`. Der Startadapter fragt über die bereits vorhandene Home-Assistant-WebSocket-Verbindung die Entity Registry ab und sucht genau einen aktivierten Reolink-`binary_sensor` mit `translation_key=visitor`. Die konkrete Entity-ID wird nur in den privaten Runtime-Snapshot geschrieben. Umbenannte Entity-IDs bleiben damit automatisch auffindbar.

Bei mehreren aktivierten Reolink-Türklingeln wird bewusst nicht geraten; in diesem Fall muss die gewünschte `binary_sensor...`-Entity manuell angegeben werden. Ein vorhandener manueller Wert bleibt beim Update unverändert und überspringt die Erkennung.

Die bisherige UI-Bezeichnung **Testbetrieb** heißt jetzt **Passivmodus**. Intern bleibt der Schlüssel `dry_run` kompatibel bestehen. Im Passivmodus werden Klingelereignisse überwacht, aber SIP wird nicht registriert, es werden keine Anrufe gestartet und der akustische Kalibrierungsmarker wird nicht ausgegeben.

Die in 0.5.11 eingeführten Defaults bleiben bestehen: `sip_registrar: auto` verwendet die IPv4-Default-Gateway-Adresse des Home-Assistant-Hosts und `reolink_username` ist bei Neuinstallationen `admin`.

## 0.5.10: finaler 0.5.x-Stand

0.5.10 schließt die Konfigurationsmigration der in 0.5.8 eingeführten fünf UI-Blöcke ab. Nach einer einmaligen Legacy-Migration ist ausschließlich die gruppierte Darstellung maßgeblich; normale Starts lesen die Supervisor-Konfiguration nur noch und schreiben sie nicht mehr zurück. Ein persistenter Migrationsmarker verhindert, dass alte flache 0.5.x-Schlüssel später erneut Vorrang vor aktuellen gruppierten Benutzerwerten erhalten.

Die fünf Blöcke bleiben intern **Reolink**, **SIP-Telefonie**, **Audio**, **Anruf** und `diagnostics`. Der letzte Block hieß zunächst sichtbar **Diagnose** und trägt seit 0.5.14 die präzisere Bezeichnung **Betrieb & Diagnose**. `visitor_entity` liegt im Block „Anruf“. Die frühere AEC-Suchfenster-Option bleibt entfernt; intern wird nur noch ein fester Kompatibilitätswert an den unveränderten Runtime-Konfigurationsparser übergeben.

`icon.png`, `logo.png` und das in der Ingress-Seite eingebettete Logo besitzen ab 0.5.10 einen echten Alphakanal; der frühere weiße Außenrand wurde entfernt, das eigentliche Logo bleibt unverändert.

Für die öffentliche Veröffentlichung wurde außerdem der Go-Modulpfad auf `github.com/vothmarkus/reolink-sip-gateway` ausgerichtet. Das ist ausschließlich eine Importpfad-/Metadatenänderung; die Audio-, AEC-, Baichuan-, SIP/RTP- und Kalibrierungslogik bleibt funktional unverändert.

## 0.5.1: Kompatibilität und Branding

0.5.1 ist bewusst ein kleines Polishing-Release. Die Audio-, Baichuan-, SIP-, Kalibrierungs- und native WebRTC-AEC-Implementierung aus 0.5.0 bleibt unverändert. Behoben wird die Optionsmigration auf Home-Assistant-Basisimages mit älterem Bashio; zusätzlich enthält die App nun das neue Reolink-SIP-Gateway-Icon/Logo und zeigt es auch auf der Ingress-Statusseite.

## 0.5.0: automatische, profilbasierte Audiokonfiguration

0.5.0 reduziert die öffentliche Konfiguration deutlich und macht die in 0.4.3 erfolgreich getestete native WebRTC-AEC zum normalen Betriebsweg.

### Reolink-Modus

Nur noch `reolink_mode` bestimmt den kompletten Medienweg:

- `auto`: erkennt beim App-Start genau ein vollständiges Profil und hält dieses für alle Anrufe fest.
- `standalone`: Doorbell → SIP über RTSP; SIP → Doorbell über ONVIF-RTSP-Backchannel.
- `nvr`: Doorbell → SIP über Baichuan `sub`; SIP → Doorbell über Baichuan Live Talk.

Es gibt keinen unabhängigen Hin-/Rückkanal-Fallback mehr innerhalb eines laufenden Gesprächs.

### Automatische AEC-Latenzkalibrierung

Wenn `echo_cancellation_enabled` aktiv ist, misst die App bei jedem normalen Start automatisch die lokale akustische Reolink-Laufzeit. Dazu wird ein etwa einsekündiger codierter Marker über den später verwendeten Talkback-Weg abgespielt und über den später verwendeten Empfangsweg korreliert. Der Marker ist an der Doorbell hörbar.

Der Messwert wird unmittelbar zum AEC-Coarse-Delay und bleibt während des gesamten Gesprächs fest. Der frühere Go-1-ms-Live-Tracker ist seit 0.5.7 deaktiviert; die verbleibende Feinausrichtung übernimmt der interne AEC3-Delay-Estimator. Das frühere AEC-Suchfenster ist daher ab 0.5.10 keine Benutzeroption mehr.

Eine erfolgreiche Messung wird in `/data/aec-calibration.json` gespeichert. Falls eine spätere Startmessung fehlschlägt, wird nur eine zur aktiven Reolink-Konfiguration passende gespeicherte Messung verwendet; andernfalls gilt der sichere 1450-ms-Fallback.

Im **Passivmodus** (`dry_run`) wird kein hörbarer Marker gesendet.

### Native WebRTC-AEC

GStreamer ist seit 0.4.3 nicht mehr Teil des AEC-Pfads. Go richtet die lange Reolink-Laufzeit aus; der native `reolink-aec-helper` erhält anschließend exakt ein 10-ms-Render-/Capture-Paar und ruft WebRTC AudioProcessing direkt auf.

0.5.0 behebt außerdem die Statistik-Bitmaske von 0.4.3. ERL, ERLE, Residual-Echo-Wahrscheinlichkeit, Divergenz und interne Delay-Werte werden bei `log_level: debug` nun mit den gleichen Bitpositionen interpretiert wie im C++-Helper.

## Wesentliche Konfiguration

```yaml
reolink:
  reolink_host: 192.168.177.50
  reolink_username: admin
  reolink_password: "..."
  reolink_mode: auto
  nvr_channel_number: 2
  reolink_rtsp_port: 554
  baichuan_port: 9000

sip:
  sip_registrar: auto
  sip_username: "..."
  sip_password: "..."
  sip_destination: "**610"
  sip_display_name: Haustür
  sip_codec_preference: pcma
  sip_registrar_port: 5060
  sip_local_port: 5070

audio:
  echo_cancellation_enabled: true
  webrtc_high_pass_filter_enabled: true
  webrtc_noise_suppression_enabled: true

call:
  visitor_entity: auto
  incoming_calls_enabled: false
  incoming_allowed_callers:
    - "*"
  incoming_connection_tone_enabled: true
  debounce_seconds: 3
  ring_timeout_seconds: 30
  rtp_inactivity_timeout_seconds: 15
  max_call_duration_seconds: 300

diagnostics:
  dry_run: false
  log_level: info
```

Bei `sip_registrar: auto` wird beim Start die IPv4-Default-Gateway-Adresse des Home-Assistant-Hosts verwendet. Ein manueller Registrar überschreibt diese Automatik. Kann kein nutzbares IPv4-Gateway gefunden werden, fordert das Startlog dazu auf, den Registrar manuell einzutragen.

Bei `visitor_entity: auto` wird genau ein aktivierter Reolink-Besucher-Sensor aus der Home-Assistant-Entity-Registry verwendet. Bei keinem oder mehreren Treffern fordert das Startlog zur manuellen Auswahl auf.

Mit `incoming_calls_enabled: true` nimmt das Gateway Anrufe an seine registrierte SIP-Nebenstelle automatisch an, sobald der konfigurierte Reolink-Medienweg bereit ist. Bei einer FRITZ!Box wird die in **Telefonie → Telefoniegeräte** angezeigte interne Nummer des Gateway-IP-Telefons gewählt.

RTP-Ports werden automatisch vom Betriebssystem gewählt. FFmpeg liegt fest unter `/usr/bin/ffmpeg`. Die WebRTC-Rauschunterdrückung verwendet bei Aktivierung fest `moderate`. Der Home-Assistant-WebSocket ist der primäre Klingelpfad; der REST-Fallback läuft intern mit festem Einsekundenintervall.

## Betrieb & Diagnose

`log_level: info` ist für normalen Betrieb gedacht. `log_level: debug` aktiviert zusätzlich SIP-, RTSP-, Baichuan-, RTP-, Puffer-, AEC-Tracker- und native WebRTC-Statistiken. Separate Debug-Schalter gibt es nicht mehr.

Die Ingress-Seite zeigt unter anderem:

- konfigurierten und tatsächlich aktiven Reolink-Modus,
- den aktiven Medienweg,
- Status und Ergebnis der Startkalibrierung,
- kalibrierte und aktuelle AEC-Latenz,
- den daraus berechneten Suchbereich,
- SIP-/Home-Assistant-Verbindungsstatus und aktuelle Call-Medien.

## Hardwarestatus

0.4.3 wurde auf der Zielhardware mit NVR/Baichuan und nativer WebRTC-AEC erfolgreich über einen 53-s-Testanruf betrieben. Die Echoreduktion blieb subjektiv gleichmäßig; der Long-Delay-Tracker lag stabil bei etwa 1429–1430 ms. Double-Talk bleibt noch separat zu prüfen. 0.5.0 ändert den bewährten Call-AEC-Kern nicht, sondern automatisiert dessen Startwert und räumt Konfiguration/Diagnose auf.
