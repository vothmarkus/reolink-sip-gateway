package de.vothmarkus.reolinksip;

import android.media.AudioFormat;
import android.media.MediaCodec;
import android.media.MediaCodecInfo;
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
    private final Map<Long, Decoder> decoders = new ConcurrentHashMap<>();

    @Override
    public long startAACDecoder(int sampleRate, int channels) throws Exception {
        if (sampleRate < 8000 || sampleRate > 48000) {
            throw new IllegalArgumentException("Unsupported AAC sample rate " + sampleRate);
        }
        if (channels != 1) {
            throw new IllegalArgumentException("Android alpha currently supports mono AAC only");
        }
        MediaFormat format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_AAC, sampleRate, channels);
        format.setInteger(MediaFormat.KEY_AAC_PROFILE, MediaCodecInfo.CodecProfileLevel.AACObjectLC);
        format.setInteger(MediaFormat.KEY_IS_ADTS, 1);
        format.setInteger(MediaFormat.KEY_PCM_ENCODING, AudioFormat.ENCODING_PCM_16BIT);
        MediaCodec codec = MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_AUDIO_AAC);
        codec.configure(format, null, null, 0);
        codec.start();
        long handle = nextHandle.getAndIncrement();
        decoders.put(handle, new Decoder(codec, sampleRate));
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

    void closeAll() {
        for (Long handle : decoders.keySet()) {
            stopAACDecoder(handle);
        }
    }

    private static final class Decoder {
        private final MediaCodec codec;
        private final int sampleRate;
        private long frameIndex;

        Decoder(MediaCodec codec, int sampleRate) {
            this.codec = codec;
            this.sampleRate = sampleRate;
        }

        synchronized byte[] decode(byte[] adts) throws Exception {
            int inputIndex = codec.dequeueInputBuffer(IO_TIMEOUT_US);
            if (inputIndex < 0) {
                throw new IllegalStateException("AAC decoder has no input buffer");
            }
            ByteBuffer input = codec.getInputBuffer(inputIndex);
            if (input == null || input.capacity() < adts.length) {
                throw new IllegalStateException("AAC decoder input buffer too small");
            }
            input.clear();
            input.put(adts);
            long ptsUs = frameIndex * 1_024_000_000L / sampleRate;
            frameIndex++;
            codec.queueInputBuffer(inputIndex, 0, adts.length, ptsUs, 0);

            ByteArrayOutputStream out = new ByteArrayOutputStream();
            MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
            boolean first = true;
            while (true) {
                int outputIndex = codec.dequeueOutputBuffer(info, first ? IO_TIMEOUT_US : 0);
                first = false;
                if (outputIndex >= 0) {
                    ByteBuffer output = codec.getOutputBuffer(outputIndex);
                    if (output != null && info.size > 0) {
                        output.position(info.offset);
                        output.limit(info.offset + info.size);
                        byte[] chunk = new byte[info.size];
                        output.get(chunk);
                        out.write(chunk);
                    }
                    codec.releaseOutputBuffer(outputIndex, false);
                    continue;
                }
                if (outputIndex == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                    MediaFormat fmt = codec.getOutputFormat();
                    int channels = fmt.getInteger(MediaFormat.KEY_CHANNEL_COUNT);
                    if (channels != 1) {
                        throw new IllegalStateException("AAC decoder changed to " + channels + " channels");
                    }
                    if (fmt.containsKey(MediaFormat.KEY_PCM_ENCODING)
                            && fmt.getInteger(MediaFormat.KEY_PCM_ENCODING) != AudioFormat.ENCODING_PCM_16BIT) {
                        throw new IllegalStateException("AAC decoder did not provide PCM 16-bit");
                    }
                    continue;
                }
                break;
            }
            return out.toByteArray();
        }

        synchronized void close() {
            try {
                codec.stop();
            } catch (Exception ignored) {
            }
            codec.release();
        }
    }
}
