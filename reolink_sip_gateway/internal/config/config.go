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
	DefaultRTPInactivitySeconds = 15
	MaxParallelDestinations     = 3
	MaxCallRoutes               = 32
	DefaultRouteID              = "default"
	minimumAECSearchWindowMS    = 50
	maximumAECSearchWindowMS    = 1000
	MaxSupportedAECDelayMS      = 3000
)

type MobileTarget struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

type CallRoute struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	VisitorEntity  string `json:"visitor_entity"`
	DoorbellNumber string `json:"doorbell_number"`
	MobileNumber1  string `json:"mobile_number_1"`
	MobileNumber2  string `json:"mobile_number_2"`
	MobileNumber3  string `json:"mobile_number_3"`

	// v1.2.0/1.2.1 accepted catalogue IDs in these fields. They remain
	// decode-only aliases so standalone old runtime snapshots can be upgraded;
	// the Home Assistant adapter persists only the direct-number fields.
	LegacyMobileTarget1 string `json:"mobile_target_1,omitempty"`
	LegacyMobileTarget2 string `json:"mobile_target_2,omitempty"`
	LegacyMobileTarget3 string `json:"mobile_target_3,omitempty"`
}

func (r CallRoute) MobileNumbers() []string {
	values := []string{r.MobileNumber1, r.MobileNumber2, r.MobileNumber3}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// ResolvedCallRoute is the runtime routing contract. It contains no camera or
// media selector: every route deliberately shares the one configured Reolink
// media path.
type ResolvedCallRoute struct {
	ID             string
	Name           string
	VisitorEntity  string
	DoorbellNumber string
	MobileTargets  []MobileTarget
}

type Config struct {
	// TriggerSource defaults to Home Assistant for existing installations.
	TriggerSource  string `json:"trigger_source,omitempty"`
	TriggerRouteID string `json:"trigger_route_id,omitempty"`
	// Legacy v1.1/v1.2 routing inputs. They are accepted only for lossless
	// upgrades and are immediately converted to CallRoutes during Load.
	VisitorEntity        string         `json:"visitor_entity,omitempty"`
	SIPDestination       string         `json:"sip_destination,omitempty"`
	ParallelDestinations []string       `json:"parallel_destinations,omitempty"`
	MobileTargets        []MobileTarget `json:"mobile_targets,omitempty"`

	ReolinkHost                    string      `json:"reolink_host"`
	ReolinkRTSPPort                int         `json:"reolink_rtsp_port"`
	ReolinkStreamPath              string      `json:"reolink_stream_path"`
	ReolinkUsername                string      `json:"reolink_username"`
	ReolinkPassword                string      `json:"reolink_password"`
	ReolinkMode                    string      `json:"reolink_mode"`
	BaichuanPort                   int         `json:"baichuan_port"`
	NVRChannel                     int         `json:"nvr_channel"`
	FritzFonLiveImageEnabled       bool        `json:"fritzfon_live_image_enabled"`
	EchoCancellationEnabled        bool        `json:"echo_cancellation_enabled"`
	EchoCancellationSearchWindowMS int         `json:"echo_cancellation_search_window_ms"`
	WebRTCHighPassFilterEnabled    bool        `json:"webrtc_high_pass_filter_enabled"`
	WebRTCNoiseSuppressionEnabled  bool        `json:"webrtc_noise_suppression_enabled"`
	SIPRegistrar                   string      `json:"sip_registrar"`
	SIPRegistrarPort               int         `json:"sip_registrar_port"`
	SIPUsername                    string      `json:"sip_username"`
	SIPPassword                    string      `json:"sip_password"`
	SIPLocalPort                   int         `json:"sip_local_port"`
	SIPDisplayName                 string      `json:"sip_display_name"`
	SIPCodecPreference             string      `json:"sip_codec_preference"`
	DoorCallEnabled                bool        `json:"door_call_enabled"`
	ParallelCallEnabled            bool        `json:"parallel_call_enabled"`
	ParallelUsername               string      `json:"parallel_username"`
	ParallelPassword               string      `json:"parallel_password"`
	ParallelLocalPort              int         `json:"parallel_local_port"`
	CallRoutes                     []CallRoute `json:"call_routes"`
	IncomingCallsEnabled           bool        `json:"incoming_calls_enabled"`
	IncomingAllowedCallers         []string    `json:"incoming_allowed_callers"`
	IncomingConnectionToneEnabled  bool        `json:"incoming_connection_tone_enabled"`
	RTPInactivityTimeoutSeconds    int         `json:"rtp_inactivity_timeout_seconds"`
	RingTimeoutSeconds             int         `json:"ring_timeout_seconds"`
	MaxCallDurationSeconds         int         `json:"max_call_duration_seconds"`
	DebounceSeconds                int         `json:"debounce_seconds"`
	LogLevel                       string      `json:"log_level"`
	DryRun                         bool        `json:"dry_run"`

	// Runtime-only values. They are resolved during startup and never exposed as
	// user options. This keeps the Home Assistant form small while preserving a
	// deterministic media configuration for every call.
	StatusPort          int    `json:"-"`
	ResolvedReolinkMode string `json:"-"`
	AECInitialDelayMS   int    `json:"-"`
	AECMinDelayMS       int    `json:"-"`
	AECMaxDelayMS       int    `json:"-"`
	FFmpegBinaryPath    string `json:"-"` // test/runtime override; never a Home Assistant option
	DataDir             string `json:"-"`
}

func Defaults() Config {
	cfg := Config{
		TriggerSource:                  "homeassistant",
		TriggerRouteID:                 DefaultRouteID,
		ReolinkHost:                    "192.168.177.50",
		ReolinkUsername:                "admin",
		ReolinkRTSPPort:                554,
		ReolinkStreamPath:              "/Preview_01_sub",
		ReolinkMode:                    "auto",
		BaichuanPort:                   9000,
		NVRChannel:                     1,
		FritzFonLiveImageEnabled:       true,
		EchoCancellationEnabled:        true,
		EchoCancellationSearchWindowMS: DefaultAECSearchWindowMS,
		WebRTCHighPassFilterEnabled:    true,
		WebRTCNoiseSuppressionEnabled:  true,
		SIPRegistrar:                   "192.168.177.9",
		SIPRegistrarPort:               5060,
		SIPLocalPort:                   5070,
		SIPDisplayName:                 "Haustür",
		SIPCodecPreference:             "pcma",
		DoorCallEnabled:                true,
		ParallelCallEnabled:            false,
		ParallelLocalPort:              5071,
		CallRoutes: []CallRoute{{
			ID:             DefaultRouteID,
			Name:           "Standardroute",
			VisitorEntity:  "binary_sensor.reolink_video_doorbell_visitor",
			DoorbellNumber: "11",
		}},
		IncomingCallsEnabled:          false,
		IncomingAllowedCallers:        []string{"*"},
		IncomingConnectionToneEnabled: true,
		RTPInactivityTimeoutSeconds:   DefaultRTPInactivitySeconds,
		RingTimeoutSeconds:            30,
		MaxCallDurationSeconds:        300,
		DebounceSeconds:               3,
		LogLevel:                      "info",
		DryRun:                        true,
		StatusPort:                    18099,
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
	b, err := os.ReadFile(path)
	if err != nil {
		return Defaults(), fmt.Errorf("read config: %w", err)
	}
	return Decode(b)
}

// Decode applies the same defaults, migrations and validation to every adapter.
func Decode(b []byte) (Config, error) {
	cfg := Defaults()
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
	if cfg.ReolinkMode == "direct" {
		// A camera has one endpoint; never reuse a previously selected NVR channel.
		cfg.NVRChannel = 0
	}
	cfg.SIPCodecPreference = strings.ToLower(strings.TrimSpace(cfg.SIPCodecPreference))
	cfg.ParallelDestinations = normalizeListEntries(cfg.ParallelDestinations)
	cfg.migrateLegacyRouting(raw)
	cfg.normalizeRouting()
	cfg.IncomingAllowedCallers = normalizeCallerEntries(cfg.IncomingAllowedCallers)
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	cfg.StatusPort = 18099
	cfg.SetAECDelay(DefaultAECInitialDelayMS)
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []error
	switch c.TriggerSource {
	case "homeassistant", "manual":
	case "baichuan":
		found := false
		for _, route := range c.CallRoutes {
			found = found || route.ID == c.TriggerRouteID
		}
		if !found {
			errs = append(errs, errors.New("trigger_route_id must reference a configured call route"))
		}
	default:
		errs = append(errs, errors.New("trigger_source must be homeassistant, baichuan or manual"))
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
	case "auto", "nvr", "standalone", "direct":
	default:
		errs = append(errs, errors.New("reolink_mode must be auto, nvr, standalone or direct"))
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
		"sip_display_name":    c.SIPDisplayName,
		"parallel_username":   c.ParallelUsername,
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
	if c.DoorCallEnabled && !c.DryRun {
		if strings.TrimSpace(c.SIPUsername) == "" {
			errs = append(errs, errors.New("sip_username is required when the door call is enabled"))
		}
		if strings.TrimSpace(c.SIPPassword) == "" {
			errs = append(errs, errors.New("sip_password is required when the door call is enabled"))
		}
	}
	if c.SIPLocalPort < 1 || c.SIPLocalPort > 65535 {
		errs = append(errs, errors.New("sip_local_port must be 1..65535"))
	}
	if c.SIPCodecPreference != "pcma" && c.SIPCodecPreference != "pcmu" && c.SIPCodecPreference != "auto" {
		errs = append(errs, errors.New("sip_codec_preference must be pcma, pcmu or auto"))
	}
	if c.ParallelLocalPort < 1 || c.ParallelLocalPort > 65535 {
		errs = append(errs, errors.New("parallel_local_port must be 1..65535"))
	}
	if c.ParallelCallEnabled {
		if c.DoorCallEnabled && c.ParallelLocalPort == c.SIPLocalPort {
			errs = append(errs, errors.New("parallel_local_port must differ from sip_local_port"))
		}
		if !c.DryRun {
			if strings.TrimSpace(c.ParallelUsername) == "" {
				errs = append(errs, errors.New("parallel_username is required when parallel calling is enabled"))
			}
			if strings.TrimSpace(c.ParallelPassword) == "" {
				errs = append(errs, errors.New("parallel_password is required when parallel calling is enabled"))
			}
		}
	}
	if !c.DryRun && !c.DoorCallEnabled && !c.ParallelCallEnabled {
		errs = append(errs, errors.New("at least one of door_call_enabled or parallel_call_enabled must be enabled"))
	}
	errs = append(errs, c.validateRouting()...)
	if c.IncomingCallsEnabled && len(c.IncomingAllowedCallers) == 0 {
		errs = append(errs, errors.New("incoming_allowed_callers must contain at least one caller or * when incoming calls are enabled"))
	}
	wildcardCallers := 0
	for _, caller := range c.IncomingAllowedCallers {
		caller = strings.TrimSpace(caller)
		if caller == "" {
			errs = append(errs, errors.New("incoming_allowed_callers entries must not be empty"))
			continue
		}
		if strings.ContainsAny(caller, "\r\n") {
			errs = append(errs, errors.New("incoming_allowed_callers must not contain CR/LF characters"))
		}
		if len(caller) > 128 {
			errs = append(errs, errors.New("incoming_allowed_callers entries must not exceed 128 characters"))
		}
		if caller == "*" {
			wildcardCallers++
		}
	}
	if wildcardCallers > 0 && len(c.IncomingAllowedCallers) > 1 {
		errs = append(errs, errors.New("incoming_allowed_callers wildcard * must be the only entry"))
	}
	if c.RTPInactivityTimeoutSeconds < 5 || c.RTPInactivityTimeoutSeconds > 120 {
		errs = append(errs, errors.New("rtp_inactivity_timeout_seconds must be 5..120"))
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

func (c *Config) migrateLegacyRouting(raw map[string]json.RawMessage) {
	targets := make(map[string]string, len(c.MobileTargets))
	for _, target := range c.MobileTargets {
		id := strings.TrimSpace(target.ID)
		if id != "" {
			targets[id] = strings.TrimSpace(target.Destination)
		}
	}

	_, routesPresent := raw["call_routes"]
	if !routesPresent || len(c.CallRoutes) == 0 {
		route := Defaults().CallRoutes[0]
		if _, present := raw["visitor_entity"]; present {
			route.VisitorEntity = strings.TrimSpace(c.VisitorEntity)
		}
		if _, present := raw["sip_destination"]; present {
			route.DoorbellNumber = strings.TrimSpace(c.SIPDestination)
		}
		legacyNumbers := c.ParallelDestinations
		if len(legacyNumbers) > MaxParallelDestinations {
			legacyNumbers = legacyNumbers[:MaxParallelDestinations]
		}
		for index, destination := range legacyNumbers {
			switch index {
			case 0:
				route.MobileNumber1 = destination
			case 1:
				route.MobileNumber2 = destination
			case 2:
				route.MobileNumber3 = destination
			}
		}
		c.CallRoutes = []CallRoute{route}
	}

	for i := range c.CallRoutes {
		route := &c.CallRoutes[i]
		numbers := []*string{&route.MobileNumber1, &route.MobileNumber2, &route.MobileNumber3}
		legacyRefs := []string{route.LegacyMobileTarget1, route.LegacyMobileTarget2, route.LegacyMobileTarget3}
		for index, ref := range legacyRefs {
			if strings.TrimSpace(*numbers[index]) != "" || strings.TrimSpace(ref) == "" {
				continue
			}
			ref = strings.TrimSpace(ref)
			if destination, found := targets[ref]; found && destination != "" {
				*numbers[index] = destination
			} else {
				// Some v1.2.1 users intuitively entered a number directly into the
				// old reference field. Preserve unknown values verbatim instead of
				// losing a potentially valid SIP destination.
				*numbers[index] = ref
			}
		}
		route.LegacyMobileTarget1 = ""
		route.LegacyMobileTarget2 = ""
		route.LegacyMobileTarget3 = ""
	}

	// Do not carry retired values into marshalled runtime snapshots.
	c.VisitorEntity = ""
	c.SIPDestination = ""
	c.ParallelDestinations = nil
	c.MobileTargets = nil
}

func (c *Config) normalizeRouting() {
	for i := range c.CallRoutes {
		c.CallRoutes[i].ID = strings.TrimSpace(c.CallRoutes[i].ID)
		c.CallRoutes[i].Name = strings.TrimSpace(c.CallRoutes[i].Name)
		c.CallRoutes[i].VisitorEntity = strings.TrimSpace(c.CallRoutes[i].VisitorEntity)
		c.CallRoutes[i].DoorbellNumber = strings.TrimSpace(c.CallRoutes[i].DoorbellNumber)
		c.CallRoutes[i].MobileNumber1 = strings.TrimSpace(c.CallRoutes[i].MobileNumber1)
		c.CallRoutes[i].MobileNumber2 = strings.TrimSpace(c.CallRoutes[i].MobileNumber2)
		c.CallRoutes[i].MobileNumber3 = strings.TrimSpace(c.CallRoutes[i].MobileNumber3)
	}
}

func (c Config) validateRouting() []error {
	var errs []error
	if len(c.CallRoutes) == 0 {
		errs = append(errs, errors.New("call_routes must contain at least one route"))
		return errs
	}
	if len(c.CallRoutes) > MaxCallRoutes {
		errs = append(errs, fmt.Errorf("call_routes must contain at most %d entries", MaxCallRoutes))
	}

	routeIDs := make(map[string]struct{}, len(c.CallRoutes))
	entities := make(map[string]string, len(c.CallRoutes))
	doorbellNumbers := make(map[string]string, len(c.CallRoutes))
	doorRoutes := 0
	mobileRoutes := 0
	for index, route := range c.CallRoutes {
		route.ID = strings.TrimSpace(route.ID)
		route.Name = strings.TrimSpace(route.Name)
		route.VisitorEntity = strings.TrimSpace(route.VisitorEntity)
		route.DoorbellNumber = strings.TrimSpace(route.DoorbellNumber)
		label := fmt.Sprintf("call_routes[%d]", index)
		if !validRoutingID(route.ID) {
			errs = append(errs, fmt.Errorf("%s.id must start with a lowercase letter and contain only lowercase letters, digits or underscores (maximum 64 characters)", label))
		} else if _, exists := routeIDs[route.ID]; exists {
			errs = append(errs, fmt.Errorf("call_routes contains duplicate id %q", route.ID))
		} else {
			routeIDs[route.ID] = struct{}{}
		}
		if err := validateRoutingName(label+".name", route.Name); err != nil {
			errs = append(errs, err)
		}
		if c.TriggerSource != "homeassistant" {
			// Direct Reolink and manual triggers do not require HA entity IDs.
		} else if !validBinarySensorEntityID(route.VisitorEntity) {
			errs = append(errs, fmt.Errorf("%s.visitor_entity must be a binary_sensor entity ID", label))
		} else if previousID, exists := entities[route.VisitorEntity]; exists {
			errs = append(errs, fmt.Errorf("call_routes %q and %q use the same visitor_entity", previousID, route.ID))
		} else {
			entities[route.VisitorEntity] = route.ID
		}

		if route.DoorbellNumber != "" {
			doorRoutes++
			if err := validateRoutingDestination(label+".doorbell_number", route.DoorbellNumber, true); err != nil {
				errs = append(errs, err)
			} else {
				canonical := canonicalRoutingDestination(route.DoorbellNumber)
				if previousID, exists := doorbellNumbers[canonical]; exists {
					errs = append(errs, fmt.Errorf("call_routes %q and %q use the same doorbell_number", previousID, route.ID))
				} else {
					doorbellNumbers[canonical] = route.ID
				}
			}
		}

		mobileFields := []string{route.MobileNumber1, route.MobileNumber2, route.MobileNumber3}
		mobileNumbers := route.MobileNumbers()
		if len(mobileNumbers) > 0 {
			mobileRoutes++
		}
		seenNumbers := make(map[string]struct{}, len(mobileNumbers))
		for numberIndex, destination := range mobileFields {
			destination = strings.TrimSpace(destination)
			if destination == "" {
				continue
			}
			field := fmt.Sprintf("%s.mobile_number_%d", label, numberIndex+1)
			if err := validateRoutingDestination(field, destination, false); err != nil {
				errs = append(errs, err)
				continue
			}
			canonical := canonicalRoutingDestination(destination)
			if _, duplicate := seenNumbers[canonical]; duplicate {
				errs = append(errs, fmt.Errorf("%s contains the same mobile number more than once", label))
				continue
			}
			seenNumbers[canonical] = struct{}{}
		}
		if route.DoorbellNumber == "" && len(mobileNumbers) == 0 {
			errs = append(errs, fmt.Errorf("%s must configure a doorbell_number, at least one mobile number, or both", label))
		}
		if !c.DryRun && !(c.DoorCallEnabled && route.DoorbellNumber != "") && !(c.ParallelCallEnabled && len(mobileNumbers) > 0) {
			errs = append(errs, fmt.Errorf("%s has no call path on an enabled SIP account", label))
		}
	}
	if c.DoorCallEnabled && doorRoutes == 0 {
		errs = append(errs, errors.New("door_call_enabled requires at least one call route with a doorbell_number"))
	}
	if c.ParallelCallEnabled && mobileRoutes == 0 {
		errs = append(errs, errors.New("parallel_call_enabled requires at least one call route with a mobile number"))
	}
	return errs
}

func validateRoutingName(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > 64 {
		return fmt.Errorf("%s must not exceed 64 characters", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must not contain CR/LF characters", name)
	}
	return nil
}

func validateRoutingDestination(name, value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s must not exceed 128 characters", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must not contain CR/LF characters", name)
	}
	return nil
}

func validRoutingID(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func canonicalRoutingDestination(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-', '.', '/', '(', ')':
			return -1
		default:
			return r
		}
	}, value)
}

func (c Config) ResolvedCallRoutes() []ResolvedCallRoute {
	routes := make([]ResolvedCallRoute, 0, len(c.CallRoutes))
	for _, route := range c.CallRoutes {
		resolved := ResolvedCallRoute{
			ID:             strings.TrimSpace(route.ID),
			Name:           strings.TrimSpace(route.Name),
			VisitorEntity:  strings.TrimSpace(route.VisitorEntity),
			DoorbellNumber: strings.TrimSpace(route.DoorbellNumber),
			MobileTargets:  make([]MobileTarget, 0, MaxParallelDestinations),
		}
		for index, destination := range route.MobileNumbers() {
			resolved.MobileTargets = append(resolved.MobileTargets, MobileTarget{
				ID:          strconv.Itoa(index + 1),
				Name:        fmt.Sprintf("Mobilziel %d", index+1),
				Destination: destination,
			})
		}
		routes = append(routes, resolved)
	}
	return routes
}

func (c Config) FindCallRoute(id string) (ResolvedCallRoute, bool) {
	id = strings.TrimSpace(id)
	for _, route := range c.ResolvedCallRoutes() {
		if route.ID == id {
			return route, true
		}
	}
	return ResolvedCallRoute{}, false
}

func (c Config) MaxMobileTargetsPerRoute() int {
	maximum := 0
	for _, route := range c.ResolvedCallRoutes() {
		if len(route.MobileTargets) > maximum {
			maximum = len(route.MobileTargets)
		}
	}
	return maximum
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

func normalizeCallerEntries(values []string) []string {
	return normalizeListEntries(values)
}

func normalizeListEntries(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (c Config) HAPollInterval() time.Duration { return FixedHAPollInterval }
func (c Config) RingTimeout() time.Duration    { return time.Duration(c.RingTimeoutSeconds) * time.Second }
func (c Config) MaxCallDuration() time.Duration {
	return time.Duration(c.MaxCallDurationSeconds) * time.Second
}
func (c Config) Debounce() time.Duration { return time.Duration(c.DebounceSeconds) * time.Second }
func (c Config) RTPInactivityTimeout() time.Duration {
	return time.Duration(c.RTPInactivityTimeoutSeconds) * time.Second
}
func (c Config) FFmpegPath() string {
	if path := strings.TrimSpace(c.FFmpegBinaryPath); path != "" {
		return path
	}
	return FFmpegBinary
}
func (c Config) DebugEnabled() bool { return c.LogLevel == "debug" }
func (c Config) ReceiveMode() string {
	if c.EffectiveReolinkMode() == "nvr" || c.EffectiveReolinkMode() == "direct" {
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
	if c.ResolvedReolinkMode == "direct" {
		c.NVRChannel = 0
	}
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
