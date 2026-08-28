package liveimage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

const maxChannelStatusBytes = 1 << 20

// Channel is one public, 1-based NVR channel that can be presented to a
// FRITZ!Fon. Reolink's GetChannelstatus response is 0-based; that protocol
// detail remains inside this package.
type Channel struct {
	Number int
	Name   string
}

type catalogChannel struct {
	Channel
	source *Source
}

// Catalog keeps the configured door channel available at all times and can
// add every online NVR channel discovered through GetChannelstatus. Each
// channel has its own short snapshot cache, so simultaneous requests for one
// camera are coalesced without coupling unrelated cameras or the audio path.
type Catalog struct {
	cfg           config.Config
	primary       *Source
	client        *http.Client
	cgiBases      []string
	sourceFactory func(int) *Source

	mu       sync.RWMutex
	channels map[int]catalogChannel
}

func NewCatalog(cfg config.Config, logger *slog.Logger) *Catalog {
	channelLogger := func(number int) *slog.Logger {
		if logger == nil {
			return nil
		}
		return logger.With("nvr_channel", number)
	}
	primaryNumber := cfg.NVRChannel + 1
	primary := newChannelSource(cfg, channelLogger(primaryNumber), cfg.NVRChannel)
	catalog := &Catalog{
		cfg:      cfg,
		primary:  primary,
		client:   newChannelStatusHTTPClient(),
		cgiBases: snapshotCGIBases(cfg.ReolinkHost),
		channels: map[int]catalogChannel{
			primaryNumber: {Channel: Channel{Number: primaryNumber}, source: primary},
		},
	}
	catalog.sourceFactory = func(channel int) *Source {
		number := channel + 1
		if number == primaryNumber {
			return primary
		}
		return newChannelSource(cfg, channelLogger(number), channel)
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
	channels := make([]Channel, 0, len(c.channels))
	for _, entry := range c.channels {
		channels = append(channels, entry.Channel)
	}
	c.mu.RUnlock()
	sort.Slice(channels, func(i, j int) bool { return channels[i].Number < channels[j].Number })
	return channels
}

// Discover replaces the optional channel set with the currently online NVR
// channels. The configured primary channel is always retained so the existing
// door-live-image URL remains useful during a temporary discovery failure or
// camera outage.
func (c *Catalog) Discover(ctx context.Context) ([]Channel, error) {
	detected, err := c.fetchOnlineChannels(ctx)
	if err != nil {
		return c.Channels(), err
	}

	c.mu.Lock()
	previous := c.channels
	next := make(map[int]catalogChannel, len(detected)+1)
	for _, channel := range detected {
		entry, ok := previous[channel.Number]
		if !ok || entry.source == nil {
			entry.source = c.sourceFactory(channel.Number - 1)
		}
		entry.Channel = channel
		next[channel.Number] = entry
	}
	primaryNumber := c.cfg.NVRChannel + 1
	if _, ok := next[primaryNumber]; !ok {
		entry := previous[primaryNumber]
		if entry.source == nil {
			entry.source = c.primary
		}
		entry.Number = primaryNumber
		next[primaryNumber] = entry
	}
	c.channels = next
	c.mu.Unlock()
	return c.Channels(), nil
}

func (c *Catalog) fetchOnlineChannels(ctx context.Context) ([]Channel, error) {
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

func (c *Catalog) fetchChannelStatusCGI(ctx context.Context, base string) ([]Channel, error) {
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
	req.Header.Set("User-Agent", "ReolinkSIPGateway/1.4.0")
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
			} `json:"status"`
		} `json:"value"`
	}
	if err := json.Unmarshal(payload, &replies); err != nil || len(replies) != 1 ||
		replies[0].Code != 0 || !strings.EqualFold(replies[0].Command, "GetChannelstatus") {
		return nil, fmt.Errorf("%s Reolink channel-status response was rejected", u.Scheme)
	}

	byNumber := make(map[int]Channel)
	for _, status := range replies[0].Value.Status {
		if status.Online != 1 || status.Channel < 0 || status.Channel > 255 {
			continue
		}
		number := status.Channel + 1
		byNumber[number] = Channel{Number: number, Name: normalizeChannelName(status.Name)}
	}
	if len(byNumber) == 0 {
		return nil, fmt.Errorf("%s Reolink channel-status reported no online channels", u.Scheme)
	}
	channels := make([]Channel, 0, len(byNumber))
	for _, channel := range byNumber {
		channels = append(channels, channel)
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
