// Command streambridge-origin-router runs the Origin Router (Epic D): a
// stateless HTTP reverse proxy that routes viewer LL-HLS requests to
// whichever StreamBridge instance owns a session, per the Ownership
// Registry. It has no CGo/native-library dependency — pure Go, a much
// smaller build than the streamer image (Epic D5).
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/fleetclient"
	"vt-stream-transcoder/internal/originrouter"
	"vt-stream-transcoder/internal/registry"

	"github.com/go-redis/redis/v8"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

var (
	configFile = flag.String("config", "", "Path to configuration file (YAML)")
	listenAddr = flag.String("listen-addr", "", "Address for the Origin Router HTTP server (overrides config)")
)

func main() {
	flag.Parse()

	zerolog.TimeFieldFormat = time.RFC3339
	zlog.Logger = zlog.With().Caller().Logger()

	cfg := config.LoadOrDefault(*configFile)
	if *listenAddr != "" {
		cfg.OriginRouter.ListenAddr = *listenAddr
	}
	if !cfg.Registry.Enabled {
		zlog.Fatal().Msg("registry.enabled must be true for the Origin Router — it has nothing to route without the Ownership Registry")
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Registry.RedisAddr,
		Password: cfg.Registry.RedisPassword,
		DB:       cfg.Registry.RedisDB,
		// See the matching comment in cmd/streambridge/main.go -- go-redis
		// v8 (this) predates the RESP3/HELLO and CLIENT SETINFO handshake
		// behaviors that made v9 incompatible with twemproxy, so no
		// equivalent option is needed here.
		//
		// twemproxy silently closes idle connections server-side, and
		// without this the pool keeps handing out already-dead ones,
		// surfacing as bare EOF.
		IdleTimeout: 60 * time.Second,
		// See the matching comment in cmd/streambridge/main.go -- matches
		// vt-api-service's MaxRetries against the same twemproxy.
		MaxRetries: 5,
	})
	defer rdb.Close()
	reg := registry.New(rdb, cfg.Registry.KeyPrefix, cfg.Registry.SessionTTL)

	var liveness fleetclient.Checker = fleetclient.NoopChecker{}
	if cfg.OriginRouter.FleetController.Enabled {
		fc, err := fleetclient.New(cfg.OriginRouter.FleetController.GRPCAddr, cfg.OriginRouter.FleetController.ServiceName)
		if err != nil {
			zlog.Fatal().Err(err).Msg("failed to create Fleet Controller client")
		}
		defer fc.Close()
		liveness = fc
		zlog.Info().Msgf("Fleet Controller liveness cross-check enabled: addr=%s service=%s",
			cfg.OriginRouter.FleetController.GRPCAddr, cfg.OriginRouter.FleetController.ServiceName)
	} else {
		zlog.Warn().Msg("Fleet Controller liveness cross-check disabled (Epic D4 no-op) — a crashed origin's " +
			"sessions will surface as proxy errors rather than clean 404s until its registry record expires")
	}

	router := originrouter.New(reg, liveness, cfg.OriginRouter.OwnershipCacheTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/live/streams/", router)

	httpSrv := &http.Server{
		Addr:    cfg.OriginRouter.ListenAddr,
		Handler: mux,
		// No WriteTimeout: must tolerate LL-HLS's blocking-playlist reload
		// on the origin side (Epic D2) — a proxy-imposed deadline here
		// would cut off a legitimately slow-but-healthy response.
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		zlog.Info().Msgf("Origin Router listening on %s", cfg.OriginRouter.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.Fatal().Err(err).Msg("Origin Router HTTP server error")
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	zlog.Info().Msg("Received shutdown signal, stopping Origin Router...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		zlog.Error().Err(err).Msg("Origin Router shutdown error")
	}
}
