package de.vothmarkus.reolinksip;

import org.junit.Test;
import java.util.Arrays;
import static org.junit.Assert.*;

public class AdtsFrameTest {
    private static byte[] frame(boolean crc, int frequencyIndex) {
        int header = crc ? 9 : 7;
        byte[] data = new byte[header + 3];
        data[0] = (byte) 0xff;
        data[1] = (byte) (crc ? 0xf0 : 0xf1);
        data[2] = (byte) (0x40 | (frequencyIndex << 2)); // AAC-LC
        data[3] = (byte) (0x40 | (data.length >> 11)); // mono
        data[4] = (byte) (data.length >> 3);
        data[5] = (byte) ((data.length << 5) | 0x1f);
        data[6] = (byte) 0xfc;
        data[header] = 11;
        data[header + 1] = 22;
        data[header + 2] = 33;
        return data;
    }

    @Test public void suppliesDecoderConfigAndSeparatesCRCFromPayload() {
        for (boolean crc : new boolean[]{false, true}) {
            byte[] data = frame(crc, 8);
            AdtsFrame parsed = AdtsFrame.parse(data, 0);
            assertEquals(16000, parsed.sampleRate);
            assertEquals(1, parsed.channels);
            assertArrayEquals(new byte[]{0x14, 0x08}, parsed.audioSpecificConfig());
            assertArrayEquals(new byte[]{11, 22, 33}, Arrays.copyOfRange(data, parsed.headerSize, parsed.frameSize));
        }
        assertArrayEquals(new byte[]{0x15, (byte) 0x88}, AdtsFrame.parse(frame(false, 11), 0).audioSpecificConfig());
    }

    @Test public void identifiesEachAccessUnitInCombinedPacket() {
        byte[] first = frame(false, 8), second = frame(true, 8);
        byte[] packet = Arrays.copyOf(first, first.length + second.length);
        System.arraycopy(second, 0, packet, first.length, second.length);
        AdtsFrame a = AdtsFrame.parse(packet, 0);
        AdtsFrame b = AdtsFrame.parse(packet, a.frameSize);
        assertEquals(first.length, b.offset);
        assertEquals(packet.length, b.offset + b.frameSize);
        assertEquals(9, b.headerSize);
    }

    @Test public void rejectsTruncatedAndUnsupportedInput() {
        byte[] valid = frame(false, 8);
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(Arrays.copyOf(valid, 6), 0));
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(Arrays.copyOf(valid, 9), 0));
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(frame(false, 15), 0));
        byte[] stereo = valid.clone(); stereo[3] = (byte) 0x80;
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(stereo, 0));
        byte[] multiple = valid.clone(); multiple[6] |= 1;
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(multiple, 0));
        byte[] ssr = valid.clone(); ssr[2] = (byte) 0xa0;
        assertThrows(IllegalArgumentException.class, () -> AdtsFrame.parse(ssr, 0));
    }
}
