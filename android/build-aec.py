#!/usr/bin/env python3
"""Build pinned WebRTC APM in-process for Android; --host-smoke builds the helper.

Requires Meson >=0.63, Ninja, and ANDROID_NDK_HOME (Android only). Abseil's
version, source and Meson patch hashes are pinned by the upstream wrap file.
"""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile

ROOT = Path(__file__).resolve().parent.parent
WORK = ROOT / "android" / ".native-build"
VERSION = "1.3"
ARCHIVE_SHA = "2365e93e778d7b61b5d6e02d21c47d97222e9c7deff9e1d0838ad6ec2e86f1b9"
HOST = "--host-smoke" in sys.argv


def run(*args):
    subprocess.run([str(a) for a in args], check=True)


def main():
    WORK.mkdir(parents=True, exist_ok=True)
    archive = WORK / f"webrtc-audio-processing-{VERSION}.tar.xz"
    if not archive.exists():
        run("curl", "--fail", "--location", "--retry", "3", "--output", archive,
            f"https://freedesktop.org/software/pulseaudio/webrtc-audio-processing/{archive.name}")
    if hashlib.sha256(archive.read_bytes()).hexdigest() != ARCHIVE_SHA:
        raise ValueError("WebRTC source archive checksum mismatch")
    source = WORK / f"webrtc-audio-processing-{VERSION}"
    if not source.exists():
        with tarfile.open(archive) as tar:
            # The hash above fixes the complete reviewed source archive.
            tar.extractall(WORK, filter="data")
    meson_file = source / "meson.build"
    marker = "# Reolink shared processor targets"
    text = meson_file.read_text().split(marker)[0]
    helper_dir = ROOT / "reolink_sip_gateway/native/aec-helper"
    if HOST:
        target, cpp = "reolink-aec-helper", helper_dir / "main.cc"
        text += f"\n{marker}\nexecutable('{target}', '{cpp}', dependencies: audio_processing_dep, cpp_args: common_cxxflags)\n"
    else:
        target, cpp = "reolink_apm", ROOT / "android/native/aec_jni.cc"
        text += f"\n{marker}\nshared_library('{target}', '{cpp}', include_directories: include_directories('{helper_dir}'), dependencies: audio_processing_dep, cpp_args: common_cxxflags, link_args: ['-static-libstdc++', '-Wl,-z,max-page-size=16384', '-Wl,-z,common-page-size=16384'])\n"
    meson_file.write_text(text)
    targets = [("host", "", "", "")] if HOST else [
        ("arm64-v8a", "aarch64-linux-android", "aarch64", "aarch64"),
        ("armeabi-v7a", "armv7a-linux-androideabi", "arm", "armv7"),
    ]
    for abi, triple, family, cpu in targets:
        build = WORK / abi
        args = ["meson", "setup", str(build), str(source), "--buildtype=release",
                "--wrap-mode=forcefallback", "-Ddefault_library=static", "-Db_staticpic=true"]
        if not HOST:
            ndk = Path(os.environ["ANDROID_NDK_HOME"])
            clang = ndk / "toolchains/llvm/prebuilt/linux-x86_64/bin"
            cross = WORK / f"{abi}.ini"
            cross.write_text(f"""[binaries]
c = '{clang}/{triple}26-clang'
cpp = '{clang}/{triple}26-clang++'
ar = '{clang}/llvm-ar'
strip = '{clang}/llvm-strip'
[host_machine]
system = 'android'
cpu_family = '{family}'
cpu = '{cpu}'
endian = 'little'
[properties]
needs_exe_wrapper = true
""")
            args += ["--cross-file", str(cross), "-Dgnustl=disabled", "-Dneon=auto"]
        if (build / "build.ninja").exists():
            args += ["--reconfigure"]
        run(*args)
        run("meson", "compile", "-C", build, "-j", "2", target)
        if HOST:
            run(sys.executable, ROOT / "scripts/smoke-aec.py", build / target)
        else:
            output = ROOT / "android/app/src/main/jniLibs" / abi
            output.mkdir(parents=True, exist_ok=True)
            shutil.copy2(build / f"lib{target}.so", output)
            run(clang / "llvm-strip", "--strip-unneeded", output / f"lib{target}.so")
    if not HOST:
        # Redistribute upstream notices for all bundled WebRTC/Abseil components.
        notices = ROOT / "android/app/src/main/assets/webrtc-notices.txt"
        notices.parent.mkdir(parents=True, exist_ok=True)
        files = sorted(p for p in source.rglob("*") if p.is_file() and p.name in {"COPYING", "LICENSE", "NOTICE", "AUTHORS"})
        notices.write_text("WebRTC Audio Processing 1.3 and Abseil 20230125.1\n\n" +
                           "\n\n".join(f"{p.relative_to(source)}\n\n{p.read_text(errors='replace')}" for p in files))


if __name__ == "__main__":
    main()
