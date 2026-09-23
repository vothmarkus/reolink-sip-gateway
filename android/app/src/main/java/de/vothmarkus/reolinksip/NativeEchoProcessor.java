package de.vothmarkus.reolinksip;

/** In-process WebRTC APM for camera PCM, independent of the Android microphone. */
final class NativeEchoProcessor {
    static { System.loadLibrary("reolink_apm"); }
    private long pointer;

    NativeEchoProcessor(boolean highPass, boolean noiseSuppression) {
        pointer = create(highPass, noiseSuppression);
        if (pointer == 0) throw new IllegalStateException("WebRTC AEC konnte nicht gestartet werden");
    }

    synchronized byte[] process(byte[] request) {
        if (pointer == 0) throw new IllegalStateException("WebRTC AEC wurde beendet");
        return processFrame(pointer, request);
    }

    synchronized void close() {
        if (pointer != 0) { destroy(pointer); pointer = 0; }
    }

    private static native long create(boolean highPass, boolean noiseSuppression);
    private static native byte[] processFrame(long pointer, byte[] request);
    private static native void destroy(long pointer);
}
