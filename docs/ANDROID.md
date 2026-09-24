# Android 1.0.0

The Android target lives on `feature/v2-android`, independently of `main` and
`feature/v2-standalone`. Android 8 / API 26 or newer is supported; APKs contain
ARM64 and ARMv7. The app reuses the HA gateway's Go SIP/RTP, Baichuan,
visitor-trigger, call-control and codec code through a gomobile AAR.

## V1.0: operation, routes and backup

- **Übersicht** contains the Gateway on/off switch, compact connection status,
  a route selector for test calls, hangup, calibration status and live preview.
  The switch follows service state and is disabled during startup/teardown.
  An active call or dial attempt is confirmed before stop or settings restart.
- **Einstellungen** groups camera, SIP, routes, timing, WebRTC, live image,
  operation and backup in expandable sections. Turning on applies and validates
  the visible form. **Einstellungen speichern** restarts an enabled gateway;
  saving while off leaves it off. Existing alpha credentials, destinations,
  device mode and opt-in audio/image settings are retained.
- **Klingeln & Rufrouten** supports up to 32 named routes with a door destination
  and up to three parallel destinations each. One route is selected for the
  camera visitor trigger. All routes share one camera and the configured SIP
  accounts, with one active call globally. Tests target the selected *running*
  route and respect its runtime availability; unsaved routes cannot be called.
- **Nur manuell / Testanrufe** disables automatic visitor calls, while SIP and
  manual calls remain usable. **Passivmodus** retains its separate meaning:
  automatic event monitoring without SIP/calls. Log level is now configurable.
- **Sicherung & Wiederherstellung** exports the saved configuration through the
  Android document picker and loads standalone schema-2 JSON files (max 256 KiB).
  Credentials are exported exactly, including spaces. The file therefore needs
  private storage. Import validates schema, fields and Android compatibility,
  expands standalone defaults, then loads a draft. It does not modify saved
  settings or restart until **Speichern** is pressed. Unsupported Linux
  RTSP/automatic profiles or `sip_registrar=auto` are rejected explicitly.
- **Diagnose** retains the complete connection, WebRTC/AEC, acoustic measurement
  and last media-session counters from alpha6, with a redacted copy action.
- Explicit switch-off persists through device restart and package replacement.
  Boot startup requires both the saved on-intent and **Nach Geräteneustart
  automatisch starten**. Package replacement resumes an enabled gateway; legacy
  alpha preferences keep their previous autostart behavior until first toggled.
  One process-wide service executor serializes old-instance audio teardown and
  new-instance startup during quick stop/start or Activity recreation.

See [ANDROID-PARITY.md](ANDROID-PARITY.md) for the branch comparison and remaining
platform differences. This is Android app version 1.0.0; the reused gateway core
continues to identify itself separately as 2.0.0-beta.1.

Update over alpha3–alpha6 using the same branch signing identity; do not
uninstall to update. First check: verify saved targets under **Klingeln &
Rufrouten**, toggle the gateway on, press the doorbell and answer. Then toggle
off/on, export/import a backup and verify AEC/live image if enabled. V1.0 unit
tests cover alpha routing migration, full configuration round-trip including
secrets, autostart/off policy, per-route availability and invalid imports.
CI compiles/tests/lints the Android app and checks APK/native alignment and its
signer. The new menu and service behavior still need this device-level check.

## WebRTC and acoustic measurement in alpha6

- Fix the unconditional FFmpeg executable check in acoustic calibration. Direct
  camera and NVR capture now use the same decoder selection as calls: Android
  MediaCodec for AAC, built-in ADPCM decoding where applicable. RTSP still needs
  FFmpeg. Previously Android always chose `safe fallback` before contacting the
  camera; 1450 ms was the built-in value, not a measurement.
- The app separates **AEC** (WebRTC processing) from **Laufzeitmessung** (the
  coarse acoustic delay). `calibration_details` now exposes the failure reason
  or accepted correlation and peak ratio. An enabled AEC with a fallback delay
  is not labelled as successfully calibrated. Restart with AEC enabled to run
  the audible coded marker; allow up to about one minute before placing a call.
- `media.echo_stats` reports actual native processing: capture/render frame
  counts, missing aligned reference frames, native ERLE and residual echo
  likelihood. Validity flags distinguish absent statistics from measured zero.
  Counts are sampled without depending on debug logging and retained on hangup.
  Frames/ERLE are diagnostics, not a guarantee of echo-free physical audio.
- Audio/echo counters now reset when a media session starts, after an outgoing
  call is answered. An unsuccessful SIP dial does not erase previous media
  diagnostics. `stats_call_started_at` identifies their source session;
  `stats_current_call` prevents old counters being displayed as a new call's.
- Native tests now check synthetic echo attenuation AND near-end signal retention
  with HPF/NS disabled, in addition to the wire protocol on silence. Platform
  tests cover 16 kHz calibration PCM without FFmpeg, preserved post-call stats,
  unavailable/invalid native measurements and rejected outgoing calls.

For a hardware check, enable AEC, restart, wait for calibration to complete,
then answer a call and alternate speaking at the phone and doorbell for 20–30
seconds. Copy diagnostics during or immediately after the call. A failed
measurement keeps a cached/default delay and now includes its reason. Changing
acoustic conditions or timing can still require another gateway restart to
measure again; live coarse-delay tracking remains disabled as in the HA core.

## Connection recovery and diagnosis in alpha5

Alpha4 could remain at `starting` while SIP was registered and the visitor
connection failed. Its last-change timestamp (`updated_at`) and revision then
stayed unchanged, because repeated `trigger_connected=false` callbacks were
identical and connection errors were logged without appearing in the API.
The reported dump alone cannot identify TCP, authentication or subscription as
the failing step, and does not establish an Android polling failure.

- Event sessions now use the reference protocol's host-control header channel
  250 for nonce/login/keepalive and 251 for the event subscription. The audio
  preview/talkback headers and zero-based camera channel remain unchanged.
- A valid authenticated alarm push can confirm a subscription before/without a
  separate command-31 acknowledgement. Buffered events remain ordered; malformed
  pushes cannot confirm it, and login failures are never bypassed. Once events
  arrive, liveness uses command 93 instead of periodically replaying subscription
  snapshots. A failed keepalive write closes the connection for recovery.
- `gateway.trigger_events` now also reports `stage`, `connection_attempts`,
  `successful_connections`, `last_attempt_at`, `last_connected_at`, `last_error`,
  `last_error_stage`, `last_error_at`, and `next_retry_at`. The last error remains
  visible while retrying and clears on a successful subscription. Raw nonce XML,
  hashes and configured credentials are excluded from these diagnostics.
- The app displays the connection stage, last camera error and status-poll age.
  API `generated_at` advances on every status response, independently of the
  state-change timestamp. This distinguishes a quiet/unavailable camera from a
  stalled status display without inventing state-change events.
- Manual test calls become available after call-control setup and SIP registration,
  independently of visitor push connectivity. Passive mode, route availability
  and the single-call lock still apply. This lets a test call separately check
  the working SIP/audio path when automatic ringing is unavailable.

Install over alpha3/alpha4, keep AEC off for the initial comparison, wait about
30 seconds, copy diagnostics, and try **Testanruf**. The protocol regression tests
cover rejected login/subscription, missing acknowledgement with valid pushes,
invalid/absent pushes, reconnect diagnostics and real UDP SIP registration/test
calling while the camera TCP port is unavailable. Actual camera compatibility
still needs the user's hardware test; these changes do not claim the dump's
unknown camera-side cause has been conclusively identified.

## New in alpha4

- Visitor events no longer discard a first press arriving after subscription
  startup just because the firmware did not send an initial idle state. Only an
  initial active state within two seconds after subscribing is treated as a
  snapshot; an idle-to-active edge inside that window still triggers immediately.
  Repeated active states remain suppressed. Wait a few seconds after connection
  before the first hardware test. Reconnect snapshots have the same short guard.
- `gateway.trigger_events` reports received messages, matching-channel states,
  visitor states, emitted trigger events, invalid messages, initial active states,
  last protocol channel (zero-based), and last message time. These are camera
  event counters, not successful call counts. They contain no raw XML or secrets.
- **Audio / WebRTC** enables native WebRTC APM/AEC3 in the app process, with the
  HA runtime's calibrated delay alignment, optional high-pass filter and optional
  moderate noise suppression. Android and Linux use the same C++ processor and
  diagnostic protocol. No Android microphone or external executable is involved.
- **Live-Bild** enables an app JPEG preview and a copyable FRITZ!Fon URL. Enable
  HTTP or HTTPS on the camera, save/restart, then start the preview. JPEG refreshes
  every three seconds while the preview and activity are active; this is not a
  continuous video stream. HTTP/HTTPS snapshot capture runs entirely in Go.
- A separate image-only LAN listener defaults to port **18099** (configurable).
  The exact tokenized primary JPEG URL is available via **FRITZ!Fon-Bildadresse
  kopieren**. The status/control API and setup UI stay inaccessible on that port.
  The image route also enforces private/local source IPs. No camera credentials
  are included in the URL. The image token is persistent and separate from the API
  token; diagnostic copies do not contain either token. Assign Android a stable
  IPv4 address in the FRITZ!Box before configuring the phone image URL.

AEC and live images are opt-in; updating does not silently change working audio
settings. Starting an active gateway with AEC enabled can play the existing
calibration signal and take about a minute. Actual echo quality and visitor-event
compatibility still require a test with the user's camera. Two-way audio was
confirmed on alpha3. The alpha4 signer is intended to match alpha3 so an update
can retain preferences.

## Camera-to-phone audio fix in alpha3

Alpha2 could accept an AAC header without decoding audible camera audio. Alpha3
configures MediaCodec with the AAC AudioSpecificConfig from ADTS and feeds one
raw AAC-LC access unit per buffer (including CRC-header handling). The synchronous
Go/Android bridge returns PCM directly, removing a queue that could block its own
consumer. Camera receive becomes ready only after the first decoded PCM; a
10-second startup timeout now reports received packet and decoded sample counts.

The status and copied diagnostics include `media.camera_audio`: received camera
packets/bytes, decoded 8 kHz PCM samples, maximum absolute PCM level (0–32768),
and RTP packets sent to the phone. Counters remain visible after hangup and reset
for the next call. Growing RTP counts prove local UDP sends, not phone playback;
a zero peak means the camera stream decoded to silence. These counters contain
no audio recordings. Two-way audio was confirmed by the user on alpha3.

## Direct camera operation (no NVR, no Home Assistant)

Select **Kamera direkt (ohne NVR)** and enter the camera's own IP/hostname and
Reolink credentials. Enable the camera's **Basic Service**, normally TCP 9000.
The camera must be directly reachable from Android and support Baichuan push
events, audio preview and ADPCM talkback. Battery-only devices and firmware
without these services are not established compatibility targets.

- New setups default to the `direct` profile, with protocol channel 0 fixed for
  events, audio reception and talkback. A saved NVR channel cannot leak into it.
- The camera's visitor event triggers the shared call controller directly.
- Receive and talkback use Baichuan. AAC-LC decoding uses Android MediaCodec;
  ADPCM and G.711 conversion run in Go. Neither FFmpeg nor an NVR is required.
- **Über NVR** remains available with 1-based channel selection. Existing alpha1
  preferences retain NVR mode until the user explicitly changes it.
- The Linux profile named `standalone` still means RTSP/ONVIF and is distinct
  from `direct`. Android rejects RTSP and automatic transport selection because
  its RTSP receive implementation would otherwise try to launch FFmpeg.

## Functions carried over from HA

- Outgoing door calls, test calls and hangup.
- Incoming calls, caller allow-list and optional connection tone at the camera.
- Parallel calling of up to three additional destinations through a second SIP
  account. The first answered leg wins; other legs are cancelled by the existing
  shared controller. The door account can be disabled for parallel-only operation.
- SIP registrar/local ports, display name and PCMA/PCMU/automatic codec selection.
- Ring duration, maximum call duration, audio inactivity timeout and debounce.
- Readable connection/registration/call status, last visitor event, error details
  and copyable diagnostics. Test/hangup availability follows the running core.

There is no Android DTMF action configuration or continuous RTSP/WebRTC video
player in this version. Android live images use camera HTTP/HTTPS snapshots;
the Linux FFmpeg fallback is not attempted on Android. Hardware verification of
echo and latency is still required; unit tests do not establish camera compatibility.

## Android lifecycle

**Einstellungen speichern** validates the entire candidate configuration
before saving, then restarts an enabled gateway or leaves a stopped one off.
Lifecycle and API calls run off the UI thread. The Go bridge refuses overlapping
runtimes and closes the private loopback API on shutdown or fatal runtime exit.
Configuration includes credentials and is kept in private app preferences; app
backup is disabled. Diagnostic copies exclude configuration and redact configured
passwords, but may include phone numbers and local IP addresses.

The foreground service uses `connectedDevice`, with a partial CPU wake lock and
Wi-Fi performance lock only while the gateway runs. Use a powered device and
disable battery optimization. A foreground service alone does not prevent CPU
suspend. Network/IPv4-address changes restart the runtime after a short debounce,
so SIP can bind to the new address. An active call ends during that restart.
OEM battery policies still require testing on the actual device.

Optional boot start runs after normal BOOT_COMPLETED (after unlock where
required), provided the gateway was not explicitly switched off. App updates
resume a gateway with saved on-intent. No credentials are read during locked boot.

References:
- https://developer.android.com/develop/background-work/services/fgs/service-types
- https://developer.android.com/develop/background-work/background-tasks/awake/wakelock

## Build and signing

Install Android SDK API 36, NDK 27.0.12077973, Go (CI: 1.26), Gradle 8.13 and
JDK 17, then run:

```sh
python3 -m pip install meson==1.5.2 ninja==1.11.1.1
python3 android/build-aec.py
bash android/build-core.sh
gradle -p android :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
python3 android/check-apk.py android/app/build/outputs/apk/debug/app-debug.apk
```

The WebRTC 1.3 archive is SHA-256 pinned; its upstream Meson wrap pins Abseil
20230125.1 and its Meson patch. Both are built as static dependencies of the JNI
library, including a static C++ runtime. Their license notices are packaged in
the APK and available via **WebRTC-Lizenzen**. `python3 android/build-aec.py
--host-smoke` builds the identical processor on Linux and runs the PCM protocol
smoke test without an Android SDK.

Native Go and WebRTC libraries are linked with 16 KiB ELF page alignment; CI checks all
ARM native LOAD segments and verifies the APK with `zipalign -P 16`.
The generated AAR and APK are CI artifacts. CI also runs the shared Go tests,
shuffle tests, race detector and Linux/HA builds. Starting with alpha3, CI creates
or restores a debug keystore at `REOLINK_ANDROID_KEYSTORE`, passes that exact path
to Gradle and verifies the APK signature. The key is cached per Android branch.
The previous implicit path did not exist when the cache was saved, so alpha1/2
keys were ephemeral. **Updating from alpha2 to alpha3 requires recording the
settings, uninstalling alpha2, installing alpha3 and entering the settings again.**
Uninstalling removes the app's configuration. V1.0 adds explicit settings export;
alpha2 itself does not have it.
Future updates can retain settings while the cache survives. Debug signing is
not a permanent release-signing solution: cache loss or another build machine
can still change the certificate. No production signing key is committed or
generated into the source repository.

## Device acceptance test

1. Install the APK. For a signature-conflict error, follow the note above.
2. Select **Kamera direkt (ohne NVR)**, enter camera IP/credentials/Basic-Service
   port and an explicit SIP registrar address (which need not be the router).
3. Save/start with **Passivmodus** checked. Verify the visitor connection and
   press the real doorbell. The last visitor time must update; SIP is intentionally
   unregistered and the test-call button disabled.
4. Uncheck Passivmodus, save/restart, verify registration and place a test call.
   Verify camera-to-phone audio, phone-to-camera talkback and hangup on both ends.
5. Check a real doorbell press and repeated presses within the configured debounce.
   Inspect camera-event, visitor-state and emitted counters when a press does not call.
   Then enable AEC, save/restart, wait for calibration and compare echo during a call.
   Enable live images, save/restart, verify the preview, and copy the image URL to
   the FRITZ!Box. Test the image during calls and with the Android screen off.
6. If enabled: test allowed/denied incoming callers, connection tone and parallel
   destinations. Check that other destinations stop ringing when one answers.
7. Save a changed setting while running; verify that it takes effect. Stop/start
   repeatedly, turn off the screen for 30 minutes, interrupt Wi-Fi, restore it,
   then test another call. Test device reboot if boot start is enabled.
8. Copy diagnostics from the app if a step fails. Test with the Reolink app's
   two-way-talk session closed so that both apps do not claim talkback at once.
