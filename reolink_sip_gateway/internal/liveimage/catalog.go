package liveimage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

const (
	maxChannelStatusBytes = 1 << 20
	maxCatalogStateBytes  = 1 << 20
	catalogStateVersion   = 1
)

// Channel is one public, 1-based NVR channel that can be presented to a
// FRITZ!Fon. Reolink's GetChannelstatus response is 0-based; that protocol
// detail remains inside this package. CameraID is a one-way UID fingerprint,
// not the camera UID itself.
type Channel struct {
	Number            int
	Name              string
	CameraID          string
	Online            bool
	StatusKnown       bool
	Primary           bool
	LastImageAttempt  time.Time
	LastImageSuccess  time.Time
	LastImageSource   string
	LastImageDuration time.Duration
	LastImageFailed   bool
}

type DiscoveryDiagnostics struct {
	LastAttempt time.Time
	LastSuccess time.Time
	LastFailed  bool
}

type catalogChannel struct {
	Channel
	uid    string
	source *Source
}

type persistedCatalog struct {
	Version              int                `json:"version"`
	Host                 string             `json:"host"`
	LastDiscoverySuccess time.Time          `json:"last_discovery_success,omitempty"`
	Channels             []persistedChannel `json:"channels"`
}

type persistedChannel struct {
	Number int    `json:"number"`
	Name   string `json:"name,omitempty"`
	UID    string `json:"uid,omitempty"`
	Online bool   `json:"online"`
}

// Catalog keeps the configured door channel available at all times and adds
// the NVR channels discovered through GetChannelstatus. The most recently
// successful catalog can be persisted, so temporary NVR outages never make
// previously published FRITZ!Fon URLs disappear.
type Catalog struct {
	cfg           config.Config
	primary       *Source
	client        *http.Client
	cgiBases      []string
	sourceFactory func(int) *Source
	logger        *slog.Logger
	statePath     string
	now           func() time.Time

	mu        sync.RWMutex
	channels  map[int]catalogChannel
	discovery DiscoveryDiagnostics
}

func NewCatalog(cfg config.Config, logger *slog.Logger) *Catalog {
	return newCatalog(cfg, logger, "")
}

// NewPersistentCatalog restores and updates the last successful NVR channel
// catalog at statePath. Corrupt or device-mismatched state is ignored safely.
func NewPersistentCatalog(cfg config.Config, logger *slog.Logger, statePath string) *Catalog {
	return newCatalog(cfg, logger, statePath)
}

func newCatalog(cfg config.Config, logger *slog.Logger, statePath string) *Catalog {
	channelLogger := func(number int) *slog.Logger {
		if logger == nil {
			return nil
		}
		return logger.With("nvr_channel", number)
	}
	primaryNumber := cfg.NVRChannel + 1
	primary := newChannelSource(cfg, channelLogger(primaryNumber), cfg.NVRChannel)
	catalog := &Catalog{
		cfg:       cfg,
		primary:   primary,
		client:    newChannelStatusHTTPClient(),
		cgiBases:  snapshotCGIBases(cfg.ReolinkHost),
		logger:    logger,
		statePath: strings.TrimSpace(statePath),
		now:       time.Now,
		channels: map[int]catalogChannel{
			primaryNumber: {
				Channel: Channel{Number: primaryNumber, Primary: true},
				source:  primary,
			},
		},
	}
	catalog.sourceFactory = func(channel int) *Source {
		number := channel + 1
		if number == primaryNumber {
			return primary
		}
		return newChannelSource(cfg, channelLogger(number), channel)
	}
	if catalog.statePath != "" {
		if err := catalog.loadState(); err != nil && !errors.Is(err, os.ErrNotExist) && logger != nil {
			logger.Warn("persistent FRITZ!Fon camera catalog could not be restored", "error", err)
		}
	}
	return catalog
}

// FetchJPEG preserves the v1.3 door-live-image behavior and therefore always
// reads the configured primary channel, independent of channel discovery.
func (c *Catalog) FetchJPEG(ctx context.Context) ([]byte, error) {
	return c.primary.FetchJPEG(ctx)
}

func (c *Catalog) FetchChannelJPEG(ctx context.Context, number int) ([]byte, error) {
	c.mu.RLock()
	entry, ok := c.channels[number]
	c.mu.RUnlock()
	if !ok || entry.source == nil {
		return nil, errors.New("FRITZ!Fon live image channel is unavailable")
	}
	return entry.source.FetchJPEG(ctx)
}

// FetchCameraJPEG follows the camera's derived stable ID to its current NVR
// channel. A channel move therefore does not change the published URL.
func (c *Catalog) FetchCameraJPEG(ctx context.Context, cameraID string) ([]byte, error) {
	c.mu.RLock()
	var source *Source
	for _, entry := range c.channels {
		if entry.CameraID == cameraID {
			source = entry.source
			break
		}
	}
	c.mu.RUnlock()
	if source == nil {
		return nil, errors.New("FRITZ!Fon live image camera is unavailable")
	}
	return source.FetchJPEG(ctx)
}

// PrewarmPrimary obtains the door frame in advance of FRITZ!Fon requesting it.
func (c *Catalog) PrewarmPrimary(ctx context.Context) error {
	return c.primary.Prewarm(ctx)
}

func (c *Catalog) ChannelNumbers() []int {
	channels := c.Channels()
	numbers := make([]int, len(channels))
	for i, channel := range channels {
		numbers[i] = channel.Number
	}
	return numbers
}

func (c *Catalog) ChannelName(number int) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channels[number].Name
}

func (c *Catalog) Channels() []Channel {
	c.mu.RLock()
	entries := make([]catalogChannel, 0, len(c.channels))
	for _, entry := range c.channels {
		entries = append(entries, entry)
	}
	c.mu.RUnlock()

	channels := make([]Channel, 0, len(entries))
	for _, entry := range entries {
		channel := entry.Channel
		if entry.source != nil {
			diagnostics := entry.source.Diagnostics()
			channel.LastImageAttempt = diagnostics.LastAttempt
			channel.LastImageSuccess = diagnostics.LastSuccess
			channel.LastImageSource = diagnostics.LastSource
			channel.LastImageDuration = diagnostics.LastDuration
			channel.LastImageFailed = diagnostics.LastFailed
		}
		channels = append(channels, channel)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].Number < channels[j].Number })
	return channels
}

func (c *Catalog) DiscoveryDiagnostics() DiscoveryDiagnostics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.discovery
}

// Discover atomically replaces the catalog after a valid complete response.
// On any request or parse failure, the last known channel set remains intact.
func (c *Catalog) Discover(ctx context.Context) ([]Channel, error) {
	attemptedAt := c.now()
	c.mu.Lock()
	c.discovery.LastAttempt = attemptedAt
	c.mu.Unlock()

	detected, err := c.fetchChannels(ctx)
	if err != nil {
		c.mu.Lock()
		c.discovery.LastFailed = true
		c.mu.Unlock()
		return c.Channels(), err
	}

	c.mu.Lock()
	previous := c.channels
	previousByCamera := make(map[string]catalogChannel, len(previous))
	for _, entry := range previous {
		if entry.CameraID != "" {
			previousByCamera[entry.CameraID] = entry
		}
	}
	next := make(map[int]catalogChannel, len(detected)+1)
	seenCameraIDs := make(map[string]bool, len(detected))
	for _, detectedEntry := range detected {
		channel := detectedEntry.Channel
		if channel.CameraID != "" {
			if seenCameraIDs[channel.CameraID] {
				continue
			}
			seenCameraIDs[channel.CameraID] = true
		}
		entry, ok := previous[channel.Number]
		if !ok || entry.source == nil {
			entry.source = c.sourceFactory(channel.Number - 1)
		}
		if old, ok := previousByCamera[channel.CameraID]; ok && channel.Name == "" {
			channel.Name = old.Name
		}
		entry.Channel = channel
		entry.uid = detectedEntry.uid
		next[channel.Number] = entry
	}
	primaryNumber := c.cfg.NVRChannel + 1
	if entry, ok := next[primaryNumber]; ok {
		entry.Primary = true
		entry.source = c.primary
		next[primaryNumber] = entry
	} else {
		entry := previous[primaryNumber]
		entry.Number = primaryNumber
		entry.Primary = true
		entry.StatusKnown = true
		entry.Online = false
		entry.source = c.primary
		next[primaryNumber] = entry
	}
	c.channels = next
	c.discovery.LastSuccess = attemptedAt
	c.discovery.LastFailed = false
	c.mu.Unlock()

	if c.statePath != "" {
		if err := c.saveState(); err != nil && c.logger != nil {
			c.logger.Warn("persistent FRITZ!Fon camera catalog could not be updated", "error", err)
		}
	}
	return c.Channels(), nil
}

func (c *Catalog) fetchChannels(ctx context.Context) ([]catalogChannel, error) {
	var attempts []error
	for _, base := range c.cgiBases {
		channels, err := c.fetchChannelStatusCGI(ctx, base)
		if err == nil {
			return channels, nil
		}
		attempts = append(attempts, err)
	}
	if len(attempts) == 0 {
		return nil, errors.New("no Reolink channel-status endpoint is configured")
	}
	return nil, errors.Join(attempts...)
}

func (c *Catalog) fetchChannelStatusCGI(ctx context.Context, base string) ([]catalogChannel, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid Reolink channel-status endpoint")
	}
	u.Path = "/api.cgi"
	u.RawQuery = ""
	query := u.Query()
	query.Set("cmd", "GetChannelstatus")
	query.Set("user", c.cfg.ReolinkUsername)
	query.Set("password", c.cfg.ReolinkPassword)
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s Reolink channel-status request could not be created", u.Scheme)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ReolinkSIPGateway/2.0.0-beta.1")
	response, err := c.client.Do(req)
	if err != nil {
		// net/http errors can include the complete credential-bearing URL.
		return nil, fmt.Errorf("%s Reolink channel-status connection failed", u.Scheme)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s Reolink channel-status returned HTTP %d", u.Scheme, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxChannelStatusBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxChannelStatusBytes {
		return nil, fmt.Errorf("%s Reolink channel-status response is invalid", u.Scheme)
	}

	var replies []struct {
		Command string `json:"cmd"`
		Code    int    `json:"code"`
		Value   struct {
			Status []struct {
				Channel int    `json:"channel"`
				Name    string `json:"name"`
				Online  int    `json:"online"`
				UID     string `json:"uid"`
			} `json:"status"`
		} `json:"value"`
	}
	if err := json.Unmarshal(payload, &replies); err != nil || len(replies) != 1 ||
		replies[0].Code != 0 || !strings.EqualFold(replies[0].Command, "GetChannelstatus") {
		return nil, fmt.Errorf("%s Reolink channel-status response was rejected", u.Scheme)
	}

	byNumber := make(map[int]catalogChannel)
	for _, status := range replies[0].Value.Status {
		name := normalizeChannelName(status.Name)
		uid := normalizeUID(status.UID)
		if status.Channel < 0 || status.Channel > 255 || (status.Online != 1 && name == "" && uid == "") {
			continue
		}
		number := status.Channel + 1
		byNumber[number] = catalogChannel{
			Channel: Channel{
				Number: number, Name: name, CameraID: cameraIDForUID(uid),
				Online: status.Online == 1, StatusKnown: true,
			},
			uid: uid,
		}
	}
	if len(byNumber) == 0 {
		return nil, fmt.Errorf("%s Reolink channel-status reported no cameras", u.Scheme)
	}
	channels := make([]catalogChannel, 0, len(byNumber))
	for _, entry := range byNumber {
		channels = append(channels, entry)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].Number < channels[j].Number })
	return channels, nil
}

func normalizeChannelName(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}

func normalizeUID(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func cameraIDForUID(uid string) string {
	uid = normalizeUID(uid)
	if uid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:16])
}

func (c *Catalog) loadState() error {
	file, err := os.Open(c.statePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxCatalogStateBytes {
		return errors.New("camera catalog state has an invalid size")
	}
	var state persistedCatalog
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&state); err != nil {
		return errors.New("camera catalog state is invalid")
	}
	if state.Version != catalogStateVersion || state.Host != catalogHost(c.cfg) || len(state.Channels) > 256 {
		return errors.New("camera catalog state belongs to another configuration")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	primaryNumber := c.cfg.NVRChannel + 1
	for _, saved := range state.Channels {
		if saved.Number < 1 || saved.Number > 256 {
			continue
		}
		uid := normalizeUID(saved.UID)
		entry := catalogChannel{
			Channel: Channel{
				Number: saved.Number, Name: normalizeChannelName(saved.Name), CameraID: cameraIDForUID(uid),
				Online: saved.Online, StatusKnown: false, Primary: saved.Number == primaryNumber,
			},
			uid: uid, source: c.sourceFactory(saved.Number - 1),
		}
		c.channels[saved.Number] = entry
	}
	primary := c.channels[primaryNumber]
	primary.Number = primaryNumber
	primary.Primary = true
	primary.source = c.primary
	c.channels[primaryNumber] = primary
	c.discovery.LastSuccess = state.LastDiscoverySuccess
	return nil
}

func (c *Catalog) saveState() error {
	c.mu.RLock()
	state := persistedCatalog{
		Version: catalogStateVersion, Host: catalogHost(c.cfg),
		LastDiscoverySuccess: c.discovery.LastSuccess,
		Channels:             make([]persistedChannel, 0, len(c.channels)),
	}
	for _, entry := range c.channels {
		state.Channels = append(state.Channels, persistedChannel{
			Number: entry.Number, Name: entry.Name, UID: entry.uid, Online: entry.Online,
		})
	}
	c.mu.RUnlock()
	sort.Slice(state.Channels, func(i, j int) bool { return state.Channels[i].Number < state.Channels[j].Number })

	directory := filepath.Dir(c.statePath)
	temporary, err := os.CreateTemp(directory, ".fritzfon-live-image-catalog-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, c.statePath); err != nil {
		return err
	}
	keep = true
	return nil
}

func catalogHost(cfg config.Config) string {
	return strings.ToLower(strings.TrimSpace(cfg.ReolinkHost))
}

func newChannelStatusHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec -- configured local Reolink device boundary
			MinVersion:         tls.VersionTLS12,
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	client.CheckRedirect = sameHostRedirects
	return client
}
