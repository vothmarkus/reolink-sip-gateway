#include <cerrno>
#include <csignal>
#include <iostream>
#include <unistd.h>
#include "processor.h"

namespace {
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

}

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

  try {
    reolink_aec::Processor processor(high_pass, noise_suppression, noise_level);
    std::array<uint8_t, reolink_aec::kRequestBytes> request{};
    std::array<uint8_t, reolink_aec::kReplyBytes> reply{};
    while (ReadExact(STDIN_FILENO, request.data(), request.size())) {
      processor.Process(request, reply);
      if (!WriteExact(STDOUT_FILENO, reply.data(), reply.size())) return 0;
    }
  } catch (const std::exception& e) {
    std::cerr << e.what() << "\n";
    return 2;
  }
  return 0;
}
