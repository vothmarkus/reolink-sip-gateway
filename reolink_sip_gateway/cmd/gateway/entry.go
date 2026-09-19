package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/ha"
	"github.com/vothmarkus/reolink-sip-gateway/internal/standalone"
	"github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

func main() {
	if err := entry(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func entry() error {
	mode := flag.String("mode", envOr("REOLINK_SIP_MODE", "auto"), "auto, homeassistant or standalone")
	configPath := flag.String("config", os.Getenv("REOLINK_SIP_CONFIG"), "configuration JSON file")
	dataDir := flag.String("data-dir", os.Getenv("REOLINK_SIP_DATA_DIR"), "persistent state directory")
	listen := flag.String("listen", envOr("REOLINK_SIP_LISTEN", "0.0.0.0:18099"), "standalone web/API listen address")
	checkOnly := flag.Bool("check-config", false, "validate configuration without starting services")
	resolveVisitor := flag.Bool("resolve-visitor-entity", false, "resolve the HA Reolink visitor entity")
	showVersion := flag.Bool("version", false, "print version")
	healthcheck := flag.Bool("healthcheck", false, "check the local standalone web service")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *healthcheck {
		_, port, err := net.SplitHostPort(*listen)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/health")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("health status %d", response.StatusCode)
		}
		return nil
	}
	if *resolveVisitor {
		token := os.Getenv("SUPERVISOR_TOKEN")
		if token == "" {
			return fmt.Errorf("visitor entity resolution: SUPERVISOR_TOKEN is missing")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entity, err := ha.ResolveReolinkVisitorEntity(ctx, "", token)
		if err != nil {
			return fmt.Errorf("visitor entity resolution: %w", err)
		}
		fmt.Println(entity)
		return nil
	}
	if *mode == "auto" {
		*mode = "standalone"
		if os.Getenv("SUPERVISOR_TOKEN") != "" {
			*mode = "homeassistant"
		}
		// Preserve the existing flat-runtime validation command used by HA CI.
		if *checkOnly && *configPath != "" {
			if raw, err := os.ReadFile(*configPath); err == nil {
				var shape map[string]json.RawMessage
				if json.Unmarshal(raw, &shape) == nil && shape["schema_version"] == nil {
					*mode = "homeassistant"
				}
			}
		}
	}
	if *mode != "homeassistant" && *mode != "standalone" {
		return fmt.Errorf("mode must be auto, homeassistant or standalone")
	}
	if *dataDir == "" {
		*dataDir = "/var/lib/reolink-sip-gateway"
		if *mode == "homeassistant" {
			*dataDir = "/data"
		}
	}
	if *configPath == "" {
		*configPath = filepath.Join(*dataDir, "config.json")
		if *mode == "homeassistant" {
			*configPath = "/data/options.json"
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if *mode == "homeassistant" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		if *checkOnly {
			fmt.Println("configuration valid")
			return nil
		}
		cfg.DataDir = *dataDir
		return runGateway(ctx, cfg, gatewayRuntimeOptions{})
	}
	if *checkOnly {
		raw, err := os.ReadFile(*configPath)
		if err != nil {
			return err
		}
		settings, err := standalone.DecodeSettings(raw)
		if err != nil {
			return err
		}
		if _, err = settings.Runtime(false); err != nil {
			return err
		}
		fmt.Println("configuration valid")
		return nil
	}
	_, portString, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("listen port must be 1..65535")
	}
	identity, err := status.LoadOrCreateIdentity(*dataDir)
	if err != nil {
		return err
	}
	var server *standalone.Server
	runner := func(ctx context.Context, cfg config.Config, publish func(http.Handler)) error {
		return runGateway(ctx, cfg, gatewayRuntimeOptions{
			UIAuth: server.Auth,
			Publish: func(handler http.Handler) {
				// End old SSE streams when a configuration generation is stopped.
				publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCtx, done := context.WithCancel(r.Context())
					stop := context.AfterFunc(ctx, done)
					defer stop()
					defer done()
					handler.ServeHTTP(w, r.WithContext(requestCtx))
				}))
			},
		})
	}
	server, err = standalone.New(*configPath, *dataDir, port, version, identity.Token, runner, newLogger("info"))
	if err != nil {
		return err
	}
	return server.Serve(ctx, *listen)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
