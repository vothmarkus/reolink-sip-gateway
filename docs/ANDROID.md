# Android alpha

The Android target lives only on `feature/v2-android`. It reuses the Go SIP,
RTP, Baichuan, trigger and call-control code through a `gomobile bind` AAR.

## First hardware target

The first alpha deliberately targets the already proven NVR path:

- Android 8 / API 26 or newer
- targetSdk 36
- Reolink mode `nvr`
- direct Baichuan visitor trigger on TCP 9000
- Baichuan receive/talkback
- AAC-LC decoded by Android MediaCodec instead of FFmpeg
- SIP/RTP and PCMA/PCMU remain in Go
- foreground service type `connectedDevice`
- optional BOOT_COMPLETED restart
- native Android settings/status screen

The first alpha **does not enable WebRTC AEC** and disables FRITZ!Fon live
images. Those are intentionally separate follow-up steps. The Android host
rejects a configuration that enables either feature instead of silently
falling back to an unavailable Linux helper.

## Why connectedDevice

The gateway maintains long-lived network connections to an external Reolink
device/NVR. Android's `connectedDevice` foreground-service type is designed
for interaction with external devices over a network and, unlike
`dataSync`, is not subject to Android 15's six-hour data-sync timeout. It is
also one of the foreground-service types that may be started from
`BOOT_COMPLETED` on Android 15+.

## Build

Install Android SDK API 36, NDK, Go and Gradle/JDK 17. Then:

```sh
bash android/build-core.sh
gradle -p android :app:assembleDebug
```

The Go AAR is intentionally generated and not committed. CI builds both the
AAR and debug APK.

## First device test

1. Install the APK and grant notification permission.
2. Enter NVR IP, Reolink credentials, 1-based NVR channel, explicit FRITZ!Box
   registrar address and SIP credentials.
3. Leave **Passivmodus** enabled and start the service.
4. Verify the status and press the real doorbell. The direct Baichuan trigger
   must be observed without a SIP call.
5. Disable Passivmodus, restart the gateway and run a test call.
6. Verify camera -> phone audio. If the NVR sends AAC, MediaCodec is used; no
   FFmpeg executable is present in the APK.
7. Verify phone -> doorbell talkback.
8. Only after the non-AEC path is stable, add the Android WebRTC APM/NDK
   adapter and repeat latency/echo measurements.

## Architecture

```text
Android Activity
      |
      v
Foreground GatewayService
      |
      +--> MediaCodecAudioAdapter ---- AAC/ADTS -> PCM16
      |
      v
gomobile AAR (mobilebridge)
      |
      v
Go gateway runtime
  |       |       |
Baichuan  SIP     RTP
  |
Reolink/NVR
```

The AAR boundary uses only gomobile-supported primitives: strings, booleans,
signed integers, byte arrays, interfaces and pointers to bound Go structs.
