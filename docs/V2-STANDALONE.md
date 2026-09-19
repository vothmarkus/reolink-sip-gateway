# Version 2 – Standalone-Beta

`2.0.0-beta.1` wird auf `feature/v2-standalone` entwickelt. Der Gateway-Kern ist für Home Assistant, Docker und natives Linux derselbe. Die Beta ergänzt den direkten Reolink-Klingeltrigger, eine lokale Konfigurationsseite und ARM64. Der stabile `main`-Branch wird dadurch nicht aktualisiert.

## Plattform und Umfang

| Installation | Architektur | Konfiguration und Trigger |
| --- | --- | --- |
| Home-Assistant-App | amd64, aarch64 | Bisherige HA-Optionen, Ingress und HA-Besuchersensoren |
| Eigenständiger Docker-Container | amd64, arm64 | Lokale Webseite, direkte Reolink-Ereignisse oder manuelle Anrufe |
| Natives Debian-Paket | amd64, arm64 | systemd-Dienst, dieselbe Webseite und dieselben Trigger wie Docker |

Für den **Raspberry Pi Zero 2 W** ist das native **arm64-Paket** mit **Raspberry Pi OS Lite, 64 Bit, Debian 13/Trixie** vorgesehen. Raspberry Pi führt den Zero 2 W bei den unterstützten 64-Bit-Geräten auf; Lite benötigt keine Desktop-Umgebung ([offizielle Betriebssystemübersicht](https://www.raspberrypi.com/software/operating-systems/)). Ein 32-Bit-System und der ältere Zero W werden nicht gebaut. Die native Beta setzt die Trixie-Version von [WebRTC AudioProcessing](https://packages.debian.org/trixie/libwebrtc-audio-processing-1-3) voraus und ist kein Bookworm-Paket.

Eine Installation versorgt weiterhin **eine Kamera und einen gemeinsamen Audiokanal**. Der direkte Trigger startet eine ausgewählte Anrufroute; weitere Routen können über Oberfläche/API getestet werden. NVR-Livebilder können mehrere Kameras zeigen. Eigenständige Aktionen auf DTMF-Tasten sind zurückgestellt. Die bestehenden DTMF-Ereignisse für die HA-Integration bleiben erhalten.

**Hardware-Abnahme offen:** ARM64-Build und AEC-Protokolltest ersetzen keine Messung auf dem Zero 2 W. Echte Klingelereignisse, Audiolatenz, Echounterdrückung, CPU/RAM und Verhalten bei WLAN-Ausfällen müssen dort geprüft werden.

## Native Installation auf dem Zero 2 W

1. Raspberry Pi OS Lite **64 Bit / Trixie** installieren; WLAN und SSH einrichten. Im Heimnetz eine feste DHCP-Zuordnung für den Pi verwenden.
2. Im Repository unter **Actions → CI** einen erfolgreichen Lauf von `feature/v2-standalone` öffnen. Das Artefakt **reolink-sip-gateway-linux-arm64** herunterladen und entpacken. Darin liegt `reolink-sip-gateway.deb`. Die CI-Artefakte sind 14 Tage verfügbar; es gibt noch keinen stabilen V2-Release oder veröffentlichten Container-Tag.
3. Das Paket auf den Pi kopieren und im Verzeichnis der Datei ausführen:

```sh
dpkg --print-architecture
# Erwartet: arm64
sudo apt update
sudo apt install ./reolink-sip-gateway.deb
sudo systemctl enable --now reolink-sip-gateway
sudo systemctl status reolink-sip-gateway --no-pager
```

Das Paket installiert Gateway und AEC-Helfer unter `/usr/bin`, einen eigenen Dienstbenutzer und `/var/lib/reolink-sip-gateway`. FFmpeg und WebRTC werden als Paketabhängigkeiten installiert. Beim ersten Start entsteht der Zugangsschlüssel:

```sh
sudo cat /var/lib/reolink-sip-gateway/admin-token
```

Im Browser `http://<IP-des-Pi>:18099` öffnen und diesen Schlüssel eingeben. Er bleibt bei Updates erhalten und ist unabhängig vom API-Token. Ohne gespeicherte Konfiguration startet zunächst nur die Einrichtung.

## Ersteinrichtung

1. **Reolink & Klingel:** Kamera-/NVR-Adresse, Benutzername und Passwort eintragen. Für einen NVR den öffentlichen, bei **1** beginnenden Kamerakanal wählen. Bei direkter Kamera ist der Kanal intern immer 0. Der direkte Klingeltrigger benötigt den Reolink-Dienstport, standardmäßig TCP 9000; dieser Dienst muss am Gerät aktiviert und vom Pi erreichbar sein.
2. **Telefonie:** `auto` verwendet die IPv4-Standardroute mit der niedrigsten Metrik als Adresse der vermuteten FRITZ!Box. Das ist keine Geräteerkennung. Befindet sich die FRITZ!Box nicht am Standardgateway, ihre IP oder ihren Hostnamen eintragen. Die SIP-Zugangsdaten aus der FRITZ!Box-Konfiguration übernehmen.
3. **Anrufrouten:** Die Standardroute verwendet die FRITZ!Box-Klingeltaste `11`. Bei Mobilrufen das zweite SIP-Konto einschalten und bis zu drei Rufnummern pro Route setzen. Unter Reolink die Route für den echten Klingeldruck auswählen.
4. **Audio & Betrieb:** Der **Passivmodus** ist anfangs aktiv. Er empfängt Klingelereignisse, führt aber weder SIP-Anrufe noch die hörbare AEC-Kalibrierung aus. Eingehende automatische Rufannahme ist standardmäßig ausgeschaltet.
5. **Speichern & übernehmen**. Sobald der direkte Trigger verbunden ist, die echte Klingel zunächst im Passivmodus prüfen. Danach den Passivmodus ausschalten, erneut speichern und Registrierung, Testanruf sowie Audio prüfen. Die AEC-Kalibrierung kann beim Start einen kurzen Ton an der Doorbell ausgeben.

Speichern validiert alle Einstellungen vor dem Schreiben und bewahrt die vorherige Datei als `config.json.previous`. Leere Passwortfelder behalten das vorhandene Passwort; zum Löschen gibt es eine eigene Auswahl. Ein erfolgreicher Speichervorgang beendet laufende Gespräche und baut die Verbindungen mit den neuen Werten neu auf. Bei einem aktiven Gespräch fragt die Oberfläche vorher nach. Die Webseite bleibt währenddessen erreichbar.

Der erste Besucherzustand nach jeder Reolink-Verbindung dient als Ausgangszustand und löst keinen Ruf aus. Danach zählen nur Wechsel von inaktiv zu aktiv; doppelte Meldungen und die konfigurierte Entprellzeit unterdrücken Wiederholungen. Ein während einer Unterbrechung erfolgter Klingeldruck wird nicht nachträglich angerufen. Die Statusanzeige „Verbunden“ bestätigt das Abonnement, nicht die erfolgreiche Prüfung der physischen Klingeltaste.

## Docker unter Linux

Docker Engine mit Compose und Buildx wird vorausgesetzt. Aus dem Repository:

```sh
git switch feature/v2-standalone
docker compose -f packaging/compose.yaml up -d --build
docker compose -f packaging/compose.yaml exec gateway cat /data/admin-token
```

Danach `http://<Docker-Host>:18099` öffnen. Compose verwendet **Host-Netzwerk**, damit SIP/RTP und die Ermittlung des Standardgateways die Netzwerkadressen des Hosts verwenden. Es ist für Linux vorgesehen. Nur eine Instanz auf denselben SIP-/Webports betreiben. In einem Bridge-Netzwerk ist `auto` typischerweise die Docker-Bridge; dort sind Registrar-, SIP-/RTP- und Adresskonfiguration gesondert zu lösen.

Das benannte Volume `gateway-data` enthält Einstellungen und Identitäten; der Container läuft als UID/GID 10001. Bei einem selbst gewählten Bind-Mount muss diese Kennung in das Verzeichnis schreiben können. `docker compose down` erhält das Volume, `down -v` löscht es. Die Health-Prüfung bestätigt die erreichbare Einrichtungsseite, nicht Kamera- oder SIP-Bereitschaft.

Einzelnes Image bauen:

```sh
docker buildx build --load --platform linux/arm64 --target standalone \
  -t reolink-sip-gateway:2.0.0-beta.1 reolink_sip_gateway
```

Auf einem anderen Build-Host erfordert der native C++-Helfer für ARM64 eine passende Buildx-Node oder QEMU. Die CI baut jede Architektur auf einem passenden Runner. Auf dem Zero 2 W vorzugsweise das fertige Paket verwenden; ein vollständiger Containerbuild benötigt deutlich mehr Ressourcen als der laufende Dienst.

## Einstellungen, Schlüssel und Daten

| Datei im Datenverzeichnis | Zweck |
| --- | --- |
| `config.json` | Versionierte Standalone-Konfiguration, einschließlich Passwörtern |
| `config.json.previous` | Vorherige Datei vor dem letzten erfolgreichen Speichern |
| `admin-token` | Zugangsschlüssel für die Konfigurationsseite |
| `integration-api-token`, `integration-api-instance-id` | Bestehende API-Identität für die HA-Integration |
| `fritzfon-live-image-token` | Eigenständiges Geheimnis für die Livebildpfade |
| `aec-calibration.json` | Gespeicherte Kalibrierung |
| `fritzfon-live-image-catalog.json` | Letzter erkannter NVR-Kamerakatalog |

Datenverzeichnis: nativ `/var/lib/reolink-sip-gateway`, Docker `/data`. Identitäten und neu gespeicherte Konfigurationsdateien besitzen Modus `0600`. Unter **Audio & Betrieb** lässt sich die Konfiguration als JSON exportieren und importieren. Der Export enthält Passwörter, aber keine API-/Bild-/Admin-Identitäten; für eine vollständige Sicherung das ganze Datenverzeichnis bei gestopptem Dienst sichern. Ein Import wird erst durch **Speichern & übernehmen** aktiv. Dateien mit unbekannter Schema-Version werden zurückgewiesen.

Eine [Beispielkonfiguration](../packaging/config.example.json) beschreibt Schema 2. Platzhalter ersetzen. Die Prüfung startet keine Netzwerkdienste:

```sh
reolink-sip-gateway -mode standalone -check-config -config ./config.json
```

Die Konfigurationsdatei kann bei gestopptem Dienst auch von Hand geändert werden. Externe Dateiänderungen während des Betriebs werden nicht automatisch eingelesen und blockieren ein Überschreiben durch eine alte Browsersitzung; danach den Dienst neu starten und die Seite neu laden. Gleichzeitig geöffnete Browser erhalten bei veralteten Werten einen Konflikthinweis.

Oberfläche, API und Bilder sind auf lokale/private Quellnetze beschränkt. Die Oberfläche nutzt HTTP im Heimnetz und ist nicht für eine öffentliche Portfreigabe vorgesehen. Bei Bedarf einen eigenen TLS-Zugang für das lokale Netz einrichten. Die Login-Sitzung gilt 24 Stunden. Um einen verlorenen Admin-Schlüssel zu ersetzen, den Dienst stoppen, `admin-token` entfernen und wieder starten; alte Sitzungen werden dadurch ungültig. API- und Bildtoken bleiben bestehen.

## Startparameter

| Argument | Umgebungsvariable | Standard im Standalone-Modus |
| --- | --- | --- |
| `-mode` | `REOLINK_SIP_MODE` | `auto`: mit Supervisor-Token HA, sonst Standalone |
| `-data-dir` | `REOLINK_SIP_DATA_DIR` | `/var/lib/reolink-sip-gateway` |
| `-config` | `REOLINK_SIP_CONFIG` | `<data-dir>/config.json` |
| `-listen` | `REOLINK_SIP_LISTEN` | `0.0.0.0:18099` |

Das Docker-Entrypoint setzt Modus und Datenverzeichnis ausdrücklich. Zusätzliche Flags können diese Werte ersetzen. `-version` zeigt die Programmversion; `-healthcheck` prüft den lokalen HTTP-Port. Native Dienstanpassungen über `systemctl edit reolink-sip-gateway` vornehmen. Bei einem anderen Datenverzeichnis auch Benutzerrechte und die `ReadWritePaths`-Einstellung des Dienstes anpassen.

## Update und Wiederherstellung

Vor einem Beta-Update den Dienst stoppen und das Datenverzeichnis einschließlich Identitäten sichern. Ein neueres `.deb` mit `sudo apt install ./reolink-sip-gateway.deb` installieren und den Dienst wieder starten. Das Paket startet einen zuvor aktiven Dienst bei einem Upgrade neu. Die Konfiguration und bestehende Token werden nicht zurückgesetzt. Ein Paket-Downgrade kann eine passende Datensicherung erfordern, falls spätere Betas das Schema ändern.

Zum Wiederherstellen der letzten Konfiguration:

```sh
sudo systemctl stop reolink-sip-gateway
sudo cp -p /var/lib/reolink-sip-gateway/config.json.previous \
  /var/lib/reolink-sip-gateway/config.json
sudo systemctl start reolink-sip-gateway
```

Nur eine zuvor geprüfte Sicherung zurückspielen. Das Entfernen des nativen Pakets lässt das Datenverzeichnis bewusst bestehen. In Docker für ein Update neu bauen und mit `up -d` ersetzen; das Volume erhalten. Eine HA-Installation verwendet weiterhin ihren Supervisor-Startadapter und eigene Daten. HA-Optionen sind keine Standalone-Schema-2-Datei; eine automatische Übertragung zwischen Installationen ist in dieser Beta nicht enthalten.

## Fehlerdiagnose und Hardware-Abnahme

Native Logs: `sudo journalctl -u reolink-sip-gateway -f`. Docker: `docker compose -f packaging/compose.yaml logs -f gateway`. Bei Konfigurations- oder Verbindungsfehlern bleibt die Einrichtung verfügbar. Fehlgeschlagene Laufzeitstarts werden nach 15 Sekunden wiederholt; die direkte Ereignisverbindung hat eine eigene Wiederverbindung mit bis zu 30 Sekunden Pause. Für Fehlerberichte Geheimnisse und private Gerätekennungen aus Logs entfernen.

Vor Freigabe auf dem Zero 2 W prüfen:

- Kaltstart und Neustart: Konfiguration/Identitäten bleiben erhalten, kein ungewollter Anruf.
- Reale Klingeltaste direkt und gegebenenfalls am NVR: richtiger Kanal, genau ein Ruf, korrekte ausgewählte Route, keine Auslösung durch Bewegung.
- Kamera-/WLAN-Unterbrechung: erneute Verbindung ohne Phantomruf; nächster neuer Klingeldruck funktioniert.
- FRITZ!Box automatisch/manuell, Tür-/Mobilkonto, gleichzeitige Ziele und Auflegen; eingehende Rufe nur nach ausdrücklicher Aktivierung.
- Verständliches Audio in beiden Richtungen, AEC-Kalibrierung, Echo, Aussetzer und CPU/RAM während längerer Gespräche. Mit und ohne Livebilder vergleichen.
- Einstellungen bei laufendem Gespräch ändern: bestätigtes Gesprächsende, alte Ports freigegeben, neue Registrierung, Webseite weiterhin erreichbar.
- Sicherung/Import und Neustart sowie mindestens ein längerer Betrieb mit wiederholten Anrufen.

CI prüft Go-Tests, Shuffle, Vet und Race Detector, statische amd64-/ARM64-Builds, den bestehenden HA-Konfigurationsadapter sowie Standalone-/HA-Images und Debian-Pakete. Der native AEC-Test verarbeitet 100 PCM-Frames auf beiden Architekturen; er misst keine reale Echoqualität oder Pi-Leistung.
