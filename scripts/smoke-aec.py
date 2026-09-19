#!/usr/bin/env python3
"""Exercise the native AEC wire protocol (not a hardware performance benchmark)."""
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
