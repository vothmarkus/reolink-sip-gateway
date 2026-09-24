package media

import (
	"math"

	"github.com/vothmarkus/reolink-sip-gateway/internal/audiostats"
)

func (s *Session) EchoStats() audiostats.EchoSnapshot {
	s.echoMu.RLock()
	echo := s.echo
	s.echoMu.RUnlock()
	if echo == nil {
		return audiostats.EchoSnapshot{}
	}
	return publicEchoStats(echo.Stats())
}

func publicEchoStats(st echoStats) audiostats.EchoSnapshot {
	result := audiostats.EchoSnapshot{
		Available: true, CaptureFrames: st.CaptureFrames, RenderFrames: st.RenderFrames,
		MissingRenderFrames: st.MissingRenderFrames, CurrentDelayMS: st.CurrentDelayMS,
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	// Initialization processes silence, which is not a live echo measurement.
	// Preserve native validity and expose only JSON-finite values.
	if st.CaptureFrames > 0 && st.Native.has(nativeStatERLE) && finite(st.Native.EchoReturnLossEnhancementDB) {
		result.ERLEValid = true
		result.ERLEDB = st.Native.EchoReturnLossEnhancementDB
	}
	if st.CaptureFrames > 0 && st.Native.has(nativeStatResidualEchoLikelihood) && finite(st.Native.ResidualEchoLikelihood) {
		result.ResidualEchoValid = true
		result.ResidualEchoLikelihood = st.Native.ResidualEchoLikelihood
	}
	return result
}
