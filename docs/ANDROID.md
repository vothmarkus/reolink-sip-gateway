# Android 0.2.0-alpha2

The Android target lives on `feature/v2-android`, independently of `main` and
`feature/v2-standalone`. Android 8 / API 26 or newer is supported; APKs contain
ARM64 and ARMv7. The app reuses the HA gateway's Go SIP/RTP, Baichuan,
visitor-trigger, call-control and codec code through a gomobile AAR.

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

AEC, its high-pass/noise filters and FRITZ!Fon live images remain unavailable
on Android. Validation rejects these features. There is no Android DTMF action
configuration in this version. Hardware verification of audio, echo and latency
is still required; protocol/unit tests do not establish camera compatibility.

## Android lifecycle

**Speichern & Gateway neu starten** validates the entire candidate configuration
before saving, then stops the previous runtime before starting the new one.
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
required) or an app update; no credential access is attempted during locked boot.

References:
- https://developer.android.com/develop/background-work/services/fgs/service-types
- https://developer.android.com/develop/background-work/background-tasks/awake/wakelock

## Build and signing

Install Android SDK API 36, NDK 27.0.12077973, Go (CI: 1.26), Gradle 8.13 and
JDK 17, then run:

```sh
bash android/build-core.sh
gradle -p android :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
python3 android/check-apk.py android/app/build/outputs/apk/debug/app-debug.apk
```

Native Go libraries are linked with 16 KiB ELF page alignment; CI checks all
ARM native LOAD segments and verifies the APK with `zipalign -P 16`.
The generated AAR and APK are CI artifacts. CI also runs the shared Go tests,
shuffle tests, race detector and Linux/HA builds. The debug keystore is cached
per Android branch to support subsequent updates while that cache survives.
Debug signing is not a permanent release-signing solution: cache loss or a build
from a different machine can change the certificate. Alpha1 used an ephemeral
CI debug key; Android may therefore require uninstalling alpha1 before alpha2.
Record the settings first, since uninstalling removes them. No production
signing key is committed or generated into the source repository.

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
6. If enabled: test allowed/denied incoming callers, connection tone and parallel
   destinations. Check that other destinations stop ringing when one answers.
7. Save a changed setting while running; verify that it takes effect. Stop/start
   repeatedly, turn off the screen for 30 minutes, interrupt Wi-Fi, restore it,
   then test another call. Test device reboot if boot start is enabled.
8. Copy diagnostics from the app if a step fails. Test with the Reolink app's
   two-way-talk session closed so that both apps do not claim talkback at once.
