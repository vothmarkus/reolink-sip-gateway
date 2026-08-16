# Reolink SIP Gateway 0.5.14

Home-Assistant-App für Reolink Video Doorbells: Ein Klingelereignis löst einen SIP-Anruf aus; Audio läuft bidirektional zwischen SIP-Endgerät und Doorbell.

> Community-Projekt. Nicht offiziell von Reolink oder Home Assistant bereitgestellt oder unterstützt.

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
  debounce_seconds: 3
  ring_timeout_seconds: 30
  max_call_duration_seconds: 300

diagnostics:
  dry_run: false
  log_level: info
```

Bei `sip_registrar: auto` wird beim Start die IPv4-Default-Gateway-Adresse des Home-Assistant-Hosts verwendet. Ein manueller Registrar überschreibt diese Automatik. Kann kein nutzbares IPv4-Gateway gefunden werden, fordert das Startlog dazu auf, den Registrar manuell einzutragen.

Bei `visitor_entity: auto` wird genau ein aktivierter Reolink-Besucher-Sensor aus der Home-Assistant-Entity-Registry verwendet. Bei keinem oder mehreren Treffern fordert das Startlog zur manuellen Auswahl auf.

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
