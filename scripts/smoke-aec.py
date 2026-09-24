#!/usr/bin/env python3
"""Exercise the native AEC wire protocol (not a hardware performance benchmark)."""
import math
import random
import struct
import subprocess
import sys

command = sys.argv[1:] or ["/usr/bin/reolink-aec-helper"]
frames = 100
request = b"AEC1" + bytes(2 * 80 * 2)
result = subprocess.run(command, input=request * frames, capture_output=True, timeout=60)
if result.returncode:
    sys.stderr.buffer.write(result.stderr)
    raise SystemExit(f"AEC helper exited with {result.returncode}")
reply_bytes = 224
if len(result.stdout) != frames * reply_bytes:
    raise SystemExit(f"Unexpected AEC reply length: {len(result.stdout)}")
for offset in range(0, len(result.stdout), reply_bytes):
    magic, status = struct.unpack_from("<4si", result.stdout, offset)
    if magic != b"AER1" or status != 0:
        raise SystemExit(f"Invalid AEC response at frame {offset // reply_bytes}: {magic!r}, {status}")
print(f"AEC smoke test passed: {frames} frames")

# A successful status code on silence does not prove echo cancellation works.
# Exercise the shared Android/Linux processor with a reproducible, short echo
# path, followed by independent near-end speech-like noise with no render input.
# This models the residual path AFTER Go's measured coarse-delay alignment.
rng = random.Random(726)
rate = 8000
render, capture = [], []
smooth = near = 0.0
for i in range(10 * rate):
    smooth = .65 * smooth + .35 * rng.uniform(-12000, 12000)
    render.append(round(smooth) if i < 8 * rate else 0)
    near = .65 * near + .35 * rng.uniform(-9000, 9000)
    echo = (.55 * render[i - 240] if i >= 240 else 0) + (.2 * render[i - 352] if i >= 352 else 0)
    capture.append(round(echo + (near if i >= 8 * rate else 0)))
request = b"".join(b"AEC1" + struct.pack("<160h", *(render[i:i + 80] + capture[i:i + 80]))
                   for i in range(0, len(render), 80))
# Disable HPF/NS here so an unrelated filter cannot pass the echo test.
result = subprocess.run(command + ["--high-pass=false", "--noise-suppression=false"],
                        input=request, capture_output=True, timeout=60)
if result.returncode or len(result.stdout) != 1000 * reply_bytes:
    sys.stderr.buffer.write(result.stderr)
    raise SystemExit("AEC signal test failed to process 1000 frames")
output = []
for offset in range(0, len(result.stdout), reply_bytes):
    magic, status = struct.unpack_from("<4si", result.stdout, offset)
    if magic != b"AER1" or status != 0:
        raise SystemExit(f"AEC signal processing failed at frame {offset // reply_bytes}")
    output.extend(struct.unpack_from("<80h", result.stdout, offset + 64))


def energy(samples):
    return sum(v * v for v in samples)


reduction = 10 * math.log10(energy(capture[5 * rate:8 * rate]) / max(1, energy(output[5 * rate:8 * rate])))
near_ratio = energy(output[9 * rate:]) / energy(capture[9 * rate:])
if reduction < 6:
    raise SystemExit(f"AEC did not reduce the synthetic echo sufficiently: {reduction:.1f} dB")
if not .2 < near_ratio < 4:
    raise SystemExit(f"AEC damaged near-end audio without a render signal: energy ratio {near_ratio:.3f}")
print(f"AEC signal test passed: synthetic echo reduction {reduction:.1f} dB; near-end energy ratio {near_ratio:.3f}")
