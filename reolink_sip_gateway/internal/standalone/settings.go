// Package standalone provides the persistent setup UI and runtime lifecycle.
package standalone

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

// Settings is the public, versioned file/UI contract. NVR channel numbers are
// always 1-based here; runtime-only fields are intentionally not serializable.
type Settings struct {
	SchemaVersion int                `json:"schema_version"`
	Reolink       ReolinkSettings    `json:"reolink"`
	SIP           SIPSettings        `json:"sip"`
	Audio         AudioSettings      `json:"audio"`
	Call          CallSettings       `json:"call"`
	CallRoutes    []config.CallRoute `json:"call_routes"`
	Trigger       TriggerSettings    `json:"trigger"`
	LiveImage     struct {
		Enabled bool `json:"fritzfon_live_image_enabled"`
	} `json:"live_image"`
	Diagnostics struct {
		DryRun   bool   `json:"dry_run"`
		LogLevel string `json:"log_level"`
	} `json:"diagnostics"`
}

type ReolinkSettings struct {
	Host         string `json:"reolink_host"`
	Username     string `json:"reolink_username"`
	Password     string `json:"reolink_password"`
	Mode         string `json:"reolink_mode"`
	Channel      int    `json:"nvr_channel_number"`
	RTSPPort     int    `json:"reolink_rtsp_port"`
	BaichuanPort int    `json:"baichuan_port"`
}

type SIPSettings struct {
	Registrar         string `json:"sip_registrar"`
	RegistrarPort     int    `json:"sip_registrar_port"`
	DoorEnabled       bool   `json:"door_call_enabled"`
	Username          string `json:"sip_username"`
	Password          string `json:"sip_password"`
	LocalPort         int    `json:"sip_local_port"`
	DisplayName       string `json:"sip_display_name"`
	Codec             string `json:"sip_codec_preference"`
	ParallelEnabled   bool   `json:"parallel_call_enabled"`
	ParallelUsername  string `json:"parallel_username"`
	ParallelPassword  string `json:"parallel_password"`
	ParallelLocalPort int    `json:"parallel_local_port"`
}

type AudioSettings struct {
	AEC              bool `json:"echo_cancellation_enabled"`
	HighPass         bool `json:"webrtc_high_pass_filter_enabled"`
	NoiseSuppression bool `json:"webrtc_noise_suppression_enabled"`
}

type CallSettings struct {
	Incoming       bool     `json:"incoming_calls_enabled"`
	AllowedCallers []string `json:"incoming_allowed_callers"`
	ConnectionTone bool     `json:"incoming_connection_tone_enabled"`
	Debounce       int      `json:"debounce_seconds"`
	RingTimeout    int      `json:"ring_timeout_seconds"`
	RTPTimeout     int      `json:"rtp_inactivity_timeout_seconds"`
	MaxDuration    int      `json:"max_call_duration_seconds"`
}

type TriggerSettings struct {
	Source  string `json:"source"`
	RouteID string `json:"route_id"`
}

func Defaults() Settings {
	s := Settings{SchemaVersion: 2}
	s.Reolink = ReolinkSettings{Username: "admin", Mode: "auto", Channel: 1, RTSPPort: 554, BaichuanPort: 9000}
	s.SIP = SIPSettings{Registrar: "auto", RegistrarPort: 5060, DoorEnabled: true, LocalPort: 5070, ParallelLocalPort: 5071, DisplayName: "Haustür", Codec: "pcma"}
	s.Audio = AudioSettings{AEC: true, HighPass: true, NoiseSuppression: true}
	s.Call = CallSettings{AllowedCallers: []string{"*"}, ConnectionTone: true, Debounce: 3, RingTimeout: 30, RTPTimeout: 15, MaxDuration: 300}
	s.CallRoutes = []config.CallRoute{{ID: config.DefaultRouteID, Name: "Standardroute", DoorbellNumber: "11"}}
	s.Trigger = TriggerSettings{Source: "baichuan", RouteID: config.DefaultRouteID}
	s.LiveImage.Enabled = true
	s.Diagnostics.DryRun, s.Diagnostics.LogLevel = true, "info"
	return s
}

func DecodeSettings(raw []byte) (Settings, error) {
	s := Defaults()
	// Require the explicit version even when all other optional fields use defaults.
	s.SchemaVersion = 0
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return s, errors.New("configuration must contain exactly one JSON object")
	}
	if s.SchemaVersion != 2 {
		return s, errors.New("unsupported configuration schema_version (expected 2)")
	}
	return s, nil
}

// Runtime uses the existing validator and media defaults. Static validation
// does not require a working network or contact any external device.
func (s Settings) Runtime(resolveGateway bool) (config.Config, error) {
	if s.SchemaVersion != 2 {
		return config.Config{}, errors.New("schema_version must be 2")
	}
	if s.Reolink.Channel < 1 || s.Reolink.Channel > 256 {
		return config.Config{}, errors.New("NVR channel must be between 1 and 256")
	}
	if s.Trigger.Source != "baichuan" && s.Trigger.Source != "manual" {
		return config.Config{}, errors.New("standalone trigger source must be baichuan or manual")
	}
	if s.Trigger.Source == "baichuan" && (strings.TrimSpace(s.Reolink.Username) == "" || s.Reolink.Password == "") {
		return config.Config{}, errors.New("Reolink username and password are required for the direct visitor trigger")
	}
	flat := map[string]any{}
	for _, group := range []any{s.Reolink, s.SIP, s.Audio, s.Call, s.LiveImage, s.Diagnostics} {
		b, err := json.Marshal(group)
		if err != nil {
			return config.Config{}, err
		}
		var fields map[string]any
		if err := json.Unmarshal(b, &fields); err != nil {
			return config.Config{}, err
		}
		for k, v := range fields {
			flat[k] = v
		}
	}
	delete(flat, "nvr_channel_number")
	channel := s.Reolink.Channel
	if s.Reolink.Mode == "standalone" {
		channel = 1
	}
	flat["nvr_channel"] = channel - 1
	flat["reolink_stream_path"] = fmt.Sprintf("/Preview_%02d_sub", channel)
	flat["call_routes"] = s.CallRoutes
	flat["trigger_source"], flat["trigger_route_id"] = s.Trigger.Source, s.Trigger.RouteID
	if strings.EqualFold(strings.TrimSpace(s.SIP.Registrar), "auto") {
		registrar := "192.0.2.1" // validation placeholder only; never a runtime default
		if resolveGateway {
			var err error
			registrar, err = DefaultIPv4Gateway("/proc/net/route")
			if err != nil {
				return config.Config{}, err
			}
		}
		flat["sip_registrar"] = registrar
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return config.Config{}, err
	}
	return config.Decode(b)
}

func DefaultIPv4Gateway(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read default route: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	bestMetric := uint64(^uint64(0))
	var gateway uint64
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		address, e1 := strconv.ParseUint(fields[2], 16, 32)
		flags, e2 := strconv.ParseUint(fields[3], 16, 32)
		metric, e3 := strconv.ParseUint(fields[6], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || address == 0 || flags&3 != 3 {
			continue
		}
		if metric < bestMetric {
			bestMetric, gateway = metric, address
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if gateway == 0 {
		return "", errors.New("no IPv4 default gateway; enter the FRITZ!Box address in SIP settings")
	}
	return fmt.Sprintf("%d.%d.%d.%d", gateway&255, (gateway>>8)&255, (gateway>>16)&255, (gateway>>24)&255), nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".gateway-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
