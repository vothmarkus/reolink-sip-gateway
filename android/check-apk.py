#!/usr/bin/env python3
"""Fail the Android build if native libraries cannot load on 16 KiB devices."""
import struct
import sys
import zipfile


def check_elf(name, data):
    if data[:4] != b"\x7fELF" or data[5] != 1:
        raise ValueError(f"{name}: expected little-endian ELF")
    if data[4] == 2:
        offset = struct.unpack_from("<Q", data, 32)[0]
        size, count = struct.unpack_from("<HH", data, 54)
        layout = "<IIQQQQQQ"
    elif data[4] == 1:
        offset = struct.unpack_from("<I", data, 28)[0]
        size, count = struct.unpack_from("<HH", data, 42)
        layout = "<IIIIIIII"
    else:
        raise ValueError(f"{name}: unsupported ELF class")
    loads = 0
    for i in range(count):
        fields = struct.unpack_from(layout, data, offset + i * size)
        if fields[0] == 1:  # PT_LOAD
            loads += 1
            alignment = fields[-1]
            if alignment < 16384:
                raise ValueError(f"{name}: LOAD segment {i} aligned to {alignment}, expected >=16384")
    if not loads:
        raise ValueError(f"{name}: no loadable segments")
    print(f"{name}: {loads} LOAD segments support 16 KiB alignment")


def main(path):
    with zipfile.ZipFile(path) as apk:
        libraries = [name for name in apk.namelist() if name.startswith("lib/") and name.endswith(".so")]
        required = {f"lib/{abi}/{lib}.so" for abi in ("arm64-v8a", "armeabi-v7a")
                    for lib in ("libgojni", "libreolink_apm")}
        if not required.issubset(libraries):
            raise ValueError("APK is missing an ARM64 or ARMv7 gateway library")
        for name in libraries:
            check_elf(name, apk.read(name))


if __name__ == "__main__":
    main(sys.argv[1])
