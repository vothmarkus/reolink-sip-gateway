# Prüfprotokoll 1.3.1

## Ziel

1.3.0 stellt der FRITZ!Box ein aktuelles Reolink-JPEG unter einer stabilen geheimen `.jpg`-Adresse bereit. 1.3.1 verschiebt die bestehende Gruppe **FRITZ!Fon-Livebild** an die vorletzte Position unmittelbar vor **Betrieb & Diagnose**. Zu prüfen sind die identische öffentliche Gruppenreihenfolge, CGI- und RTSP-Fallback, JPEG-Validierung und -Skalierung, Tokenpersistenz, die lokale HTTP-Grenze, die Anzeige auf FRITZ!Fon sowie die vollständige Regression des unveränderten 1.2.2-Routing-, SIP- und Audiopfads.

## Softwareprüfungen vor Release

- `gofmt -l cmd internal`
- `go vet ./...`
- `go test ./...`
- `go test -shuffle=on ./...`
- `go test -race -p 1 ./...` (Paketserialisierung schützt den Echtzeit-Pacing-Test vor fremder Race-Build-Last; die Race-Erkennung selbst bleibt vollständig aktiv.)
- wiederholte Media/AEC-/Kalibrierungstests
- statischer amd64-Go-Releasebuild
- UI-Adaptertest: gruppierte `testdata/options.valid.json` → flache `testdata/options.runtime.valid.json`; anschließend `-check-config` gegen die Runtime-Datei
- YAML-/JSON-Prüfung von App-Konfiguration und Übersetzungen
- identische sechs Gruppen sowie `call_routes` unmittelbar hinter `call` in `options`, `schema`, DE, EN und Testkonfiguration; `live_image` steht jeweils als vorletzte Gruppe direkt vor `diagnostics`
- identische SIP-Feldreihenfolge in `options`, `schema`, DE, EN und Fixture: beide Konto-Blöcke vor `sip_registrar_port`, `sip_local_port` und `parallel_local_port`
- Bash-Syntaxprüfung des s6-Startskripts
- Versionsprüfung 1.3.1 in App, Gateway, SIP-/RTSP-/Snapshot-User-Agent und CI-Buildargument
- Prüfung, dass alle 0.4.x-Retired-Options aus dem öffentlichen Schema entfernt sind
- expliziter Test der nativen Statistikbits 0…7

## Ergänzungen 1.3.1

- Die öffentliche Reihenfolge lautet durchgängig `reolink`, `sip`, `audio`, `call`, `call_routes`, `live_image`, `diagnostics`.
- Die vorhandene Livebildoption wird nur sichtbar verschoben. Adapter, flacher Runtimewert, Standard, gespeicherte Optionen und Token bleiben unverändert.
- Ein expliziter Reihenfolgetest prüft `options`, `schema`, DE, EN und die öffentliche JSON-Testkonfiguration.

## Ergänzungen 1.3.0

- Der neue Optionsblock `live_image` wird bei fehlendem Wert sicher mit `fritzfon_live_image_enabled: true` materialisiert und als flacher boolescher Runtimewert übergeben. Abschalten verhindert die Registrierung des Endpunkts.
- Ein Mock-Reolink-Server prüft `cmd=Snap`, den internen Kanal, Benutzer, Passwort, Zufallswert sowie Reolinks dokumentierte 640×480-Anforderung. Ein 1280×960-JPEG wird seitenverhältnistreu auf 480×360 in den AVM-Rahmen eingepasst; kleinere Bilder bleiben korrekt.
- Eine erfolgreiche Aufnahme wird 750 ms zwischengespeichert und jedem Aufrufer als unabhängige Bytekopie geliefert. Eine ungültige JSON-CGI-Antwort führt zum injizierten RTSP-Fallback.
- Verbindungs-, HTTP- und FFmpeg-Fehler dürfen das konfigurierte Reolink-Passwort nicht enthalten. CGI-Redirects zu einem anderen Host werden abgelehnt.
- Der geheime HTTP-Pfad akzeptiert nur lokale/private/link-lokale `GET`- und `HEAD`-Anfragen, liefert `image/jpeg`, eine feste `.jpg`-Disposition und No-Cache-/Nosniff-Header. Öffentliche Quelladressen und falsche Pfade bleiben unerreichbar; Providerfehler ergeben eine generische `502`-Antwort.
- API-Token und separates 192-Bit-Livebildtoken bleiben über erneutes Laden stabil und liegen jeweils mit Modus `0600` vor. Das Livebildtoken erfüllt bewusst nicht die Validierung des 256-Bit-API-Tokens.
- Die Statusseite zeigt Protokollauswahl-Anweisung, kopierbare Adresse und Browsertest nur bei aktivierter Funktion. Der Snapshot meldet den Aktivierungszustand additiv; API v1 bleibt ansonsten unverändert.

## FRITZ!Box-4050-/FRITZ!Fon-Hardwaretest 1.3.1

1. App aktualisieren, starten und kontrollieren, dass im Ingress-Abschnitt **FRITZ!Fon-Livebild** eine lokale Adresse mit Port `18099` und Endung `.jpg` erscheint. Sie darf weder Reolink-Benutzer noch Passwort enthalten.
2. **Aktuelles Kamerabild testen** öffnen. Der Browser muss ein aktuelles Bild des konfigurierten physischen NVR-Kanals anzeigen; wiederholte Aufrufe müssen neue Frames liefern.
3. In der FRITZ!Box unter **Telefonie → Telefoniegeräte** die IP-Türsprechanlage bearbeiten, beim Livebild `http://` auswählen und den angezeigten Wert ohne Protokollpräfix einfügen.
4. Einen realen Türruf auslösen. Das zugeordnete FRITZ!Fon muss das Kamerabild anzeigen; Wiederholen beziehungsweise Aktualisieren darf kein altes FRITZ!Box- oder HTTP-Cachebild festhalten.
5. Falls praktikabel, Reolink-CGI vorübergehend sperren oder auf eine ungültige Antwort umleiten und verifizieren, dass der RTSP-/FFmpeg-Fallback weiterhin ein Bild liefert. Danach die normale Konfiguration wiederherstellen.
6. Einen einzelnen Buchstaben im geheimen Pfad ändern. Dieser Pfad darf kein Bild liefern. Das korrekte Token muss nach App-Neustart unverändert sein.
7. `fritzfon_live_image_enabled` abschalten und neu starten. Die Statusseite muss **deaktiviert** melden und der zuvor gültige Bildpfad darf nicht mehr registriert sein. Anschließend wieder aktivieren.
8. Die bestehenden Tür-/Mobilrouten, eingehenden Anrufe, DTMF und Zwei-Wege-Audio stichprobenartig prüfen. Der Bildabruf darf keinen Call-Slot belegen und keine Audiokalibrierung auslösen.

## Ergänzungen 1.2.2

- Eine Neuinstallation enthält exakt die editierbare Route `default` mit Name `Standardroute`, Besucher-Sensor `auto`, Klingeltaster `11` und drei leeren Mobilnummern.
- Eine 1.1-Konfiguration wird einmalig in diese Route überführt; Besucher-Sensor, Türziel und bis zu drei Mobilnummern bleiben erhalten. 1.2.0/1.2.1-Routen behalten ID, Name, Sensor und Klingeltaster; Katalogreferenzen und bereits direkt eingetragene Werte werden zu direkten Nummern.
- Der Migrationsschreibvorgang erfolgt nur nach Vergleich mit den unveränderten Quelloptionen. Nach dem neuen persistenten Marker bleiben normale Starts read-only.
- Routen-IDs werden getrimmt und streng validiert. Doppelte IDs, Besucher-Sensoren, Klingeltaster-Nummern, normalisierte Rufnummern innerhalb derselben Route oder leere Rufwege schlagen beim Start fehl. Dieselbe Mobilnummer in unterschiedlichen Routen ist erlaubt.
- Eine Route kann Tür-only, Mobil-only oder kombiniert sein. Die Validierung berücksichtigt die beiden Konto-Schalter und verlangt für jede Live-Route mindestens einen tatsächlich aktivierten Rufweg.
- Alle Besucher-Sensoren werden in einer Home-Assistant-WebSocket-Subscription überwacht. Der REST-Fallback hält je Entity einen eigenen Flankenzustand; Entprellung erfolgt je Route, während der globale Call-Controller weiterhin nur einen Gewinner zulässt.
- API v1 liefert einen Routenkatalog ohne Telefonnummern, aktuelle/letzte Route und `POST /api/v1/routes/{route_id}/test`. Der Alt-Endpunkt startet kompatibel die erste aufgelöste Route; unbekannte IDs ergeben `404 route_not_found`.
- Status und Testanruf-Verfügbarkeit werden je Route aus den tatsächlich benötigten und registrierten SIP-Konten berechnet. Ein Tür-only-Test benötigt keine Mobilregistrierung und umgekehrt.
- Kein Routenmodell enthält Kamera-, NVR-Kanal-, Medien-, DTMF- oder Türöffnerauswahl. Genau eine Reolink-Konfiguration und Mediensitzung bleibt die globale Ressource.

## FRITZ!Box-4050-Hardwaretest 1.2.2

1. Zwei Klingeltaster-Ziele in der als IP-Türsprechanlage eingerichteten FRITZ!Box 4050 prüfen, beispielsweise `11` für Klingeltaster 1 und `12` für Klingeltaster 2. Die FRITZ!Box 6690 bleibt nur Modem.
2. Zwei unterschiedliche Besucher-Entities als Routen anlegen und beiden unterschiedliche `doorbell_number`-Werte geben. Jede reale Klingeltaste muss exakt die erwartete FRITZ!Fon-Türdarstellung auslösen.
3. Bis zu drei Mobilrufnummern direkt je Route eintragen und unterschiedlich auf beide Routen verteilen. Bei jeder Taste dürfen nur deren zugeordnete Mobiltelefone klingeln; dieselbe Nummer muss in mehreren Routen funktionieren.
4. In der Companion-Integration muss je Route genau ein Testanruf-Button mit dem Routennamen erscheinen. Beide Buttons einzeln auslösen und die Zuordnung zu Türziel und Mobiltelefonen prüfen.
5. Eine Tür-only- und eine Mobil-only-Route testen. Der jeweilige Testanruf muss auch dann verfügbar sein, wenn das für diese Route nicht benötigte Konto deaktiviert ist.
6. Zwei Routen nahezu gleichzeitig auslösen. Nur die erste darf einen Rufaufbau und die eine Reolink-Mediensitzung erhalten; die zweite darf weder parallel starten noch nach Gesprächsende nachgeholt werden.
7. Dieselbe Route innerhalb der Entprellzeit erneut, danach die andere Route auslösen. Nur die Wiederholung derselben Route wird entprellt; die andere erreicht den globalen Busy-Entscheid und bleibt ebenfalls unqueued.
8. Je einen Tür- und Mobilzweig zuerst annehmen und zusätzlich einen Fast-Simultaneous-Answer-Test durchführen. CANCEL beziehungsweise ACK+BYE müssen alle Verlierer sauber beenden.
9. Eingehende Anrufe an beide Konten und DTMF in beiden Richtungen prüfen. Das Verhalten muss gegenüber 1.1.1 identisch sein; Route darf höchstens als zusätzliche Diagnose erscheinen.
10. Eine Kopie alter 1.1-Optionen sowie eine 1.2.1-Konfiguration mit Mobilzielkatalog aktualisieren. Nach genau einem Start müssen die migrierten Routen editierbar vorliegen und Registrierung, Testanrufe sowie Besuchertrigger unverändert funktionieren.

## Ergänzungen 1.1.1

- `door_call_enabled` steht direkt nach dem Registrar in `options`, `schema`, DE, EN und Fixtures. Fresh Install und Update ergeben `true`, sodass bestehender Türbetrieb unverändert bleibt.
- Live-Konfigurationen mit nur Türkonto, nur Mobilkonto oder beiden Konten sind gültig. Sind beide Schalter aus, schlägt die Validierung klar fehl. Zugangsdaten und Ziel werden nur für das jeweils aktivierte Konto verlangt; gleiche lokale Ports sind bei nur einem aktiven Konto zulässig.
- Beide Konten übernehmen `incoming_calls_enabled` und dieselbe Anruferliste. Eingehende INVITEs beider lokalen Ports erreichen denselben globalen Controller: Der erste Anruf gewinnt, ein weiterer erhält `486 Busy Here`.
- Ausgehandeltes RFC-4733-DTMF wird bei ein- und ausgehenden Gesprächen beider Konten identisch als API-Ereignis ausgegeben.
- API v1 meldet Aktivierungs- und Registrierungsstatus beider Konten. Das kompatible Feld `sip.registered` ist wahr, sobald mindestens ein aktiviertes Konto registriert ist; damit sind Bereitschaft und Testanruf auch im Mobil-only-Betrieb korrekt.
- Die deutsche UI beschreibt das erste Konto ausdrücklich als FRITZ!Box-IP-Türsprechanlage; ab 1.2.2 erklärt die Route `doorbell_number`, beispielsweise `11` als typischen Klingeltaster 1.

## Ergänzungen 1.1.0

- Fresh Install und Update ergeben `parallel_call_enabled: false`, leere Ziele und lokalen Port 5071. Alle fünf neuen SIP-Felder stehen in identischer Reihenfolge in `options`, `schema`, DE, EN und Fixture und erreichen den flachen Runtime-Snapshot unverändert.
- Aktivierung verlangt in Live-Betrieb Benutzername, Passwort, ein bis drei Ziele und einen vom Tür-Konto verschiedenen lokalen Port. Ziellisten werden getrimmt/dedupliziert; vier Ziele, CR/LF und überlange Einträge werden abgewiesen.
- Das Tür-Konto bleibt auf einen Dialog beschränkt. Ein Mobilkonto mit Kapazität drei kann drei parallele ausgehende Dialoge mit unterschiedlichen `Call-ID`/Transaktionen halten; ein vierter Wählversuch wird abgewiesen.
- Ein schneller Gewinner cancelt alle noch klingelnden Zweige. Der Test modelliert zusätzlich einen `200 OK`, der die Entscheidung kreuzt: Gewinner bleibt aktiv, der verspätete Verlierer erhält genau ein `BYE`.
- Bestehende SIP-Integrationstests belegen weiterhin ACK auf endgültige Nicht-2xx-Antworten nach `CANCEL`, ACK+BYE bei verspätetem 200, 2xx-Wiederholung, eingehende INVITE/ACK/CANCEL/BYE und den Einzel-Call-Schutz des Tür-Kontos.
- Jeder Wählzweig reserviert einen eigenen dynamischen RTP-Socket. Nur der Gewinner wird an `media.Session` übergeben; loser sockets werden nach Fehler/CANCEL/BYE geschlossen. Der globale Call-Slot bleibt bis zum begrenzten Loser-Cleanup reserviert.
- API v1 meldet `parallel_calls`, Aktivierungszustand und zweite Registrierung additiv. Alte Felder und DTMF-Ereignisse bleiben unverändert.

## FRITZ!Box-4050-Regressionsprüfung 1.1.1

1. FRITZ!Box 4050 als Registrar/Telefonanlage verwenden; FRITZ!Box 6690 bleibt reines Modem.
2. Tür-Konto als IP-Türsprechanlage mit passendem Klingeltaster-Ziel anlegen, beispielsweise `11` für Klingeltaster 1; Mobilkonto als normales LAN/WLAN-IP-Telefon anlegen. Lokale Gateway-Ports 5070/5071, Registrarziel jeweils 4050:5060.
3. Dem Mobilkonto die gewünschte ausgehende Festnetznummer zuweisen und zunächst genau ein Mobilziel konfigurieren. Beide Registrierungen müssen im Gatewaystatus `true` zeigen.
4. Klingeln: FRITZ!Fon zeigt weiterhin Türruf/Livebild/Öffnen; das Handy erhält einen normalen externen Anruf. Je einmal zuerst FRITZ!Fon und zuerst Handy annehmen. Der Verlierer muss sofort aufhören zu klingeln und nur der Gewinner Audio erhalten.
5. Zwei, danach drei Mobilziele aktivieren. Alle Ziele müssen parallel klingeln; der erste angenommene Zweig gewinnt. Im Debuglog dürfen keine zweiten Reolink-Mediensitzungen und keine aktiven Restdialoge erscheinen.
6. Race provozieren, indem zwei Teilnehmer nahezu gleichzeitig annehmen. Der Verlierer muss nach ACK/BYE sauber beendet werden; kein Telefon darf verbunden bleiben oder später Audio erhalten.
7. Prüfen, wie viele externe Gespräche Anschluss/Provider tatsächlich gleichzeitig erlauben. Eine FRITZ!Box- oder Provider-Ablehnung eines einzelnen Zweigs darf einen anderen erfolgreich angenommenen Zweig nicht verhindern.
8. Während des Klingelns und während des Gesprächs die Integrationsaktion Auflegen auslösen. Sämtliche Rufzweige beziehungsweise der Gewinner müssen deterministisch beendet werden.
9. Tür-Konto deaktivieren und dessen Zugangsdaten/Ziel leeren. Mit ein, zwei und drei Mobilzielen prüfen, dass Registrierung, Status, Besuchertrigger und API-Testanruf ohne Tür-Konto funktionieren.
10. `incoming_calls_enabled` aktivieren und die internen Nummern beider Konten einzeln anrufen. Beide müssen bei freiem Gateway Audio aufbauen. Während eines angenommenen oder noch vorbereiteten Anrufs das andere Konto anrufen; dieser zweite Ruf muss `486 Busy Here` erhalten.
11. In einem eingehenden und einem ausgehenden Gespräch jedes Kontos DTMF senden. Die Companion-Integration muss für jede Taste genau ein Ereignis mit richtiger Richtung, Gegenstelle und `call_id` erhalten.

## Ergänzungen 1.0.0

- Ausgehendes SDP bietet `telephone-event/8000` auf PT 101 an; nur eine passende Antwort aktiviert DTMF. Eingehendes SDP spiegelt einen gültigen dynamischen 8-kHz-Payloadtyp.
- Terminale RFC-4733-Pakete für `0`–`9`, `*`, `#`, `A`–`D` ergeben genau ein Ereignis; Startpakete, Wiederholungen, reservierte Bits, Null-Dauer, unbekannte Codes und falsche Clockrate werden nicht veröffentlicht.
- Beide Talkback-Pfade trennen DTMF vor G.711 ab. Nur Pakete vom ausgehandelten RTP-Port sind zulässig; DTMF darf weder symmetrisches RTP retargeten noch den Audio-Watchdog zurücksetzen.
- SSE `dtmf` besitzt keine ID und ändert keine Statusrevision. Nutzdaten enthalten Dauer, Richtung, normalisierte Gegenstelle, SIP-Call-ID, Zeit und Instanz-ID. Eingehend wird der Anrufer, ausgehend das konfigurierte Ziel gemeldet; Ziffern dürfen nicht im Gatewaylog erscheinen.
- OpenAPI und `info.capabilities` enthalten `DTMFEvent` beziehungsweise `dtmf_events`. Es gibt weiterhin keine neue App-Option.

## Ergänzungen 0.9.0

- Identitätstest erzeugt UUID und 256-Bit-Token einmalig, prüft stabile Wiederverwendung und Dateirechte `0600`; manipulierte ungültige Werte müssen fail-closed zum Fehler führen.
- Alle `/api/v1`-Routen verlangen einen gültigen Bearer-Header. Fehlender/falscher Token ergibt `401`, öffentliche Quelladressen ergeben auch mit Token `403`; interne Fehlertexte dürfen keine Details oder Secrets spiegeln.
- `info` liefert exakt API-Version 1, Gateway-Version, UUID und die Fähigkeiten `call_status`, `caller_number`, `events`, `hangup`, `test_call`.
- `status` bildet aktuelle und letzte Call-Daten getrennt ab. Ein aktiver eingehender Call setzt Richtung, aktuelle/letzte Nummer, Codec und Auflegen-Verfügbarkeit; Testanruf ist währenddessen nicht verfügbar.
- Store-Revision und `updated_at` ändern sich nur bei einer realen Feldänderung. SSE liefert sofort den vollständigen aktuellen Snapshot und danach jeweils den neuesten vollständigen Snapshot; langsame Abonnenten blockieren den Gatewaypfad nicht.
- Testanruf ergibt `202`, `409` bei belegtem Call-Slot und `503` vor Runtimebereitschaft beziehungsweise ohne SIP-Registrierung. Auflegen ergibt `202`, im Leerlauf idempotent `204`.
- Controller-Test belegt: nur ein Runner gleichzeitig, Cancel erreicht den Call-Kontext, `ending` wird vor Cancel veröffentlicht und der Slot wird erst nach vollständigem Runner-Ende freigegeben.
- Besuchertrigger, eingehender Anruf und API-Testanruf laufen durch denselben Controller. Ein API-Auflegen während des ausgehenden INVITE muss `CANCEL` beziehungsweise nach Dialogaufbau `BYE` auslösen; bei eingehender Vorbereitung folgt eine kontrollierte Ablehnung, bei aktivem Dialog `BYE`.
- OpenAPI-Datei gegen einen 3.1-Parser validieren und Antwort-Fixtures der Companion-Integration gegen dieselben Schemas testen.
- Keine neue Option: `options`, `schema`, DE, EN und Runtime-Fixtures bleiben gegenüber 0.8 unverändert. Alle 0.8-Whitelist-/Hinweiston-/Watchdog-, 0.7-UAS-, 0.6-Elastic- und AEC-Regressionen bleiben grün.

## Ergänzungen 0.8.0

- Exakte UI-Reihenfolge in `options`, `schema`, DE, EN und Fixture: Besucher-Sensor, eingehende Anrufe, erlaubte Anrufer, Hinweiston, Entprellung, Klingeldauer, RTP-Verbindungswächter, maximale Gesprächsdauer.
- Fresh-Install-/Upgrade-Normalisierung ergibt `incoming_allowed_callers: ["*"]`, `incoming_connection_tone_enabled: true` und `rtp_inactivity_timeout_seconds: 15`; explizite Werte erreichen den flachen Runtime-Snapshot unverändert.
- Aktivierte eingehende Anrufe mit leerer Liste sowie `*` zusammen mit weiteren Einträgen werden als Konfigurationsfehler abgewiesen.
- Caller-ID-Normalisierung deckt SIP-/TEL-URI, Displayname, Prozentkodierung, Leerzeichen, Bindestriche, Punkte, Schrägstriche und Klammern ab. Es gibt weder Rufnummernsuffix-Matching noch automatische Landesvorwahlumrechnung.
- Ein gültiges Registrar-`INVITE` außerhalb der Whitelist erhält `403 Forbidden` und erreicht weder Anwendungs-Call-Queue noch SDP-/Medienaufbau.
- Der 256-ms-Hinweis ist samplegenau der Anfang des unveränderten 1,024-s-Kalibrierungsmarkers. Beide getesteten Raten 8/16 kHz enden mit einer ausgeblendeten Nullkante.
- Der Hinweiston läuft nur bei eingehenden Calls, nur bei aktivierter Option und über den bereits ausgehandelten Reolink-Talkback vor `media.Session.Ready()`/`200 OK`. Der AEC-Renderabgriff liegt weiterhin am tatsächlichen RTSP-/ADPCM-Write.
- RTP-Watchdog startet mit dem Medienempfang, wird ausschließlich durch gültige Pakete des ausgehandelten Payloadtyps zurückgesetzt und liefert bei Ablauf den erkennbaren Fehler `ErrRTPInactivity`.
- Watchdog-Ablauf beendet `media.Session`; die Anrufsteuerung versucht anschließend ein lokales SIP-`BYE`. Maximaldauer, entfernter `BYE` und normaler Kontextabbruch bleiben unverändert.
- Alle v0.7-SIP-UAS-, AEC-, Kamera-Smoother- und v0.6-Elastic-Regressionsprüfungen bleiben grün. 0.8 enthält keine Home-Assistant-Entitäten und kein DTMF.

## Ergänzungen 0.7.0

- `incoming_calls_enabled` steht in `options`, `schema`, DE, EN und Testkonfiguration direkt nach `visitor_entity`; Entprellung, Klingeldauer und maximale Gesprächsdauer folgen in dieser Reihenfolge.
- Fresh-Install- und Upgrade-Normalisierung ergeben `incoming_calls_enabled: false`; ein explizites `true` erreicht den flachen Runtime-Snapshot unverändert.
- Bei deaktivierter Option wird ein `INVITE` mit `403 Forbidden` abgewiesen. Absender-IP und UDP-Port müssen exakt dem aufgelösten konfigurierten Registrar entsprechen.
- Ein gültiges SDP-Angebot mit PCMA und PCMU berücksichtigt die konfigurierte Präferenz; ein Angebot ohne unterstütztes G.711-Audio erhält `488 Not Acceptable Here`.
- Ein angenommener Dialog liefert zunächst `100 Trying`, danach ein getaggtes `200 OK` mit dynamischem lokalem RTP-Port und genau dem ausgewählten Codec.
- `ACK` stoppt die kontrollierte Wiederholung des `200 OK`; `BYE` erhält `200 OK` und beendet den Call. Ein ausbleibendes `ACK` beendet den Medienweg kontrolliert.
- `CANCEL` vor der Annahme erhält `200 OK`; das ursprüngliche `INVITE` endet mit `487 Request Terminated` und kann danach nicht mehr angenommen werden.
- Ein zweites `INVITE` während eines reservierten oder aktiven Dialogs erhält `486 Busy Here`. Ausgehender Wählvorgang und eingehender Dialog reservieren denselben SIP-Call-Slot race-frei.
- Die Anwendung wartet vor `200 OK` auf `media.Session.Ready()`. Fehler beim Reolink-Aufbau führen zu `480 Temporarily Unavailable`, nicht zu einem angenommenen stummen Gespräch.
- Eingehende und ausgehende Calls verwenden dieselbe `media.Session`; AEC, Startup-Kalibrierung, Kamera-PLL und elastischer v0.6-Talkback bleiben ohne Parallelimplementierung erhalten.
- DTMF, Mehrkameraauswahl, PIN und Türöffner sind nicht Teil des 0.7.0-Produktionspfads.

## Ergänzungen 0.6.0

- Exakt fälliger FIFO-Block bleibt samplegenau und verbraucht weder mehr noch weniger Samples.
- Kleine Unterdeckung wird ohne Stille auf volle Blocklänge gedehnt; das Verhältnis bleibt bei mindestens 0,98.
- Größere Unterdeckung nutzt höchstens 2 % Dehnung, weist die verbleibende Stille separat aus und endet über eine 5-ms-Half-Hann-Kante exakt bei null.
- Vollständiger Leerlauf nach Nutzsignal erzeugt ausschließlich einen kausalen, höchstens 5 ms langen abklingenden Rand und anschließend null.
- Wiederkehrendes Signal wird über 5 ms eingeblendet; der Playout wartet dafür nicht auf künftige Samples.
- Hochwasser wird mit höchstens 3 % Stauchung abgebaut. Ein negativer Zulauftrend löst eine begrenzte vorbeugende Dehnung aus; die dabei erhaltene Reserve wird nach normalisiertem Zulauf wieder abgebaut.
- Drop-oldest-Überlauf plant genau einen kausalen 5-ms-Grenz-Splice ein, ohne Blocklänge, Verbrauchszeitpunkt oder Schreibkadenz zu verändern.
- Rohfehlmenge (`fifo_raw_shortage_samples`) und nach Korrektur verbleibende Stille (`fifo_underrun_samples`) werden getrennt gezählt; FIFO-Tiefe, Verhältnisse, Korrekturen und Übergänge sind im Abschlusslog sichtbar.
- Reolink-FIFO bleibt auf vier Blöcke begrenzt. Es gibt keine neue Konfigurationsoption und keinen zusätzlichen Vorpuffer oder Lookahead.
- ADPCM wird weiterhin vor dem AEC-Referenzabgriff erzeugt; `ObserveBaichuanPlayout` bleibt unmittelbar nach dem tatsächlichen Write. Kamera-PLL, Startkalibrierung, fester Coarse-Delay, AEC3 und deaktivierter Live-Tracker bleiben unverändert.

## Ergänzungen 0.5.14

- Exakte UI-Reihenfolge ist in `options`, `schema`, DE, EN und `options.valid.json` identisch: SIP-Ports am Blockende, Entprellung direkt nach Visitor, Passivmodus vor Log-Level.
- Sichtbare Namen lauten `Betrieb & Diagnose` / `Operation & diagnostics`; beide SIP-Portfelder tragen den Zusatz `(erweitert)` / `(advanced)`. Interner Gruppenpfad bleibt `diagnostics`.
- Semantischer Vergleich gegen 0.5.13 bestätigt identische Optionswerte, Defaults, Schematypen, Gruppenpfade und Runtime-Abbildung.
- Sämtliche Visitor-/WebSocket-, Audio-/AEC-, Kalibrierungs-, Baichuan-, Media-, RTP-, Startup-, Config- und Migrationstests bleiben unverändert grün.
- SIP und RTSP unterscheiden sich von 0.5.13 ausschließlich durch den User-Agent-Versionsstring.

## Ergänzungen 0.5.13

- Entity-Registry-Resolver fordert ausschließlich `config/entity_registry/list_for_display` an und dekodiert die kompakten Felder `ei`, `pl` und `tk` aus `result.entities`.
- Genau ein aktivierter Reolink-Visitor wird ausgewählt; null oder mehrere Treffer schlagen weiterhin kontrolliert fehl.
- Eine einzelne Registry-Antwort von mehr als 3 MiB wird vollständig gelesen und korrekt ausgewertet.
- Ein angekündigter Frame von mehr als 16 MiB wird vor der Payload-Allokation mit `websocket frame too large` abgewiesen.
- Manueller `visitor_entity` und bestehende 0.5.12-Konfigurationen bleiben unverändert.
- Audio-/AEC-/Kalibrierungs-/Baichuan-/Media-/RTP-/Startup-/Config-Dateien sowie `native/` und `Dockerfile` bleiben gegenüber 0.5.12 bytegleich. SIP und RTSP ändern ausschließlich den User-Agent-Versionsstring.

## Ergänzungen 0.5.12

- Fresh-Install-Normalisierung ergibt `visitor_entity: auto`.
- Entity-Registry-Resolver authentifiziert sich über den vorhandenen Supervisor-/Home-Assistant-WebSocket und fordert `config/entity_registry/list` an.
- Genau ein aktivierter Eintrag `binary_sensor.*` mit `platform=reolink` und `translation_key=visitor` wird ausgewählt.
- Nicht-Reolink-, Nicht-Visitor- und deaktivierte Einträge werden nicht ausgewählt.
- Bei mehreren aktivierten Visitor-Sensoren muss der Resolver kontrolliert fehlschlagen und alle Kandidaten nennen; es darf nicht geraten werden.
- Ein manueller `visitor_entity`-Wert überspringt den Resolver vollständig und bleibt beim Upgrade aus 0.5.11 bestehen.
- UI-Übersetzungen zeigen `Passivmodus` / `Passive mode`; der öffentliche Schlüssel bleibt aus Kompatibilitätsgründen `dry_run`.
- AEC-/Kalibrierungs-/Baichuan-/Media-/RTP-/Startup-/Config-Dateien bleiben gegenüber 0.5.11 bytegleich. SIP und RTSP ändern ausschließlich den User-Agent-Versionsstring.

## Ergänzungen 0.5.11

- Fresh-Install-Normalisierung ergibt `reolink_username: admin` und `sip_registrar: auto`.
- Synthetische `/proc/net/route`-Tabelle mit mehreren Default-Routen: `auto` wählt die aktive Gateway-Route mit der niedrigsten Metrik und dekodiert das Little-Endian-IPv4-Gateway korrekt.
- Manueller SIP-Registrar (DNS/IP) bleibt unverändert und benötigt keine Gateway-Erkennung.
- `auto` ohne nutzbares IPv4-Gateway muss kontrolliert fehlschlagen.
- Bestehende gruppierte Werte aus 0.5.10 dürfen durch die neuen Defaults nicht überschrieben werden.
- AEC-/Kalibrierungs-/Baichuan-/Media-/RTP-Dateien bleiben gegenüber 0.5.10 bytegleich; `internal/config/config.go` ändert ausschließlich den Reolink-Benutzer-Default auf `admin`.

## Finalisierung 0.5.10

- Frische bzw. bereits gruppierte 0.5.8/0.5.9-Konfiguration: Migrationsmarker wird ohne Supervisor-Write gesetzt.
- Direktupgrade mit alten flachen Werten und gleichzeitig materialisierten Gruppen-Defaults: solange der Marker fehlt, gewinnen die Legacy-Werte genau einmal.
- Nach gesetztem Marker: gruppierte Werte gewinnen selbst dann, wenn testweise widersprüchliche flache Altwerte vorhanden sind.
- Nach gesetztem Marker: normaler Start führt keinen `bashio::app.options`-/`bashio::addon.options`-Write aus.
- Einmaliger Migrations-Write bleibt synchron und compare-before-write-geschützt.
- `icon.png` ist 128×128 RGBA; `logo.png` und `internal/status/logo.png` sind RGBA, besitzen transparente Außenpixel und behalten deckende weiße Logo-Elemente.
- Go-Modul-/Importpfad ist konsistent `github.com/vothmarkus/reolink-sip-gateway`.
- Gegen 0.5.9 bleiben die Audio-/AEC-/Baichuan-/Kalibrierungsdateien inhaltlich unverändert, abgesehen von der mechanischen Modulpfad-Umschreibung in Go-Imports.

## Persistenz-Race 0.5.10

- Optionsmigration läuft synchron; kein `cleanup_public_options ... &` im Produktionsskript.
- Vor dem Supervisor-Write wird `/data/options.json` erneut gelesen. Nur wenn der kanonische Inhalt noch exakt dem ursprünglichen Snapshot entspricht, darf die Migration schreiben.
- Testfall unverändert: genau ein Persistenz-Write mit normalisierten Gruppen.
- Testfall konkurrierende Benutzeränderung: null Persistenz-Writes; der stale Snapshot wird verworfen.
- Bereits normalisierte Gruppen: null Persistenz-Writes.

## UI-/Boundary-Regression 0.5.10

- Gegen 0.5.9 müssen `internal/config`, `internal/startup`, `internal/calibration`, `internal/media`, `internal/baichuan`, `internal/baichuanaudio`, `native/aec-helper/main.cc` und `Dockerfile` nach Normalisierung des rein mechanisch geänderten Go-Modulpfads bytegleich bleiben; funktionale Audio-/AEC-/Transportlogik darf sich nicht ändern.
- UI `nvr_channel_number: 2` muss vor Gatewaystart exakt `nvr_channel: 1` und `reolink_stream_path: /Preview_02_sub` erzeugen.
- `auto` und `nvr` verwenden die gewählte NVR-Kanalnummer; explizites `standalone` erzeugt unabhängig vom UI-Wert intern Kanal 0 und `/Preview_01_sub`.
- Upgrade von 0.5.1 `nvr_channel: 1` muss einmalig zu UI-Kanal 2 migriert werden.
- Öffentliche Optionen/Schema/DE/EN/Testkonfiguration enthalten keinen `reolink_stream_path` und keinen `nvr_channel`.
- Bashio-Kompatibilitätsfallback `app.options` → `addon.options` bleibt erhalten.

- Öffentliche Konfiguration besteht exakt aus fünf Gruppen: `reolink`, `sip`, `audio`, `call`, `diagnostics`.
- `visitor_entity` liegt im Block `call`.
- `echo_cancellation_search_window_ms` ist weder in `options` noch `schema` noch den Übersetzungen vorhanden und wird bei Upgrade aus gespeicherten Optionen entfernt.
- Der Runtime-Adapter injiziert intern weiterhin `echo_cancellation_search_window_ms: 300`, damit der unveränderte Go-Konfigurationsparser denselben Runtimevertrag erhält.

## Patch-Regression 0.5.1

Zusätzlich zu den 0.5.0-Regressionsprüfungen:

- Bash-Syntax des s6-Startskripts.
- Kompatibilitätswrapper separat mit nur `bashio::app.options` sowie nur `bashio::addon.options` simulieren.
- `icon.png` als 128×128-PNG und `logo.png` als PNG prüfen.
- eingebettetes Ingress-Logo per Go-Test auf PNG-Signatur prüfen.
- sicherstellen, dass `internal/media`, `native/aec-helper/main.cc`, `internal/calibration`, `internal/startup` und `Dockerfile` gegenüber 0.5.0 bytegleich bleiben.
- Historischer 0.5.1-Patchtest; für 0.6.0 gilt weiterhin die gruppierte 24-Feld-Konfiguration aus `config.yaml`.

## Erster Hardwaretest 0.6.0

Empfohlen:

```yaml
echo_cancellation_enabled: true
webrtc_high_pass_filter_enabled: true
webrtc_noise_suppression_enabled: true
log_level: debug
dry_run: false
```

### Erwarteter Start

1. `automatic Reolink mode detection selected ...` bei `reolink_mode: auto`.
2. Warnung, dass der akustische Marker gleich hörbar wird.
3. Marker wird ungefähr eine Sekunde über die Doorbell abgespielt.
4. `automatic acoustic latency calibration accepted` mit `delay_ms`, `search_min_ms`, `search_max_ms`.
5. SIP-Registrierung und HA-Subscription werden aktiv.

Bei fehlgeschlagener Messung muss explizit `cached fallback` oder `safe fallback` geloggt werden; die App darf allein deshalb nicht abstürzen.

### Gespräch

Für einen ersten Call 60–120 s sprechen und im Debug-Log prüfen:

- Es erscheinen **keine** `AEC delay tracker updated`-Meldungen; `delay_ms` bleibt über den gesamten Call auf dem kalibrierten Startwert. `native_delay_ms`, ERLE und Residual-Echo-Likelihood dienen weiterhin zur Diagnose der AEC3-Feinausrichtung.
- `missing_render_frames` wächst nach der initialen Long-Delay-Füllphase nicht weiter.
- Kamera-Smoother: keine Hard-Drops/Underruns/Timeline-Rebases im Normalfall.
- SIP→Baichuan: keine Sequence Gaps/Reorder/Late-Duplicates.
- SIP→Baichuan: `elastic_ratio_min` nicht kleiner als 0,98 und `elastic_ratio_max` nicht größer als 1,03; `fifo_underrun_samples` darf nur die nach der Korrektur tatsächlich verbleibende Stille zählen.
- Bei einem bekannten kurzen Zulaufloch soll `fifo_raw_shortage_samples` steigen, während `fifo_underrun_samples` kleiner bleibt oder null ist. Fades/Splices dürfen nur an den zugehörigen Unter-/Überlaufgrenzen zählen.
- `native_stats_mask` und einzelne `native_*`-Felder sind jetzt konsistent.
- `recent_erle_db` bleibt über den Gesprächsverlauf stabil und korrespondiert mit dem Höreindruck.

### Statistikfix gezielt

Im 0.4.3-Hardwarelog war `native_stats_mask=0x3b`, aber Go zeigte nur einen Teil der gesetzten Felder. In 0.5.0 muss dieselbe Maske die Bits 0,1,3,4,5 als ERL, ERLE, Residual, Residual-Recent-Max und Delay sichtbar machen, sofern die WebRTC-Runtime diese Maske liefert.

## Hardwarestand

Die Livebildanzeige wurde auf der Zielinstallation mit FRITZ!Box 4050 und FRITZ!Fon erfolgreich bestätigt. Double-Talk auf der Zielhardware wird weiterhin später separat getestet. Der stabile 53-s-0.4.3-Einsprechtest bleibt bis dahin die Referenz für Single-Talk-Echoreduktion.
