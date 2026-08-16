#include <array>
#include <cerrno>
#include <csignal>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <memory>
#include <string>
#include <unistd.h>

#include "modules/audio_processing/include/audio_processing.h"

namespace {
constexpr int kSampleRate = 8000;
constexpr size_t kFrameSamples = 80;
constexpr size_t kFrameBytes = kFrameSamples * sizeof(int16_t);
constexpr size_t kRequestBytes = 4 + 2 * kFrameBytes;
constexpr size_t kReplyBytes = 4 + 4 + 4 + 5 * 8 + 3 * 4 + kFrameBytes;
static_assert(kRequestBytes == 324, "wire request size changed");
static_assert(kReplyBytes == 224, "wire reply size changed");

constexpr uint32_t kStatERL = 1u << 0;
constexpr uint32_t kStatERLE = 1u << 1;
constexpr uint32_t kStatDivergent = 1u << 2;
constexpr uint32_t kStatResidual = 1u << 3;
constexpr uint32_t kStatResidualRecent = 1u << 4;
constexpr uint32_t kStatDelay = 1u << 5;
constexpr uint32_t kStatDelayMedian = 1u << 6;
constexpr uint32_t kStatDelayStdDev = 1u << 7;

bool ReadExact(int fd, uint8_t* dst, size_t size) {
  size_t done = 0;
  while (done < size) {
    ssize_t n = ::read(fd, dst + done, size - done);
    if (n == 0) return false;
    if (n < 0) {
      if (errno == EINTR) continue;
      return false;
    }
    done += static_cast<size_t>(n);
  }
  return true;
}

bool WriteExact(int fd, const uint8_t* src, size_t size) {
  size_t done = 0;
  while (done < size) {
    ssize_t n = ::write(fd, src + done, size - done);
    if (n < 0) {
      if (errno == EINTR) continue;
      return false;
    }
    if (n == 0) return false;
    done += static_cast<size_t>(n);
  }
  return true;
}

uint16_t GetU16(const uint8_t* p) {
  return static_cast<uint16_t>(p[0]) |
         (static_cast<uint16_t>(p[1]) << 8);
}

void PutU16(uint8_t* p, uint16_t v) {
  p[0] = static_cast<uint8_t>(v);
  p[1] = static_cast<uint8_t>(v >> 8);
}

void PutU32(uint8_t* p, uint32_t v) {
  p[0] = static_cast<uint8_t>(v);
  p[1] = static_cast<uint8_t>(v >> 8);
  p[2] = static_cast<uint8_t>(v >> 16);
  p[3] = static_cast<uint8_t>(v >> 24);
}

void PutI32(uint8_t* p, int32_t v) { PutU32(p, static_cast<uint32_t>(v)); }

void PutF64(uint8_t* p, double v) {
  static_assert(sizeof(double) == sizeof(uint64_t), "unexpected double size");
  uint64_t bits = 0;
  std::memcpy(&bits, &v, sizeof(bits));
  for (int i = 0; i < 8; ++i) p[i] = static_cast<uint8_t>(bits >> (8 * i));
}

bool BoolArg(const std::string& value, bool fallback) {
  if (value == "1" || value == "true") return true;
  if (value == "0" || value == "false") return false;
  return fallback;
}

std::string ValueAfter(const std::string& arg, const char* prefix) {
  const std::string p(prefix);
  if (arg.rfind(p, 0) != 0) return {};
  return arg.substr(p.size());
}

webrtc::AudioProcessing::Config::NoiseSuppression::Level NoiseLevel(
    const std::string& level) {
  using NS = webrtc::AudioProcessing::Config::NoiseSuppression;
  if (level == "low") return NS::kLow;
  if (level == "high") return NS::kHigh;
  if (level == "very-high") return NS::kVeryHigh;
  return NS::kModerate;
}

struct WireStats {
  uint32_t mask = 0;
  double erl = 0;
  double erle = 0;
  double divergent = 0;
  double residual = 0;
  double residual_recent = 0;
  int32_t delay = 0;
  int32_t delay_median = 0;
  int32_t delay_stddev = 0;
};

WireStats MakeWireStats(const webrtc::AudioProcessingStats& stats) {
  WireStats out;
  if (stats.echo_return_loss) {
    out.mask |= kStatERL;
    out.erl = *stats.echo_return_loss;
  }
  if (stats.echo_return_loss_enhancement) {
    out.mask |= kStatERLE;
    out.erle = *stats.echo_return_loss_enhancement;
  }
  if (stats.divergent_filter_fraction) {
    out.mask |= kStatDivergent;
    out.divergent = *stats.divergent_filter_fraction;
  }
  if (stats.residual_echo_likelihood) {
    out.mask |= kStatResidual;
    out.residual = *stats.residual_echo_likelihood;
  }
  if (stats.residual_echo_likelihood_recent_max) {
    out.mask |= kStatResidualRecent;
    out.residual_recent = *stats.residual_echo_likelihood_recent_max;
  }
  if (stats.delay_ms) {
    out.mask |= kStatDelay;
    out.delay = *stats.delay_ms;
  }
  if (stats.delay_median_ms) {
    out.mask |= kStatDelayMedian;
    out.delay_median = *stats.delay_median_ms;
  }
  if (stats.delay_standard_deviation_ms) {
    out.mask |= kStatDelayStdDev;
    out.delay_stddev = *stats.delay_standard_deviation_ms;
  }
  return out;
}

void EncodeReply(int32_t status, const WireStats& stats,
                 const std::array<int16_t, kFrameSamples>& pcm,
                 std::array<uint8_t, kReplyBytes>* reply) {
  auto& out = *reply;
  out.fill(0);
  std::memcpy(out.data(), "AER1", 4);
  size_t off = 4;
  PutI32(out.data() + off, status);
  off += 4;
  PutU32(out.data() + off, stats.mask);
  off += 4;
  for (double v : {stats.erl, stats.erle, stats.divergent, stats.residual,
                   stats.residual_recent}) {
    PutF64(out.data() + off, v);
    off += 8;
  }
  for (int32_t v : {stats.delay, stats.delay_median, stats.delay_stddev}) {
    PutI32(out.data() + off, v);
    off += 4;
  }
  for (int16_t v : pcm) {
    PutU16(out.data() + off, static_cast<uint16_t>(v));
    off += 2;
  }
}
}  // namespace

int main(int argc, char** argv) {
  // A parent-side shutdown can close stdout while one final reply is being
  // written. Handle that as a normal WriteExact() failure instead of letting
  // SIGPIPE terminate the helper before stderr/process cleanup can complete.
  std::signal(SIGPIPE, SIG_IGN);

  bool high_pass = true;
  bool noise_suppression = true;
  std::string noise_level = "moderate";
  for (int i = 1; i < argc; ++i) {
    std::string arg(argv[i]);
    if (auto v = ValueAfter(arg, "--high-pass="); !v.empty()) {
      high_pass = BoolArg(v, high_pass);
    } else if (auto v = ValueAfter(arg, "--noise-suppression="); !v.empty()) {
      noise_suppression = BoolArg(v, noise_suppression);
    } else if (auto v = ValueAfter(arg, "--noise-level="); !v.empty()) {
      noise_level = v;
    }
  }

  std::unique_ptr<webrtc::AudioProcessing> apm(
      webrtc::AudioProcessingBuilder().Create());
  if (!apm) {
    std::cerr << "AudioProcessingBuilder::Create failed\n";
    return 2;
  }
  webrtc::AudioProcessing::Config config;
  config.echo_canceller.enabled = true;
  config.echo_canceller.mobile_mode = false;
  config.echo_canceller.enforce_high_pass_filtering = high_pass;
  config.high_pass_filter.enabled = high_pass;
  config.noise_suppression.enabled = noise_suppression;
  config.noise_suppression.level = NoiseLevel(noise_level);
  // Keep diagnostics available and make unrelated speech-level processing
  // explicit: AEC + optional HPF/NS only, no AGC and no VAD.
  config.residual_echo_detector.enabled = true;
  config.gain_controller1.enabled = false;
  config.gain_controller2.enabled = false;
  config.voice_detection.enabled = false;
  apm->ApplyConfig(config);
  if (const int init = apm->Initialize(); init != webrtc::AudioProcessing::kNoError) {
    std::cerr << "AudioProcessing::Initialize failed status=" << init << "\n";
    return 3;
  }

  const webrtc::StreamConfig stream(kSampleRate, 1);
  std::array<uint8_t, kRequestBytes> request{};
  std::array<uint8_t, kReplyBytes> reply{};
  std::array<int16_t, kFrameSamples> render{};
  std::array<int16_t, kFrameSamples> render_out{};
  std::array<int16_t, kFrameSamples> capture{};
  std::array<int16_t, kFrameSamples> processed{};

  while (ReadExact(STDIN_FILENO, request.data(), request.size())) {
    if (std::memcmp(request.data(), "AEC1", 4) != 0) {
      std::cerr << "invalid request magic\n";
      return 4;
    }
    size_t off = 4;
    for (size_t i = 0; i < kFrameSamples; ++i, off += 2) {
      render[i] = static_cast<int16_t>(GetU16(request.data() + off));
    }
    for (size_t i = 0; i < kFrameSamples; ++i, off += 2) {
      capture[i] = static_cast<int16_t>(GetU16(request.data() + off));
    }

    render_out = render;
    int status = apm->ProcessReverseStream(render.data(), stream, stream,
                                           render_out.data());
    if (status == webrtc::AudioProcessing::kNoError) {
      // Go has already selected the historic render frame corresponding to this
      // capture frame. The helper therefore sees only the short residual
      // acoustic path; adding the original ~1.45 s again would double-count it.
      status = apm->set_stream_delay_ms(0);
    }
    processed = capture;
    if (status == webrtc::AudioProcessing::kNoError) {
      status = apm->ProcessStream(capture.data(), stream, stream,
                                  processed.data());
    }
    const WireStats stats = MakeWireStats(apm->GetStatistics());
    EncodeReply(status, stats, processed, &reply);
    if (!WriteExact(STDOUT_FILENO, reply.data(), reply.size())) return 0;
  }
  return 0;
}
