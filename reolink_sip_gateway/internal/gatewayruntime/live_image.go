package gatewayruntime

import (
	"context"
	"log/slog"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/liveimage"
	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

const (
	liveImageCatalogStatePath  = "/data/fritzfon-live-image-catalog.json"
	liveImageDiscoveryInterval = 5 * time.Minute
	liveImageDiscoveryTimeout  = 12 * time.Second
	liveImagePrewarmTimeout    = 10 * time.Second
)

type liveImageCatalogProvider struct {
	catalog *liveimage.Catalog
}

func (p liveImageCatalogProvider) FetchJPEG(ctx context.Context) ([]byte, error) {
	return p.catalog.FetchJPEG(ctx)
}

func (p liveImageCatalogProvider) FetchChannelJPEG(ctx context.Context, number int) ([]byte, error) {
	return p.catalog.FetchChannelJPEG(ctx, number)
}

func (p liveImageCatalogProvider) FetchCameraJPEG(ctx context.Context, cameraID string) ([]byte, error) {
	return p.catalog.FetchCameraJPEG(ctx, cameraID)
}

func (p liveImageCatalogProvider) LiveImageChannels() []statuspkg.LiveImageChannel {
	channels := p.catalog.Channels()
	result := make([]statuspkg.LiveImageChannel, len(channels))
	for i, channel := range channels {
		result[i] = statuspkg.LiveImageChannel{
			Number: channel.Number, Name: channel.Name, CameraID: channel.CameraID,
			Online: channel.Online, StatusKnown: channel.StatusKnown, Primary: channel.Primary,
			LastImageAttempt: channel.LastImageAttempt, LastImageSuccess: channel.LastImageSuccess,
			LastImageSource: channel.LastImageSource, LastImageDuration: channel.LastImageDuration,
			LastImageFailed: channel.LastImageFailed,
		}
	}
	return result
}

func (p liveImageCatalogProvider) LiveImageDiscovery() statuspkg.LiveImageDiscovery {
	diagnostics := p.catalog.DiscoveryDiagnostics()
	return statuspkg.LiveImageDiscovery{
		LastAttempt: diagnostics.LastAttempt,
		LastSuccess: diagnostics.LastSuccess,
		LastFailed:  diagnostics.LastFailed,
	}
}

type liveImageDiscoverer interface {
	Discover(context.Context) ([]liveimage.Channel, error)
}

func runLiveImageDiscovery(
	ctx context.Context,
	discoverer liveImageDiscoverer,
	interval time.Duration,
	timeout time.Duration,
	logger *slog.Logger,
) {
	discover := func() {
		discoveryCtx, cancel := context.WithTimeout(ctx, timeout)
		channels, err := discoverer.Discover(discoveryCtx)
		cancel()
		if err != nil {
			if logger != nil {
				logger.Warn("Reolink NVR channel auto-detection unavailable; last known live-image catalog remains active", "error", err, "channels", len(channels))
			}
			return
		}
		if logger != nil {
			logger.Info("Reolink NVR live-image channels detected", "channels", len(channels))
		}
	}

	discover()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			discover()
		}
	}
}

type liveImagePrewarmer interface {
	PrewarmPrimary(context.Context) error
}

func startLiveImagePrewarm(ctx context.Context, prewarmer liveImagePrewarmer, logger *slog.Logger) {
	if prewarmer == nil {
		return
	}
	go func() {
		prewarmCtx, cancel := context.WithTimeout(ctx, liveImagePrewarmTimeout)
		defer cancel()
		if err := prewarmer.PrewarmPrimary(prewarmCtx); err != nil && logger != nil {
			logger.Warn("FRITZ!Fon door image prewarm failed; on-demand capture remains available", "error", err)
		}
	}()
}
