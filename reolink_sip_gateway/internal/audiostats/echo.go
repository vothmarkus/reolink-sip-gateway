package audiostats

// EchoSnapshot describes a successfully initialized WebRTC processor. Native
// metrics have explicit validity flags: absent statistics are not measured zero.
// It is comparable and contains no captured audio.
type EchoSnapshot struct {
	Available              bool    `json:"available"`
	CaptureFrames          uint64  `json:"capture_frames"`
	RenderFrames           uint64  `json:"render_frames"`
	MissingRenderFrames    uint64  `json:"missing_render_frames"`
	CurrentDelayMS         int     `json:"current_delay_ms"`
	ERLEValid              bool    `json:"erle_valid"`
	ERLEDB                 float64 `json:"erle_db"`
	ResidualEchoValid      bool    `json:"residual_echo_valid"`
	ResidualEchoLikelihood float64 `json:"residual_echo_likelihood"`
}
