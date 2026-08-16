# Third-party notices

## ReolinkProxy

Parts of `internal/baichuan` and `internal/codec/adpcm.go` are adapted from:

- Project: ReolinkProxy
- Author: Roman Kredentser / Shareed2k
- Source: https://github.com/Shareed2k/reolinkproxy
- License: MIT

The adapted code is intentionally limited to the TCP Baichuan login/preview/talkback paths and the IMA ADPCM codec needed by this gateway. The preview parser and stream request structures are adapted for receiving Reolink bcmedia audio directly on port 9000.

MIT License

Copyright (c) 2026 Roman Kredentser

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Optional WebRTC acoustic echo cancellation runtime

When `echo_cancellation_enabled` is active, the add-on launches the local
`reolink-aec-helper`, compiled against Debian's `webrtc-audio-processing-1`
development package and dynamically linked to `libwebrtc-audio-processing-1-3`.
The library is an unmodified Debian runtime package; its copyright/license files
remain available under `/usr/share/doc` in the container image. The helper only
adapts this gateway's fixed 10 ms PCM protocol to the public AudioProcessing API.
