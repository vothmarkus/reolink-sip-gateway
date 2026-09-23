package de.vothmarkus.reolinksip;

import android.media.AudioFormat;
import android.media.MediaCodec;
import android.media.MediaFormat;

import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;

import de.vothmarkus.reolinksip.core.mobilebridge.PlatformAudio;

final class MediaCodecAudioAdapter implements PlatformAudio {
    private static final long IO_TIMEOUT_US = 20_000;
    private final AtomicLong nextHandle = new AtomicLong(1);
    private final Map<Long, NativeEchoProcessor> echoProcessors = new ConcurrentHashMap<>();
    private final Map<Long, Decoder> decoders = new ConcurrentHashMap<>();

    @Override
    public long startAACDecoder(int sampleRate, int channels) throws Exception {
        if (sampleRate < 8000 || sampleRate > 48000) {
            throw new IllegalArgumentException("Unsupported AAC sample rate " + sampleRate);
        }
        if (channels != 1) {
            throw new IllegalArgumentException("Android alpha currently supports mono AAC only");
        }
        long handle = nextHandle.getAndIncrement();
        // The first ADTS frame supplies the exact AudioSpecificConfig. Do not
        // assume KEY_AAC_PROFILE (an encoder option) configures a decoder.
        decoders.put(handle, new Decoder(sampleRate, channels));
        return handle;
    }

    @Override
    public byte[] decodeAAC(long handle, byte[] adts) throws Exception {
        Decoder decoder = decoders.get(handle);
        if (decoder == null) {
            throw new IllegalStateException("Unknown AAC decoder handle " + handle);
        }
        return decoder.decode(adts);
    }

    @Override
    public void stopAACDecoder(long handle) {
        Decoder decoder = decoders.remove(handle);
        if (decoder != null) {
            decoder.close();
        }
    }

    @Override public long startAEC(boolean highPass, boolean noiseSuppression) throws Exception {
        try {
            NativeEchoProcessor processor = new NativeEchoProcessor(highPass, noiseSuppression);
            long handle = nextHandle.getAndIncrement();
            echoProcessors.put(handle, processor);
            return handle;
        } catch (LinkageError e) {
            throw new IllegalStateException("WebRTC-Bibliothek konnte nicht geladen werden", e);
        }
    }
    @Override public byte[] processAEC(long handle, byte[] request) {
        NativeEchoProcessor processor = echoProcessors.get(handle);
        if (processor == null) throw new IllegalStateException("Unknown WebRTC AEC handle");
        return processor.process(request);
    }
    @Override public void stopAEC(long handle) {
        NativeEchoProcessor processor = echoProcessors.remove(handle);
        if (processor != null) processor.close();
    }
    void closeAll() {
        for (Long handle : echoProcessors.keySet()) stopAEC(handle);
        for (Long handle : decoders.keySet()) {
            stopAACDecoder(handle);
        }
    }

    private static final class Decoder {
        private MediaCodec codec;
        private final int sampleRate;
        private final int channels;
        private long frameIndex;
        private boolean closed;

        Decoder(int sampleRate, int channels) {
            this.sampleRate = sampleRate;
            this.channels = channels;
        }

        private void configure(AdtsFrame frame) throws Exception {
            MediaFormat format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_AAC, sampleRate, channels);
            format.setByteBuffer("csd-0", ByteBuffer.wrap(frame.audioSpecificConfig()));
            format.setInteger(MediaFormat.KEY_PCM_ENCODING, AudioFormat.ENCODING_PCM_16BIT);
            MediaCodec created = MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_AUDIO_AAC);
            try {
                created.configure(format, null, null, 0);
                created.start();
                codec = created;
            } catch (Exception e) {
                created.release();
                throw e;
            }
        }

        synchronized byte[] decode(byte[] adts) throws Exception {
            if (closed) throw new IllegalStateException("AAC decoder is closed");
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            for (int offset = 0; offset < adts.length;) {
                AdtsFrame frame = AdtsFrame.parse(adts, offset);
                if (frame.sampleRate != sampleRate || frame.channels != channels) {
                    throw new IllegalArgumentException("AAC input format changed during the call");
                }
                if (codec == null) configure(frame);
                // Release pending output before requesting another input slot.
                drain(out, 0);
                int inputIndex = codec.dequeueInputBuffer(IO_TIMEOUT_US);
                if (inputIndex < 0) {
                    drain(out, IO_TIMEOUT_US);
                    inputIndex = codec.dequeueInputBuffer(IO_TIMEOUT_US);
                }
                if (inputIndex < 0) throw new IllegalStateException("AAC decoder has no input buffer");
                int size = frame.frameSize - frame.headerSize;
                ByteBuffer input = codec.getInputBuffer(inputIndex);
                if (input == null || input.capacity() < size) {
                    throw new IllegalStateException("AAC decoder input buffer too small");
                }
                input.clear();
                // Supply one raw AAC access unit per buffer. ADTS transport
                // headers (including optional CRC) are not codec payload.
                input.put(adts, offset + frame.headerSize, size);
                long ptsUs = frameIndex * 1_024_000_000L / sampleRate;
                frameIndex++;
                codec.queueInputBuffer(inputIndex, 0, size, ptsUs, 0);
                drain(out, IO_TIMEOUT_US);
                offset += frame.frameSize;
            }
            return out.toByteArray();
        }

        private void drain(ByteArrayOutputStream out, long timeoutUs) throws Exception {
            MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
            while (true) {
                int outputIndex = codec.dequeueOutputBuffer(info, timeoutUs);
                if (outputIndex >= 0) {
                    try {
                        ByteBuffer output = codec.getOutputBuffer(outputIndex);
                        if (info.size > 0 && (info.flags & MediaCodec.BUFFER_FLAG_CODEC_CONFIG) == 0) {
                            if (output == null) throw new IllegalStateException("AAC decoder returned no PCM buffer");
                            output.position(info.offset);
                            output.limit(info.offset + info.size);
                            byte[] chunk = new byte[info.size];
                            output.get(chunk);
                            out.write(chunk);
                        }
                    } finally {
                        codec.releaseOutputBuffer(outputIndex, false);
                    }
                    timeoutUs = 0;
                    continue;
                }
                if (outputIndex == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                    MediaFormat fmt = codec.getOutputFormat();
                    if (fmt.getInteger(MediaFormat.KEY_CHANNEL_COUNT) != channels
                            || fmt.getInteger(MediaFormat.KEY_SAMPLE_RATE) != sampleRate) {
                        throw new IllegalStateException("Unexpected AAC decoder PCM format: " + fmt);
                    }
                    if (fmt.containsKey(MediaFormat.KEY_PCM_ENCODING)
                            && fmt.getInteger(MediaFormat.KEY_PCM_ENCODING) != AudioFormat.ENCODING_PCM_16BIT) {
                        throw new IllegalStateException("AAC decoder did not provide PCM 16-bit");
                    }
                    continue;
                }
                if (outputIndex == MediaCodec.INFO_OUTPUT_BUFFERS_CHANGED) continue;
                return;
            }
        }

        synchronized void close() {
            if (closed) return;
            closed = true;
            if (codec == null) return;
            try {
                codec.stop();
            } catch (Exception ignored) {
            } finally {
                codec.release();
                codec = null;
            }
        }
    }
}
