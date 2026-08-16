package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	FixedHAPollInterval         = time.Second
	FFmpegBinary                = "/usr/bin/ffmpeg"
	BaichuanReceiveStream       = "sub"
	WebRTCNoiseSuppressionLevel = "moderate"
	DefaultAECInitialDelayMS    = 1450
	DefaultAECSearchWindowMS    = 300
	minimumAECSearchWindowMS    = 50
	maximumAECSearchWindowMS    = 1000
	MaxSupportedAECDelayMS      = 3000
)

type Config struct {
	VisitorEntity                  string `json:"visitor_entity"`
	ReolinkHost                    string `json:"reolink_host"`
	ReolinkRTSPPort                int    `json:"reolink_rtsp_port"`
	ReolinkStreamPath              string `json:"reolink_stream_path"`
	ReolinkUsername                string `json:"reolink_username"`
	ReolinkPassword                string `json:"reolink_password"`
	ReolinkMode                    string `json:"reolink_mode"`
	BaichuanPort                   int    `json:"baichuan_port"`
	NVRChannel                     int    `json:"nvr_channel"`
	EchoCancellationEnabled        bool   `json:"echo_cancellation_enabled"`
	EchoCancellationSearchWindowMS int    `json:"echo_cancellation_search_window_ms"`
	WebRTCHighPassFilterEnabled    bool   `json:"webrtc_high_pass_filter_enabled"`
	WebRTCNoiseSuppressionEnabled  bool   `json:"webrtc_noise_suppression_enabled"`
	SIPRegistrar                   string `json:"sip_registrar"`
	SIPRegistrarPort               int    `json:"sip_registrar_port"`
	SIPUsername                    string `json:"sip_username"`
	SIPPassword                    string `json:"sip_password"`
	SIPDestination                 string `json:"sip_destination"`
	SIPLocalPort                   int    `json:"sip_local_port"`
	SIPDisplayName                 string `json:"sip_display_name"`
	SIPCodecPreference             string `json:"sip_codec_preference"`
	RingTimeoutSeconds             int    `json:"ring_timeout_seconds"`
	MaxCallDurationSeconds         int    `json:"max_call_duration_seconds"`
	DebounceSeconds                int    `json:"debounce_seconds"`
	LogLevel                       string `json:"log_level"`
	DryRun                         bool   `json:"dry_run"`

	// Runtime-only values. They are resolved during startup and never exposed as
	// user options. This keeps the Home Assistant form small while preserving a
	// deterministic media configuration for every call.
	StatusPort          int    `json:"-"`
	ResolvedReolinkMode string `json:"-"`
	AECInitialDelayMS   int    `json:"-"`
	AECMinDelayMS       int    `json:"-"`
	AECMaxDelayMS       int    `json:"-"`
	FFmpegBinaryPath    string `json:"-"` // test/runtime override; never a Home Assistant option
}

func Defaults() Config {
	cfg := Config{
		VisitorEntity:                  "binary_sensor.reolink_video_doorbell_visitor",
		ReolinkHost:                    "192.168.177.50",
		ReolinkUsername:                "admin",
		ReolinkRTSPPort:                554,
		ReolinkStreamPath:              "/Preview_01_sub",
		ReolinkMode:                    "auto",
		BaichuanPort:                   9000,
		NVRChannel:                     1,
		EchoCancellationEnabled:        true,
		EchoCancellationSearchWindowMS: DefaultAECSearchWindowMS,
		WebRTCHighPassFilterEnabled:    true,
		WebRTCNoiseSuppressionEnabled:  true,
		SIPRegistrar:                   "192.168.177.9",
		SIPRegistrarPort:               5060,
		SIPDestination:                 "**610",
		SIPLocalPort:                   5070,
		SIPDisplayName:                 "Haustür",
		SIPCodecPreference:             "pcma",
		RingTimeoutSeconds:             30,
		MaxCallDurationSeconds:         300,
		DebounceSeconds:                3,
		LogLevel:                       "info",
		DryRun:                         true,
		StatusPort:                     18099,
	}
	cfg.SetAECDelay(DefaultAECInitialDelayMS)
	return cfg
}

// legacyOptions contains only aliases that existed before v0.5.0. Unknown
// legacy keys are otherwise intentionally ignored by json.Unmarshal. Keeping
// these aliases here makes an in-place v0.4.x -> v0.5.0 update preserve the
// meaningful mode/channel choices without carrying old controls into runtime.
type legacyOptions struct {
	ConnectionMode  *string `json:"connection_mode"`
	BaichuanChannel *int    `json:"baichuan_channel"`
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	var legacy legacyOptions
	_ = json.Unmarshal(b, &legacy)
	if legacy.ConnectionMode != nil {
		_, newModePresent := raw["reolink_mode"]
		legacyMode := strings.ToLower(strings.TrimSpace(*legacy.ConnectionMode))
		// During the first 0.5.0 start Supervisor may already have inserted the
		// new default `auto`. Preserve a user's former explicit nvr/standalone
		// choice in that case. A genuinely non-default new value always wins.
		if !newModePresent || (cfg.ReolinkMode == "auto" && legacyMode != "auto") {
			cfg.ReolinkMode = legacyMode
		}
	}
	if legacy.BaichuanChannel != nil {
		_, newChannelPresent := raw["nvr_channel"]
		if !newChannelPresent || (cfg.NVRChannel == Defaults().NVRChannel && *legacy.BaichuanChannel != Defaults().NVRChannel) {
			cfg.NVRChannel = *legacy.BaichuanChannel
		}
	}

	cfg.ReolinkStreamPath = strings.TrimSpace(cfg.ReolinkStreamPath)
	if cfg.ReolinkStreamPath != "" && !strings.HasPrefix(cfg.ReolinkStreamPath, "/") {
		cfg.ReolinkStreamPath = "/" + cfg.ReolinkStreamPath
	}
	cfg.ReolinkMode = strings.ToLower(strings.TrimSpace(cfg.ReolinkMode))
	cfg.SIPCodecPreference = strings.ToLower(strings.TrimSpace(cfg.SIPCodecPreference))
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	cfg.StatusPort = 18099
	cfg.SetAECDelay(DefaultAECInitialDelayMS)
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []error
	if !validBinarySensorEntityID(c.VisitorEntity) {
		errs = append(errs, errors.New("visitor_entity must be a binary_sensor entity ID"))
	}
	if err := validateHost("reolink_host", c.ReolinkHost); err != nil {
		errs = append(errs, err)
	}
	if c.ReolinkRTSPPort < 1 || c.ReolinkRTSPPort > 65535 {
		errs = append(errs, errors.New("reolink_rtsp_port must be 1..65535"))
	}
	if c.BaichuanPort < 1 || c.BaichuanPort > 65535 {
		errs = append(errs, errors.New("baichuan_port must be 1..65535"))
	}
	if c.NVRChannel < 0 || c.NVRChannel > 255 {
		errs = append(errs, errors.New("nvr_channel must be 0..255"))
	}
	switch c.ReolinkMode {
	case "auto", "nvr", "standalone":
	default:
		errs = append(errs, errors.New("reolink_mode must be auto, nvr or standalone"))
	}
	if c.EchoCancellationSearchWindowMS < minimumAECSearchWindowMS || c.EchoCancellationSearchWindowMS > maximumAECSearchWindowMS {
		errs = append(errs, fmt.Errorf("echo_cancellation_search_window_ms must be %d..%d", minimumAECSearchWindowMS, maximumAECSearchWindowMS))
	}
	if !c.DryRun {
		if strings.TrimSpace(c.ReolinkUsername) == "" {
			errs = append(errs, errors.New("reolink_username is required unless dry_run is enabled"))
		}
		if strings.TrimSpace(c.ReolinkPassword) == "" {
			errs = append(errs, errors.New("reolink_password is required unless dry_run is enabled"))
		}
	}
	for name, value := range map[string]string{
		"reolink_stream_path": c.ReolinkStreamPath,
		"reolink_username":    c.ReolinkUsername,
		"sip_username":        c.SIPUsername,
		"sip_destination":     c.SIPDestination,
		"sip_display_name":    c.SIPDisplayName,
	} {
		if strings.ContainsAny(value, "\r\n") {
			errs = append(errs, fmt.Errorf("%s must not contain CR/LF characters", name))
		}
	}
	if err := validateHost("sip_registrar", c.SIPRegistrar); err != nil {
		errs = append(errs, err)
	} else if ip := net.ParseIP(strings.TrimSpace(c.SIPRegistrar)); ip != nil && ip.To4() == nil {
		errs = append(errs, errors.New("sip_registrar IPv6 literals are not supported; use an IPv4 address or a hostname resolving to IPv4"))
	}
	if c.SIPRegistrarPort < 1 || c.SIPRegistrarPort > 65535 {
		errs = append(errs, errors.New("sip_registrar_port must be 1..65535"))
	}
	if !c.DryRun {
		if strings.TrimSpace(c.SIPUsername) == "" {
			errs = append(errs, errors.New("sip_username is required unless dry_run is enabled"))
		}
		if strings.TrimSpace(c.SIPPassword) == "" {
			errs = append(errs, errors.New("sip_password is required unless dry_run is enabled"))
		}
		if strings.TrimSpace(c.SIPDestination) == "" {
			errs = append(errs, errors.New("sip_destination is required unless dry_run is enabled"))
		}
	}
	if c.SIPLocalPort < 1 || c.SIPLocalPort > 65535 {
		errs = append(errs, errors.New("sip_local_port must be 1..65535"))
	}
	if c.SIPCodecPreference != "pcma" && c.SIPCodecPreference != "pcmu" && c.SIPCodecPreference != "auto" {
		errs = append(errs, errors.New("sip_codec_preference must be pcma, pcmu or auto"))
	}
	if c.RingTimeoutSeconds < 5 || c.RingTimeoutSeconds > 180 {
		errs = append(errs, errors.New("ring_timeout_seconds must be 5..180"))
	}
	if c.MaxCallDurationSeconds < 15 || c.MaxCallDurationSeconds > 3600 {
		errs = append(errs, errors.New("max_call_duration_seconds must be 15..3600"))
	}
	if c.DebounceSeconds < 0 || c.DebounceSeconds > 60 {
		errs = append(errs, errors.New("debounce_seconds must be 0..60"))
	}
	if c.StatusPort < 1 || c.StatusPort > 65535 {
		errs = append(errs, errors.New("status_port must be 1..65535"))
	}
	if strings.TrimSpace(c.ReolinkStreamPath) == "" {
		errs = append(errs, errors.New("reolink_stream_path is required"))
	} else if !strings.HasPrefix(c.ReolinkStreamPath, "/") || strings.ContainsAny(c.ReolinkStreamPath, "?#") {
		errs = append(errs, errors.New("reolink_stream_path must be an absolute RTSP path without query or fragment"))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "warning", "error":
	default:
		errs = append(errs, errors.New("log_level must be debug, info, warn, warning or error"))
	}
	return errors.Join(errs...)
}

func validateHost(name, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.Contains(v, "://") || strings.ContainsAny(v, "/?#") {
		return fmt.Errorf("%s must be a hostname or IP address without scheme, path or port", name)
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("%s must not contain whitespace", name)
	}
	if net.ParseIP(v) == nil && strings.Contains(v, ":") {
		return fmt.Errorf("%s must be a hostname or IP address without a port", name)
	}
	return nil
}

func validBinarySensorEntityID(v string) bool {
	const prefix = "binary_sensor."
	if !strings.HasPrefix(v, prefix) || len(v) == len(prefix) {
		return false
	}
	for _, r := range v[len(prefix):] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func (c Config) HAPollInterval() time.Duration { return FixedHAPollInterval }
func (c Config) RingTimeout() time.Duration    { return time.Duration(c.RingTimeoutSeconds) * time.Second }
func (c Config) MaxCallDuration() time.Duration {
	return time.Duration(c.MaxCallDurationSeconds) * time.Second
}
func (c Config) Debounce() time.Duration { return time.Duration(c.DebounceSeconds) * time.Second }
func (c Config) FFmpegPath() string {
	if path := strings.TrimSpace(c.FFmpegBinaryPath); path != "" {
		return path
	}
	return FFmpegBinary
}
func (c Config) DebugEnabled() bool { return c.LogLevel == "debug" }
func (c Config) ReceiveMode() string {
	if c.EffectiveReolinkMode() == "nvr" {
		return "baichuan"
	}
	return "rtsp"
}
func (c Config) EffectiveReolinkMode() string {
	if c.ResolvedReolinkMode != "" {
		return c.ResolvedReolinkMode
	}
	return c.ReolinkMode
}
func (c Config) WithResolvedReolinkMode(mode string) Config {
	c.ResolvedReolinkMode = strings.ToLower(strings.TrimSpace(mode))
	return c
}
func (c *Config) SetAECDelay(delayMS int) {
	if delayMS < 0 {
		delayMS = 0
	}
	if delayMS > MaxSupportedAECDelayMS {
		delayMS = MaxSupportedAECDelayMS
	}
	window := c.EchoCancellationSearchWindowMS
	if window <= 0 {
		window = DefaultAECSearchWindowMS
	}
	c.AECInitialDelayMS = delayMS
	c.AECMinDelayMS = delayMS - window
	if c.AECMinDelayMS < 0 {
		c.AECMinDelayMS = 0
	}
	c.AECMaxDelayMS = delayMS + window
	if c.AECMaxDelayMS > MaxSupportedAECDelayMS {
		c.AECMaxDelayMS = MaxSupportedAECDelayMS
	}
}
func (c Config) WithAECDelay(delayMS int) Config { c.SetAECDelay(delayMS); return c }

func (c Config) RTSPURL() string {
	return fmt.Sprintf("rtsp://%s%s", net.JoinHostPort(c.ReolinkHost, strconv.Itoa(c.ReolinkRTSPPort)), c.ReolinkStreamPath)
}
