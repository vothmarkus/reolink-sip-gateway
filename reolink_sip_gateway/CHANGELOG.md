# Changelog

## 0.6.0

- SIP→Baichuan-Talkback erhält einen füllstands- und trendabhängigen elastischen Block-Playout: maximal 2 % Zeitdehnung bei knapper Versorgung und maximal 3 % Zeitstauchung zum Abbau eines Rückstands oder einer vorübergehend erhaltenen Reserve.
- Restunterläufe werden nicht kaschiert oder verzögert: fehlende Samples bleiben Stille, der gültige Signalrand und die spätere Rückkehr werden jedoch kausal über 5 ms mit einer Half-Hann-Fensterfunktion aus- beziehungsweise eingeblendet.
- Nach unvermeidbarem Drop-oldest-Überlauf verbindet ein kausaler 5-ms-Splice den letzten ausgegebenen Samplewert mit dem neuen FIFO-Anfang und reduziert dadurch harte Sprünge.
- Keine neue Signalwartezeit: kein Lookahead, kein zusätzlicher Startpuffer, kein größerer FIFO, keine zusätzliche Timerperiode und keine Änderung an ausgehandelter Blockgröße oder Baichuan-Schreibkadenz. Der bestehende FIFO bleibt auf vier Reolink-Blöcke begrenzt.
- Debug-Abschlussstatistik unterscheidet rohe Fehlmenge von verbleibender Stille und ergänzt FIFO-Füllstandsbereich/-mittel, Stretch-/Compress-Zähler und -Verhältnisse, Versorgungstrend, Blenden und Überlauf-Splices.
- AEC-Renderreferenz bleibt nach der IMA-ADPCM-Kodierung am tatsächlichen Reolink-Write. Kamera→SIP-Playout-PLL, Startup-Kalibrierungsmarker, fester Coarse-Delay, AEC3, deaktivierter Go-Live-Tracker und native WebRTC-Verarbeitung bleiben unverändert.
- Synthetische Regressionstests decken kleine und harte Unterläufe, vollständigen Leerlauf, Trendreaktion und Reserveabbau, Hochwasser-Kompression, Überlauf-Splice, Verhältnisgrenzen, Fensterkanten, FIFO-Statistik und bestehende ADPCM/AEC-Write-Semantik ab.
- Home-Assistant-Optionen, Gruppierung, Defaults und Migration bleiben unverändert; App-, Gateway-, SIP-/RTSP-User-Agent- und Container-Buildversion werden auf 0.6.0 angehoben.

## 0.5.14

- Konfigurationsseite logisch neu sortiert: SIP-Zugang und Rufparameter zuerst, anschließend Codec sowie die als „erweitert“ gekennzeichneten SIP-Ports; Klingelentprellung direkt nach dem Besucher-Sensor; Passivmodus vor Protokollstufe.
- Sichtbarer Blockname **Diagnose** in **Betrieb & Diagnose** beziehungsweise **Operation & diagnostics** präzisiert. Der interne Gruppenpfad `diagnostics`, alle 24 Schlüssel und sämtliche Defaults bleiben unverändert.
- Der Visitor-Hotfix aus 0.5.13 wird unverändert übernommen. Keine Änderung an Entity-Erkennung, WebSocket-Grenzen, Migration, Runtime-Snapshot, AEC, Kalibrierung, Baichuan, RTP, Media, Startup, internem Konfigurationsparser oder nativem Helper.
- SIP-/RTSP-Code unterscheidet sich funktional nicht von 0.5.13; nur der User-Agent wurde auf 0.5.14 angehoben.

## 0.5.13

- Behebt `websocket frame too large` bei `visitor_entity: auto` auf Home-Assistant-Installationen mit großer Entity Registry.
- Registry-Abfrage von `config/entity_registry/list` auf die kompakte, nur aktivierte Entities enthaltende Ansicht `config/entity_registry/list_for_display` umgestellt; ausgewertet werden `ei`, `pl` und `tk`.
- WebSocket-Frame-/Nachrichtenlimit kontrolliert von 2 MiB auf 16 MiB erhöht. Frames oberhalb der Grenze werden weiterhin vor der Payload-Allokation abgewiesen.
- Auswahl bleibt strikt: genau ein aktivierter Reolink-`binary_sensor` mit `translation_key=visitor`; null oder mehrere Treffer führen zu einer verständlichen Aufforderung zur manuellen Auswahl.
- Manuelle Visitor-Entities, Passivmodus-Verhalten, Konfigurationsmigration und Runtime-Snapshot-Persistenz bleiben unverändert.
- Keine Änderung an AEC, Kalibrierung, Baichuan, RTP, Media, Startup, internem Konfigurationsparser oder nativem Helper. SIP-/RTSP-Code unterscheidet sich funktional nicht; nur der User-Agent wurde auf 0.5.13 angehoben.

## 0.5.12

- `visitor_entity: auto` als neuer Standard für Neuinstallationen: der Startadapter ermittelt den aktivierten Reolink-Besucher-/Klingelsensor über die Home-Assistant-Entity-Registry (`platform=reolink`, `translation_key=visitor`).
- Manuell konfigurierte Visitor-Entities bleiben unverändert und überspringen die Auto-Erkennung vollständig.
- Bei mehreren aktivierten Reolink-Besucher-Sensoren wird bewusst nicht geraten; der Start bricht mit den gefundenen Entity-IDs und dem Hinweis auf manuelle Auswahl ab. Bei ausschließlich deaktivierten Kandidaten werden diese explizit genannt.
- Die UI-Bezeichnung `Testbetrieb` wurde in **Passivmodus** geändert. Intern bleibt der Schlüssel `dry_run` aus Kompatibilitätsgründen unverändert. Der Passivmodus überwacht Klingelereignisse, registriert aber kein SIP, startet keine Anrufe und sendet keinen akustischen Kalibrierungsmarker.
- Keine Änderung an AEC, Kalibrierung, Baichuan, RTP, Playout-PLL oder Talkback-Pufferung. SIP-/RTSP-Code unterscheidet sich funktional nicht; nur der User-Agent wurde auf 0.5.12 angehoben.

## 0.5.11

- Neuer Home-Assistant-Standardwert `sip_registrar: auto`: vor Gatewaystart wird die IPv4-Default-Gateway-Adresse des Home-Assistant-Hosts aus der Routingtabelle ermittelt. Bei typischen FRITZ!Box-Installationen ist das automatisch die FRITZ!Box-Adresse.
- Ein manuell eingetragener SIP-Registrar (IP oder DNS-Name) hat weiterhin Vorrang und wird unverändert verwendet. Kann bei `auto` kein nutzbares IPv4-Gateway ermittelt werden, bricht der Start mit einer klaren Handlungsanweisung ab.
- Standardwert für `reolink_username` auf `admin` gesetzt; bestehende gespeicherte Benutzerwerte werden beim Update nicht überschrieben.
- Deutsche und englische Feldbeschreibung erklärt direkt unter `SIP-Registrar`, dass `auto` die Gateway-IP des Home-Assistant-Hosts verwendet.
- Auto-Erkennung liest `/proc/net/route`, benötigt kein zusätzliches `iproute2`-Paket und wählt bei mehreren Gateway-Routen die nutzbare Route mit der niedrigsten Metrik.
- Keine Änderung an AEC, Kalibrierung, Baichuan, SIP/RTP-Medienpfad, RTSP, Playout-PLL oder Talkback-Pufferung.
- Version und SIP-/RTSP-User-Agent auf 0.5.11 angehoben.

## 0.5.10

- **Konfigurationsmigration abgeschlossen:** persistenter Migrationsmarker trennt die einmalige Übernahme alter flacher 0.5.x-Werte vom normalen Betrieb.
- Vor der ersten bestätigten Migration haben Legacy-Werte weiterhin Vorrang vor eventuell materialisierten Gruppen-Defaults; danach sind ausschließlich die fünf gruppierten UI-Blöcke maßgeblich.
- Normale Starts schreiben keine Optionen mehr an den Home-Assistant-Supervisor zurück. Der compare-before-write-Schutz bleibt für die einmalige Migration erhalten.
- `icon.png`, `logo.png` und das eingebettete Ingress-Logo auf echten Alphakanal umgestellt; weißer Außenrand entfernt, Logo-Inhalt unverändert.
- Go-Modul-/Importpfad für die öffentliche Veröffentlichung auf `github.com/vothmarkus/reolink-sip-gateway` ausgerichtet.
- Keine funktionale Änderung an Startup-Kalibrierung, AEC, Baichuan, SIP/RTP, RTSP, Playout-PLL oder Pufferlogik.
- Version und SIP-/RTSP-User-Agent auf 0.5.10 angehoben.

## 0.5.9

- **Persistenz-Hotfix für die gruppierte 0.5.8-Konfiguration:** die einmalige Supervisor-Optionsmigration läuft nicht mehr asynchron neben dem Gateway.
- Vor jedem Migrations-Write wird `/data/options.json` erneut gelesen und kanonisch mit dem ursprünglichen Snapshot verglichen. Hat sich die Konfiguration inzwischen geändert, wird der veraltete Write verworfen statt Benutzerwerte zu überschreiben.
- Runtime-Snapshot wird weiterhin vor der Persistenzmigration erzeugt; Kanalabbildung, AEC, Startup-Kalibrierung, Baichuan, SIP/RTP, RTSP und Pufferlogik bleiben unverändert.
- Version und SIP-/RTSP-User-Agent auf 0.5.9 angehoben.

## 0.5.8

- Home-Assistant-Konfiguration in fünf Blöcke gruppiert: **Reolink**, **SIP-Telefonie**, **Audio**, **Anruf** und **Diagnose**; `visitor_entity` liegt im Block „Anruf“.
- Gruppierung ist ausschließlich eine UI-/Speichergrenze. Vor Gatewaystart werden die Werte wieder auf die bewährte flache v0.5.7-Runtimekonfiguration abgebildet.
- `echo_cancellation_search_window_ms` aus UI und öffentlichem Schema entfernt, da der Go-Live-Delaytracker deaktiviert ist. Beim Upgrade wird ein alter gespeicherter Wert entfernt; intern bleibt für den unveränderten Go-Konfigurationsparser ein fester Kompatibilitätswert von 300 ms.
- Bestehende flache 0.5.x-Optionen und ältere Kanal-Aliase werden verlustfrei in die gruppierte Darstellung migriert. Der Runtime-Snapshot wird weiterhin **vor** der asynchronen Supervisor-Bereinigung erzeugt.
- Kein funktionaler Eingriff in Startup-Kalibrierung, AEC, Baichuan, SIP/RTP, RTSP, Playout-PLL oder Pufferlogik.
- Version und SIP-/RTSP-User-Agent auf 0.5.8 angehoben.

## 0.5.7

- **AEC-Regelkreise getrennt:** Der laufende Go-Long-Delay-Tracker ist im Produktionspfad deaktiviert. Der beim Start akustisch kalibrierte Reolink-Coarse-Delay bleibt für den gesamten Call unverändert.
- Der interne AEC3-Delay-Estimator bleibt unverändert aktiv und übernimmt ausschließlich die verbleibende Feinausrichtung; `set_stream_delay_ms(0)` sowie der native WebRTC-Helper bleiben unverändert.
- Hintergrund: Hardwarelog zeigte einen stabilen Go-Korrelationskandidaten bei ca. 1608 ms, während AEC3 gleichzeitig ca. 236 ms internen Delay meldete. `1608-236≈1372 ms` entsprach nahezu exakt dem funktionierenden externen Alignment um 1367–1397 ms; das Go-Nachführen auf 1608 ms verschlechterte ERLE und Residual-Echo deutlich.
- Startup-Kalibrierung, Baichuan-/RTSP-/SIP-Medienpfad, Playout-PLL, native AEC-Verarbeitung und Kanalabbildung aus 0.5.6 bleiben unverändert.
- Debug-/Ingress-Ausgabe kennzeichnet Live-Tracking nun korrekt als deaktiviert; `native_delay_ms`, ERLE und Residual-Echo-Likelihood bleiben erhalten.
- `echo_cancellation_search_window_ms` bleibt vorerst aus Optionskompatibilität im Schema, bewegt den Live-Delay in 0.5.7 jedoch nicht.
- Neuer Regressionstest stellt sicher, dass ein absichtlich abweichender synthetischer Echo-Delay den kalibrierten Produktions-Delay nicht mehr verschiebt.

## 0.5.6

- **Startfix:** UI→Runtime-Adapter verwendet für variable JSON-Werte direkt `jq --arg/--argjson`; behebt die 0.5.5-Restartschleife mit `$channel is not defined` / `$path is not defined`.
- Kontrollierter Rebase auf den hardwarebestätigten 0.5.1-Runtime-/Media-/Kalibrierungspfad.
- Benutzeroberfläche zeigt nur noch einen 1-basierten `nvr_channel_number`; interne Kanalnummer und RTSP-Pfad werden ausschließlich im Startskript auf die bewährten 0.5.1-Werte übersetzt.
- NVR-Kanal 2 ergibt intern exakt `nvr_channel=1` und `/Preview_02_sub`; im Standalone-Modus wird die NVR-Kanalangabe ignoriert und `/Preview_01_sub` verwendet.
- `reolink_stream_path` und der interne 0-basierte `nvr_channel` aus dem öffentlichen Schema entfernt.
- Upgrade-Migration übernimmt 0.5.0/0.5.1-Kanäle einmalig in die 1-basierte UI-Darstellung.
- Keine Änderungen an `internal/config`, `internal/startup`, `internal/calibration`, `internal/media`, Baichuan-/Baichuan-Audio-Logik oder der Reihenfolge der Go-Aufrufe gegenüber 0.5.1.
- Version und SIP-/RTSP-User-Agent auf 0.5.6 angehoben.

## 0.5.1

- Kompatibilitätsfix für die einmalige 0.4.x→0.5.x-Optionsmigration: Lesen erfolgt direkt aus `/data/options.json`; Schreiben verwendet `bashio::app.options` auf neuen bzw. `bashio::addon.options` auf älteren Bashio-Versionen.
- Der in 0.5.0 beobachtete Fehler `bashio::app.options: command not found` ist damit behoben; die Audio-/AEC-Laufzeitpfade bleiben unverändert.
- Neues Reolink-SIP-Gateway-Branding als `icon.png` und `logo.png` für die Home-Assistant-App ergänzt.
- Dasselbe Logo in die Ingress-Statusseite eingebettet und deren Kopfbereich für Desktop/Mobilgeräte angepasst.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.5.1 angehoben.

## 0.5.0

- Öffentliche App-Konfiguration von 42 auf 26 Optionen reduziert.
- `reolink_mode: auto|standalone|nvr` steuert jetzt ein vollständiges Hin-/Rückkanalprofil; kein per-Call-Mischen oder Fallback mehr.
- Baichuan-Empfang fest auf `sub`; RTSP-/Baichuan-Port und NVR-Kanal als zusammenhängende erweiterte Reolink-Felder.
- Automatische akustische Latenzkalibrierung bei jedem normalen App-Start; Messwert wird AEC-Startwert.
- Erfolgreiche AEC-Kalibrierung wird profilgebunden in `/data/aec-calibration.json` gespeichert; bei Messfehler Cache, sonst 1450-ms-Fallback.
- AEC-Delaytracking bei aktiver AEC immer an; Benutzer konfiguriert nur noch das Suchfenster ± ms.
- WebRTC-Noise-Suppression-Level fest `moderate`; HPF und Noise Suppression bleiben an/aus schaltbar.
- Native WebRTC-Statistik-Bitmaske korrigiert und durch explizite Bits 0…7 plus Wire-Protokolltest abgesichert.
- `log_level: debug` ersetzt separate SIP-/RTSP-/Baichuan-Diagnoseschalter; detaillierte AEC-Abschlussstatistik ist ebenfalls Debug-only.
- Manuelle `camera_test`, `backchannel_test`, `baichuan_test` und `latency_test` entfernt; verbliebene Messlogik in reguläres `internal/calibration` überführt.
- `ha_poll_interval_ms` entfernt; REST-Fallback intern fest auf 1 s.
- `rtp_port` entfernt; SIP-RTP-Port wird pro Call vom Kernel reserviert und vor INVITE ins SDP übernommen.
- `ffmpeg_path` entfernt; Container verwendet fest `/usr/bin/ffmpeg`.
- Ingress-Statusseite zeigt aktives Medienprofil, Kalibrierungsstatus, Start-/Aktuallatenz und Suchbereich.
- Update-Migration übernimmt `connection_mode` und `baichuan_channel` in die neuen Schlüssel und entfernt alle bekannten Altoptionen atomar in einem einzigen Supervisor-Options-Update; bei einem Schreibfehler bleiben die bisherigen Optionen vollständig erhalten und werden beim nächsten Start erneut migriert.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.5.0 angehoben.

## 0.4.3

- **Build-Fix für Debian Trixie:** WebRTC-Development-Header werden beim nativen Helper als System-Header (`-isystem`) eingebunden. Dadurch bleibt `-Werror` für unseren eigenen C++-Code aktiv, während die bekannte `unused-parameter`-Warnung innerhalb des Debian-WebRTC-Headers den Add-on-Build nicht mehr abbricht.
- GStreamer `webrtcdsp`/`webrtcechoprobe` vollständig aus dem AEC-Signalpfad entfernt; die große Reolink-Verzögerung wird weiterhin nur einmal im Go-Ringpuffer ausgerichtet.
- Kleinen nativen `reolink-aec-helper` gegen Debian `libwebrtc-audio-processing-1` ergänzt. Pro 10-ms-Frame: `ProcessReverseStream()` → `set_stream_delay_ms(0)` → `ProcessStream()`.
- Native WebRTC-`AudioProcessing::GetStatistics()` über ein festes binäres Helper-Protokoll zurück an Go geführt. Logs enthalten ERL, ERLE, internen Delay, Delay-Median/-StdDev, Residual-Echo-Likelihood und Divergent-Filter-Fraction, soweit verfügbar.
- `AEC health` von ca. 10 s auf ca. 5 s verkürzt, um den real beobachteten Wechsel gut/schlecht zeitlich feiner sichtbar zu machen.
- Long-Delay-Tracker von 10-ms-Raster auf **1-ms-Auflösung** umgestellt. Die historische Renderreferenz wird dafür auf der 8-kHz-Sample-Timeline über benachbarte 10-ms-Blöcke rekonstruiert, statt auf den nächsten ganzen APM-Frame gerundet zu werden.
- Nativen Helper-Prozesspfad gehärtet: stdout wird vollständig vor `Wait()` drainiert, der letzte Reply bei sofortigem Helper-Exit bleibt erhalten, stderr wird race-frei gepuffert und SIGPIPE im Helper kontrolliert behandelt.
- `echo_cancellation_suppression` entfernt; die bisherige GStreamer-Einstellung war für die verwendete Runtime kein belastbarer AEC-Regler. Altwert wird beim Start bestmöglich aus gespeicherten HA-Optionen entfernt.
- WebRTC-Hochpass und Noise Suppression bleiben auswählbar; AGC bleibt aus.
- GStreamer-Runtimepakete aus dem Dockerfile entfernt; Runtime verwendet `libwebrtc-audio-processing-1-3`, Builder `libwebrtc-audio-processing-dev`.
- Protokoll-/Fake-Child-Regressionsprüfungen für den nativen Helper ergänzt.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.4.3 angehoben.

## 0.4.2

- Reales 0.4.1-Langzeitlog ausgewertet: bei hohem SIP-RTP-Jitter driftete der AEC-Tracker bis 1,55/1,62 s und die subjektive Echo-Unterdrückung nahm ab; gleichzeitig wurden im Talkback-Pfad relevante Stille und im Kamera-Pfad Hard-Drops/Underruns sichtbar.
- AEC-Far-End-Referenz vom SIP-Eingang an den **tatsächlichen Reolink-Playout-Write** verlegt. Baichuan rekonstruiert die Referenz aus dem exakt gesendeten IMA-ADPCM-Block; Standalone-RTSP verwendet den tatsächlich geschriebenen G.711-Chunk.
- Damit berücksichtigt die AEC-Referenz RTP-Jitter, eingefügte Stille, FIFO-Verwerfungen, Resampling, Codec-Framing und reale Transport-Sendezeit.
- Delay-Tracker gehärtet: drei konsistente eindeutige Kandidaten innerhalb ±20 ms sind vor einem Update erforderlich; einzelne Sprachkorrelationspeaks können den Delay nicht mehr verschieben.
- Nach Kamera-Hard-Drop, echtem Underrun, Media-Clock-Rebase oder relevantem Render-Timeline-Rebase wird nur die Delay-Adaption 1,5 s pausiert; AEC bleibt mit dem letzten stabilen Delay aktiv.
- Baichuan→SIP-Clockpfad als Burst-Smoother überarbeitet: einmalig 120 ms Prebuffer, 320 ms Hard-Cap, Recovery auf 180 ms, 60–260-ms-Deadband und langsame ASRC-Korrektur bis ca. ±1250 ppm.
- Virtuelle 8-kHz-Kamera-Medienzeit eingeführt, damit AAC-Decoder-Bursts wieder als kontinuierliche Quell-Timeline behandelt werden und die lokale Smoother-Wartezeit nicht in den AEC-Delay eingeht.
- Neue Smoother-Diagnose (`smoother_*`) für Queue, Drops, Underruns, Clock-Korrektur, Decoder-Chunkgröße und Arrival-Gaps.
- Neue periodische `AEC health`-Diagnose (ca. alle 10 s) einschließlich `recent_erle_db`, Tracker-Kandidat/Streak und Suspensionen; Abschlusslog enthält kumulative und jüngste ERLE.
- Regressionstests für encoded-pl[a-z]*/out playout reference, Burstcluster, virtuelle Medienzeit, 3-fach bestätigtes Delaytracking und Tracking-Suspension ergänzt.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.4.2 angehoben.

## 0.4.1

- Reale 0.4.0-Hardwareprüfung ausgewertet: WebRTC AEC funktioniert; `high` reduzierte das Eigen-Echo deutlich stärker als `moderate`. Live-Tracker bewegte sich Richtung ca. 1,45–1,48 s.
- AEC-Startdelay auf **1450 ms** und Default-Suppression auf **high** gesetzt.
- Talkback-Gain und Soft-Ducking samt Konfigurations-, Status- und Laufzeitcode vollständig entfernt; AEC bleibt der bevorzugte Full-Duplex-Ansatz.
- Beim ersten Start nach dem Update werden bereits gespeicherte Gain-/Ducking-Altoptionen per Bashio/Supervisor-Konfigurationsmigration entfernt, damit keine verwaisten Schema-Warnungen zurückbleiben.
- WebRTC-Hochpassfilter separat auswählbar, standardmäßig aktiv.
- WebRTC-Noise-Suppression separat auswählbar (`low|moderate|high|very-high`), standardmäßig aktiv auf `moderate`.
- AGC, experimentelle AGC und VAD bleiben bewusst deaktiviert, damit kein zusätzlicher adaptiver Pegelsteller Rest-Echo oder Rauschen anhebt.
- Bestehende AEC-Erweiterungen `extended-filter` und `delay-agnostic` bleiben aktiv.
- Baichuan→SIP-Festtakt-FIFO durch eine PI-geregelte ASRC/PLL ergänzt: 52,5-ms-Zielqueue, kontinuierliche fractional lineare Interpolation, exakt 160 SIP-Samples/20 ms und maximal ±1,25 % Clock-Korrektur.
- 120-ms-Hard-Cap bleibt als letzte Latenzsicherung erhalten; neue Debug-Metriken zeigen PLL-Queue, Hard-Drops, Unterläufe und Clock-Korrektur in ppm.
- Neue PLL-Regressionstests simulieren 512-Sample-AAC-Bursts und Quellclock-Drift über 60 s.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.4.1 angehoben.

## 0.4.0

- Optionale WebRTC-basierte Acoustic Echo Cancellation (AEC) für Doorbell→Telefon ergänzt.
- Lange Reolink-Echolaufzeit wird vor dem WebRTC-Filter über einen eigenen Referenz-Ringpuffer grob ausgerichtet; Default 1400 ms.
- Optionaler adaptiver Delay-Tracker sucht im konfigurierbaren Fenster und führt den Referenzversatz nur bei eindeutig korrelierten Sprachabschnitten langsam nach.
- AEC arbeitet in 10-ms-/8-kHz-Frames; SIP-RTP bleibt unverändert bei 20 ms G.711.
- AEC gilt sowohl für RTSP- als auch Baichuan-Empfang. Talkback-Gain wird vor dem Referenzabgriff berücksichtigt.
- WebRTC-AEC-Stärke als low/moderate/high auswählbar; AGC und Noise Suppression bleiben im AEC-Prozess deaktiviert.
- Laufzeitbasis auf Home-Assistant Debian trixie umgestellt; GStreamer webrtcdsp/webrtcechoprobe werden als optionale AEC-Runtime installiert.
- AEC-Diagnose protokolliert aktuellen Delay, Tracker-Konfidenz und eine konservative geschätzte Echo-Unterdrückung (ERLE).

## 0.3.4

- Neuer auswählbarer Empfangsweg `receive_mode: rtsp|baichuan` für Doorbell→Telefon.
- Neuer `baichuan_receive_stream: sub|main|extern`; `sub` ist der Default.
- Experimentelle Optionen `rtsp_transport` und `low_latency_rtsp` wieder entfernt, nachdem reale A/B-Messungen keinen relevanten Vorteil zeigten. RTSP bleibt stabil auf TCP.
- Baichuan-Preview über TCP/9000 mit Kanal-/Streamadressierung implementiert.
- Inkrementeller `bcmedia`-Parser für Reolink-Info-, H.264/H.265-, AAC- und ADPCM-Pakete integriert.
- Audio-only-Preview ergänzt: Video-Frames werden korrekt übersprungen, aber nicht in den Audiopfad kopiert.
- Baichuan-Audioempfang unterstützt natives IMA-ADPCM sowie AAC/ADTS über eine persistente FFmpeg-Decodierpipe.
- Neuer Baichuan→SIP-Livepfad mit 20-ms-PCMA/PCMU-RTP und hart auf 120 ms begrenztem Audio-FIFO.
- Akustischer Latenztest kann nun denselben Marker wahlweise über RTSP oder Baichuan empfangen und direkt vergleichen.
- Statusseite und Logs zeigen konfigurierten sowie aktiven Empfangsweg und Codecdetails.
- SIP-/RTSP-User-Agent auf 0.3.4 angehoben.
- Mock-/Regressionstests für Preview-Protokoll, binäre Medienpakete, Parser, AAC-Pipe und Baichuan→SIP-RTP ergänzt.
- Baichuan-Mediencode basiert teilweise auf der MIT-lizenzierten ReolinkProxy-Implementierung; Attribution in `THIRD-PARTY-NOTICES.md` erweitert.

## 0.3.3

### Robuster akustischer Latenztest

- Reale 0.3.2-Messungen ausgewertet: drei Läufe lagen mit `0.136..0.149` direkt um den bisherigen festen Korrelations-Schwellwert `0.14`; ein nominell erfolgreicher Lauf meldete 870 ms, war mit `0.149` aber nur knapp oberhalb der Grenze. Ein weiterer Lauf lieferte beim RTSP-Start überhaupt keine PCM-Samples. Der 870-ms-Wert wird deshalb nicht als belastbare Baseline übernommen.
- Der bisherige 384-ms-Sweep von 250..850 Hz wurde durch einen 1,024-s langen, codierten Sprachband-Marker ersetzt. 16 Symbole wählen deterministisch Frequenzen zwischen 850 und 2300 Hz; kurze Fades vermeiden harte Klicks. Der Bereich ist für den kleinen Doorbell-Lautsprecher und den Mikrofon/AAC-Pfad günstiger.
- Der Korrelator arbeitet nun mit 8-kHz-Zwischensignal statt 2 kHz, damit der vollständige 850..2300-Hz-Marker erhalten bleibt.
- Damit die höhere Korrelationsbandbreite nicht unnötig CPU kostet, wird zunächst auf einem 1-ms-Lag-Raster gesucht und nur um relevante Peaks samplegenau verfeinert; die Fensterenergie kommt aus einer Präfixsumme statt erneuter Vollberechnung.
- Neben dem besten Peak wird der zweitbeste **unabhängige** Peak außerhalb einer vollständigen Markerlänge um den Gewinner ermittelt. Ein Messwert gilt nur, wenn der beste Peak absolut ausreichend und gegenüber unabhängigen Kandidaten eindeutig ist.
- Unsichere Messungen werden als `ambiguous` statt zufällig als PASS/FAIL direkt an einer Schwellwertgrenze gemeldet. Log und Status enthalten besten/zweiten Peak, Margin, Verhältnis und den Delay-Kandidaten.
- Nach erfolgreicher Baichuan-Übertragung wird jetzt explizit `acoustic latency self-test marker transmitted` mit gesendeten/erwarteten ADPCM-Blöcken, Mediendauer und tatsächlicher Sendezeit protokolliert.
- Der RTSP-Capture-Start wird bei fehlendem/ungeeignet getaktetem PCM bis zu drei Mal kontrolliert wiederholt. Jeder Fehlversuch und der letztlich erfolgreiche Versuch werden protokolliert.
- Zusätzliche Regressionstests prüfen eindeutigen synthetischen Delay, einfache Speaker/Mic-Filterung, zwei konkurrierende Markerpeaks, Ambient-only-Ablehnung, Marker-Dauer/Pegel über mehrere Sampleraten sowie die bestehende TCP/UDP-Low-Latency-Argumentwahl. Ein Linux-Fake-FFmpeg-Test erzwingt zwei Startup-Fehler und bestätigt den erfolgreichen dritten Capture-Versuch.
- Normaler SIP-/Baichuan-Livepfad, Gain, Ducking und RTSP-Low-Latency bleiben unverändert und weiterhin separat auswählbar.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.3.3 angehoben.

## 0.3.2

### Echo-/Latenzdiagnose und optionale Audioqualität

- Reales 0.3.1-Full-Duplex-Gespräch bestätigt: beide Sprachrichtungen verständlich, sauberer BYE-Abbau und unauffällige SIP-RTP/FIFO-Statistiken. Im temporären VPN-/Auslandsaufbau wurden jedoch deutliches Echo und etwa zwei Sekunden Ende-zu-Ende-Verzögerung wahrgenommen.
- `latency_test` ergänzt: sendet im NVR-/Auto-Modus einen kurzen codierten Chirp über Baichuan und misst dessen akustische Rückkehr über Doorbell-Mikrofon, NVR-RTSP und FFmpeg per normalisierter Korrelation. SIP, PBX, VPN und Telefon sind aus dieser Messung ausgeschlossen.
- RTSP-Transport als `rtsp_transport: tcp|udp` auswählbar; TCP bleibt Default.
- `low_latency_rtsp` als opt-in ergänzt. Nur bei Aktivierung werden Probe-/Analyse-/Flush-Puffer reduziert; bei UDP wird zusätzlich die Reorder-Verzögerung minimiert. Der 0.3.1-Defaultpfad bleibt unverändert.
- `talkback_gain_enabled` + `talkback_gain_db` ergänzt: optionaler Pegel für Telefon→Doorbell vor Resampling/ADPCM.
- `soft_ducking_enabled` mit Absenkung, RMS-Sprachschwelle und Hold-Zeit ergänzt: Doorbell→Telefon wird während erkannter Telefon-Sprache vorübergehend abgesenkt. Standardmäßig aus; keine adaptive AEC.
- G.711 PCM-Encoding, dB-Gain und RMS-dBFS-Helfer ergänzt. Normaler Pass-through wird weiterhin nicht unnötig re-quantisiert.
- Live-Diagnose erweitert um maximale FIFO-Tiefe, RTP-Interarrival-Abweichungen, Baichuan-Block-/Write-Dauern, erste Kamera-RTP-Ankunft, Anzahl geduckter Pakete und FFmpeg-Timestamp-Korrekturen.
- Zusätzliche Tests für Gain/Ducking-Unabhängigkeit, Gain-Integration in den Baichuan-Puffer, Low-Latency-FFmpeg-Argumente sowie synthetische akustische Delay-Korrelation.
- App-Version sowie SIP-/RTSP-User-Agent auf 0.3.2 angehoben.

## 0.3.1

### Erster realer SIP-Call: FFmpeg-Kompatibilität

- Reales 0.3.0-Log bestätigt SIP-Registrierung, INVITE/200, PCMA und erfolgreichen Baichuan-FDX-Liveaufbau bis `call media active`.
- Abbruchursache eindeutig isoliert: das im Home-Assistant-App-Image laufende FFmpeg verwirft `-rw_timeout` beim RTSP-Liveeingang mit `Option rw_timeout not found.`.
- Live-FFmpeg und RTSP-Eingangs-Selbsttest verwenden nun die RTSP-Demuxer-Option `-timeout 10000000`.
- Regressionstest prüft, dass die Live-Argumentliste kein `-rw_timeout` mehr enthält.

### Early-Media-/RTP-Übergabe

- Beim realen FRITZ!Box-Call lag zwischen `183 Session Progress` und `200 OK` mehrere Sekunden Early-Media-Zeit. Weil der SIP-RTP-Port absichtlich schon vor INVITE gebunden wird, konnten diese Pakete bis zum Start der Baichuan-Bridge im Kernelpuffer liegen.
- Vor Start der Live-Medienworker wird die bereits vorhandene RTP-Warteschlange nun mit einem kurzen Idle-Fenster geleert. Frische RTP-Pakete nach der Übergabe werden normal verarbeitet.
- Der Drain besitzt ein hartes Paketlimit und setzt die Socket-Deadline garantiert zurück. Ein UDP-Regressionstest prüft Backlog-Entfernung und anschließende Wiederverwendbarkeit des Sockets.
- Die Anzahl verworfener Pre-Answer-Pakete wird bei Debug-Logging sichtbar.

### Wartung

- App-Version sowie SIP-/RTSP-User-Agent auf 0.3.1 angehoben.

## 0.3.0

### Live-Talkback über NVR/Baichuan

- Der in 0.2.3 am realen RLN8-410 hörbar bestätigte Baichuan-/Basic-Service-Pfad ist jetzt in den normalen SIP-Livebetrieb integriert.
- Neue benutzerorientierte Option `connection_mode: auto|nvr|standalone`.
  - `nvr`: Telefon→Doorbell über Baichuan/TCP 9000 und `baichuan_channel`.
  - `standalone`: Telefon→Doorbell über ONVIF-RTSP-Backchannel.
  - `auto`: versucht ONVIF-RTSP und fällt bei fehlender Unterstützung auf Baichuan zurück.
- Statusseite/API zeigen konfigurierten Modus, tatsächlich aktiven Rückkanal und dessen ausgehandeltes Profil. Gesprächsstatus wechselt nach erfolgreichem Medienaufbau auf `active`.

### Signalverarbeitung SIP → NVR

- G.711 PCMA/PCMU → PCM16 Decoder ergänzt.
- Streaming-Resampling von 8 kHz auf die dynamisch ausgehandelte Reolink-Talk-Samplerate.
- Begrenztes RTP-Reorder-Fenster für vertauschte Pakete; Duplikate/verspätete Pakete werden verworfen.
- Kleine RTP-Timestamp-Lücken werden mit Stille gefüllt; große Sprünge und SSRC-Wechsel setzen die Timeline kontrolliert zurück.
- Begrenzter FIFO/Jitter-Puffer mit Drop-oldest-Strategie verhindert unbegrenztes Latenzwachstum.
- ADPCM-Ausgabe wird exakt aus Reolink-Blockgröße/Samplerate getaktet; nach Stalls werden keine veralteten Blöcke burstweise nachgesendet.
- Unterläufe bzw. SIP-VAD/Silence-Suppression werden durch PCM-Stille überbrückt.
- IMA-ADPCM-Encoder bleibt über Blockgrenzen zustandsbehaftet.
- Baichuan-Schreibvorgänge erhalten kurze TCP-Write-Deadlines, damit Auflegen/Shutdown nicht an einem blockierten Socket hängt.

### Tests

- Neue G.711-Decodiervektoren.
- Tests für Streaming-Resampler, Reordering, Paketverlust, FIFO-Latenzgrenze und Duplicate-Drops.
- Mock-Livepfad `SIP RTP/G.711 → PCM → Resampling → ADPCM` gegen lokalen ADPCM-Writer einschließlich Silence-Fill/VAD-Unterlauf.
- Mock-Tests für die Rückkanalwahl `standalone`, `nvr` und den `auto`-Fallback.
- Symmetric-RTP-Härtung: die Zieladresse wird erst nach erfolgreicher RTP- und Payload-Type-Prüfung aktualisiert; malformed/RTCP-fremde UDP-Datagramme können den Rückkanal nicht umbiegen.
- Plausibilitätsgrenzen für dynamisch ausgehandelte Baichuan-Blockdauern verhindern pathologische Echtzeitprofile; das reale 16-kHz/1024-Sample-Profil liegt innerhalb der Grenzen.
- Bestehende Baichuan-Mock-NVR-, SIP-, RTSP-, HA- und Race-Tests bleiben erhalten.

### Wartung

- App-Version sowie SIP-/RTSP-User-Agent auf 0.3.0 angehoben.

## 0.2.3

### Reolink-Baichuan-/Basic-Service-Test

- `baichuan_test` ergänzt: unabhängiger Gegensprech-Selbsttest gegen Reolinks proprietären Basic Service über TCP, standardmäßig Port 9000.
- Der Test verwendet dieselbe `reolink_host`-/Benutzer-/Passwort-Konfiguration wie der bereits funktionierende NVR-RTSP-Pfad und adressiert den Zielkanal über `baichuan_channel` (0-basiert).
- Native Baichuan-Anmeldung mit Nonce, Reolink-MD5-Verfahren, BC-XOR und ausgehandelter AES-CFB-Verschlüsselung implementiert.
- Talk-Ability (Msg 10), Talk-Config (Msg 201), binärer Talk-Stream (Msg 202) und Talk-Reset (Msg 11) implementiert.
- Das vom Gerät angebotene ADPCM-Profil wird dynamisch ausgewählt; Samplerate, Sample-Precision, Duplex-Modus, Blockgröße und Stream-Modus werden protokolliert.
- 880-Hz-Testton wird nativ als IMA ADPCM codiert und in der vom Gerät ausgehandelten Blockgröße zeitlich passend übertragen. FFmpeg ist für diesen Test nicht erforderlich.
- Statusseite/API zeigen ein separates Baichuan-Testergebnis. `debug_baichuan` ergänzt Diagnose ohne Zugangsdaten.
- TCP-/Login-/Talk-/Binärdaten-Pfad wird gegen einen lokalen Mock-NVR einschließlich Verschlüsselungswechsel automatisiert getestet.
- Bei Wiederholungstests wurde ein Antwort/EOF-Race im neuen Baichuan-Client gefunden und behoben: schließt der NVR unmittelbar nach einer gültigen Antwort, wird eine bereits gepufferte Antwort nun gegenüber dem nachfolgenden EOF bevorzugt. Der Mock-NVR-Test lief danach 300-mal fehlerfrei.
- Adaptierte Baichuan-/ADPCM-Teile aus dem MIT-lizenzierten ReolinkProxy-Projekt sind in `THIRD-PARTY-NOTICES.md` attribuiert.

### Abgrenzung

- Version 0.2.3 nutzt Baichuan **noch nicht im normalen SIP-Gespräch**. Der neue Test soll zuerst auf der realen RLN8-410-/Doorbell-Kombination bestätigen, dass Port 9000 den Lautsprecher des gewählten NVR-Kanals tatsächlich erreicht. Nach erfolgreichem Praxistest kann dieser Pfad als produktive SIP→Reolink-Gegenrichtung integriert werden.

### Wartung

- App-Version auf 0.2.3 angehoben.

## 0.2.2

### Reolink-RTSP-Authentifizierung

- Reales NVR-Ergebnis aus 0.2.1 ausgewertet: RTSP-Eingangstest erfolgreich, ONVIF-Backchannel jedoch nach dem Digest-Challenge erneut `401 Unauthorized`.
- RTSP-Digest-Handshake toleranter gegen Embedded-Server gemacht: bis zu drei begrenzte Authentifizierungswiederholungen, einschließlich erneuerter/staler Nonces.
- Mehrere `WWW-Authenticate`-Header werden jetzt vollständig erhalten statt im Header-Map überschrieben zu werden.
- SHA-256-Digest gemäß aktuellem ONVIF/RFC-7616-Pfad ergänzt; MD5 bleibt unterstützt.
- Wenn der Server den `algorithm`-Parameter im Challenge auslässt, wird `algorithm=MD5` in der Authorization nicht mehr unnötig ergänzt. Das entspricht dem Default-Verhalten der Digest-Spezifikation und verbessert die Kompatibilität mit strikten RTSP-Implementierungen.
- `debug_rtsp` protokolliert Digest-Challenge-Metadaten (Realm, Algorithmus, qop, stale, Anzahl Challenges), ohne Nonce oder Zugangsdaten auszugeben.
- Zusätzliche Tests für wiederholte Authenticate-Header, implizites MD5, SHA-256 und einen `stale=true`-Nonce-Wechsel.

### Wartung

- App-Version und SIP-/RTSP-User-Agent auf 0.2.2 angehoben.

## 0.2.1

### Reolink-Diagnose

- `camera_test` ergänzt: führt beim Add-on-Start im Gateway-Container einen echten RTSP-Eingangstest mit `ffprobe` aus und prüft, ob Video **und** Audio erkannt werden.
- `backchannel_test` ergänzt: handelt den ONVIF-RTSP-`sendonly`-Audiotrack mit derselben RTSP-Implementierung wie der Livebetrieb aus und sendet einen hörbaren 3-Sekunden-Testton.
- Der Backchannel-Test verwendet den tatsächlich angebotenen PCMA-/PCMU-Codec und sendet exakt 160 G.711-Samples pro 20-ms-RTP-Frame.
- Beide Reolink-Tests funktionieren bewusst bei `dry_run: true`; dadurch ist keine SIP-/FRITZ!Box-Konfiguration erforderlich.
- Statusseite/API zeigen Ergebnis und Details beider Selbsttests.
- RTSP-/ffprobe-Fehlerausgaben werden vor dem Logging von RTSP-Userinfo und Passwortdarstellungen bereinigt.
- Testoptionen sind standardmäßig deaktiviert und laufen bei Aktivierung bei jedem Add-on-Start; Dokumentation weist auf das anschließende Abschalten hin.

### Wartung

- App-Version und SIP-/RTSP-User-Agent auf 0.2.1 angehoben.
- Konfigurationsvalidierung unterscheidet nun sauber zwischen normalem `dry_run`, Reolink-Selbsttests und Live-SIP-Betrieb.
- Bei den Wiederholungstests wurde ein RTSP-TEARDOWN-Race reproduziert: Schließt der RTSP-Server unmittelbar nach einer gültigen Antwort die TCP-Verbindung, konnte ein nachfolgendes EOF die bereits eingetroffene Antwort überholen. Der Client bevorzugt nun die bereits zugestellte Response und meldet keinen falschen TEARDOWN-Fehler mehr.

## 0.2.0

### Stabilität und Fehlerbehandlung

- Home-Assistant-Klingelerkennung von 500-ms-REST-Polling auf `subscribe_trigger` über WebSocket umgestellt; REST bleibt als automatischer Fallback mit Reconnect/Backoff erhalten.
- SIP-RTP- und lokaler FFmpeg-RTP-Port werden vor dem INVITE reserviert, damit ein Portkonflikt nicht erst nach dem Abheben auffällt.
- SIP-`200 OK` wird auch bei ungültigem SDP immer ACKed; der entstandene Dialog wird anschließend sauber per BYE beendet.
- Race-Fall „200 OK trifft nach Klingel-Timeout ein“ behoben: ACK + sofortiges BYE statt versehentlich verspätet ein Gespräch zu starten.
- CANCEL-Transaktion wartet auf die finale INVITE-Antwort und ACKt insbesondere `487 Request Terminated`.
- CSeq-Zugriff gegen paralleles Hangup/2xx-Retransmit synchronisiert.
- RTSP-Requests haben begrenzte Timeouts; Session-Timeout wird ausgewertet und per Keepalive verlängert.
- Sauberes ONVIF-RTSP-`TEARDOWN` beim Gesprächsende ergänzt.
- RTSP-`Content-Length` gegen negative und übermäßig große Werte abgesichert.
- Worker-Lifecycle der Medienbrücke überarbeitet, damit RTP, FFmpeg und RTSP bei jedem Abbruchpfad deterministisch beendet werden.

### Audio

- Eigener 20-ms-G.711-Repacketizer ergänzt. Dadurch werden variable FFmpeg-RTP-Paketgrößen nicht mehr direkt an das SIP-Endgerät weitergereicht.
- Auch der Reolink-Backchannel wird auf 160 G.711-Samples = 20 ms normalisiert.
- PCMA↔PCMU-Transcoding auf deterministische 256-Werte-Lookup-Tabellen umgestellt und bytegenau gegen FFmpeg gegengeprüft.
- Statische RTP-Payloadtypen 0/8 werden auch ohne `a=rtpmap` als PCMU/PCMA erkannt.

### Sicherheit und Konfiguration

- Strengere Host-, Entity-ID-, Port- und CR/LF-Validierung.
- SIP-/RTP-/RTCP-Portkollisionen werden beim Start erkannt.
- Raw-IPv6-SIP-Registrar wird mit verständlicher Meldung abgelehnt, da der SIP-Medienpfad derzeit IPv4/UDP verwendet.
- FFmpeg-Fehlerausgaben entfernen vollständige RTSP-Credentials einschließlich percent-encoded Userinfo.
- Ingress-Status/API nur über Loopback bzw. Home-Assistant-Ingress; `/health` enthält keine sensitiven Daten.
- `dry_run` ist jetzt standardmäßig aktiviert und benötigt keine Kamera-/SIP-Passwörter.
- Home-Assistant-App-Metadaten auf Version 0.2.0 aktualisiert.

## 0.1.0

- Erster experimenteller Prototyp.
- Home-Assistant-Binärsensor als Klingelquelle.
- Direkte SIP-Registrierung und ausgehender SIP-Anruf über UDP.
- SIP-Digest, PCMA/PCMU, Reolink-RTSP und ONVIF-Audio-Backchannel.
- FFmpeg-basierte Dekodierung des Kameratons.
- Ingress-Statusseite und Supervisor-Watchdog.
