#include <jni.h>
#include "processor.h"

// NativeEchoProcessor serializes process/close on the same Java monitor.
// Only that private wrapper owns the pointer; Go receives a separate map ID.
namespace {
void Fail(JNIEnv* env, const char* message) {
  env->ThrowNew(env->FindClass("java/lang/IllegalStateException"), message);
}
}

extern "C" JNIEXPORT jlong JNICALL
Java_de_vothmarkus_reolinksip_NativeEchoProcessor_create(
    JNIEnv* env, jclass, jboolean high_pass, jboolean noise_suppression) {
  try {
    return reinterpret_cast<jlong>(new reolink_aec::Processor(high_pass, noise_suppression, "moderate"));
  } catch (const std::exception& e) {
    Fail(env, e.what());
    return 0;
  }
}

extern "C" JNIEXPORT jbyteArray JNICALL
Java_de_vothmarkus_reolinksip_NativeEchoProcessor_processFrame(
    JNIEnv* env, jclass, jlong handle, jbyteArray input) {
  if (!handle || !input || env->GetArrayLength(input) != reolink_aec::kRequestBytes) {
    Fail(env, "Invalid WebRTC AEC frame or closed processor");
    return nullptr;
  }
  std::array<uint8_t, reolink_aec::kRequestBytes> request{};
  std::array<uint8_t, reolink_aec::kReplyBytes> reply{};
  env->GetByteArrayRegion(input, 0, request.size(), reinterpret_cast<jbyte*>(request.data()));
  if (env->ExceptionCheck()) return nullptr;
  try {
    reinterpret_cast<reolink_aec::Processor*>(handle)->Process(request, reply);
  } catch (const std::exception& e) {
    Fail(env, e.what());
    return nullptr;
  }
  jbyteArray output = env->NewByteArray(reply.size());
  if (output) env->SetByteArrayRegion(output, 0, reply.size(), reinterpret_cast<jbyte*>(reply.data()));
  return output;
}

extern "C" JNIEXPORT void JNICALL
Java_de_vothmarkus_reolinksip_NativeEchoProcessor_destroy(JNIEnv*, jclass, jlong handle) {
  delete reinterpret_cast<reolink_aec::Processor*>(handle);
}
