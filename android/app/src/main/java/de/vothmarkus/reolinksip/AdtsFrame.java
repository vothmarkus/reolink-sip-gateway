package de.vothmarkus.reolinksip;

/** One complete AAC-LC access unit from a Baichuan audio packet. */
final class AdtsFrame {
    private static final int[] RATES = {
            96000, 88200, 64000, 48000, 44100, 32000, 24000,
            22050, 16000, 12000, 11025, 8000, 7350
    };
    final int offset;
    final int headerSize;
    final int frameSize;
    final int sampleRate;
    final int channels;
    private final int frequencyIndex;
    private final int objectType;

    private AdtsFrame(byte[] data, int offset) {
        this.offset = offset;
        if (offset < 0 || data.length - offset < 7
                || (data[offset] & 0xff) != 0xff || (data[offset + 1] & 0xf6) != 0xf0) {
            throw new IllegalArgumentException("Invalid or incomplete AAC ADTS header");
        }
        objectType = ((data[offset + 2] & 0xc0) >> 6) + 1;
        frequencyIndex = (data[offset + 2] >> 2) & 0x0f;
        channels = ((data[offset + 2] & 1) << 2) | ((data[offset + 3] >> 6) & 3);
        if (objectType != 2 || frequencyIndex >= RATES.length || channels != 1) {
            throw new IllegalArgumentException("Camera audio must be AAC-LC mono with a valid sample rate");
        }
        sampleRate = RATES[frequencyIndex];
        headerSize = (data[offset + 1] & 1) == 0 ? 9 : 7;
        frameSize = ((data[offset + 3] & 3) << 11)
                | ((data[offset + 4] & 0xff) << 3) | ((data[offset + 5] & 0xe0) >> 5);
        if (frameSize <= headerSize || frameSize > data.length - offset) {
            throw new IllegalArgumentException("Invalid or incomplete AAC ADTS frame: " + frameSize + " bytes");
        }
        if ((data[offset + 6] & 3) != 0) {
            throw new IllegalArgumentException("Multiple raw blocks in one ADTS frame are unsupported");
        }
    }

    static AdtsFrame parse(byte[] data, int offset) {
        return new AdtsFrame(data, offset);
    }

    byte[] audioSpecificConfig() {
        int config = (objectType << 11) | (frequencyIndex << 7) | (channels << 3);
        return new byte[]{(byte) (config >> 8), (byte) config};
    }
}
