package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dimitrije/nikodis/internal/broker"
	"github.com/dimitrije/nikodis/internal/cache"
	"github.com/dimitrije/nikodis/internal/config"
	"github.com/dimitrije/nikodis/internal/debuglog"
	"github.com/dimitrije/nikodis/internal/server"
)

func main() {
	port := envInt("NIKODIS_PORT", 6390)
	maxBuffer := envInt("NIKODIS_MAX_BUFFER_SIZE", 10000)
	defaultAckTimeout := envDuration("NIKODIS_DEFAULT_ACK_TIMEOUT", 30*time.Second)
	maxRedeliveries := envInt("NIKODIS_MAX_REDELIVERIES", 5)

	var cfg *config.Config
	if path := os.Getenv("NIKODIS_CONFIG_FILE"); path != "" {
		var err error
		cfg, err = config.Load(path)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		log.Printf("Loaded config from %s", path)
	}

	var logger debuglog.Logger
	if envBool("NIKODIS_DEBUG_LOG") {
		dir := envString("NIKODIS_DEBUG_LOG_DIR", "./logs/")
		rotation := debuglog.RotationDaily
		if strings.ToLower(os.Getenv("NIKODIS_DEBUG_LOG_ROTATION")) == "hourly" {
			rotation = debuglog.RotationHourly
		}
		var err error
		logger, err = debuglog.NewFileLogger(dir, rotation)
		if err != nil {
			log.Fatalf("create debug logger: %v", err)
		}
		log.Printf("Debug logging enabled (dir=%s)", dir)
	}

	cacheStore := cache.New(logger)
	b := broker.New(broker.Config{
		MaxBufferSize:     maxBuffer,
		DefaultAckTimeout: defaultAckTimeout,
		MaxRedeliveries:   uint32(maxRedeliveries),
	}, logger)

	srv, err := server.NewServer(cacheStore, b, cfg, port)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		srv.GracefulStop()
		b.Close()
		cacheStore.Close()
		if logger != nil {
			logger.Close()
		}
	}()

	log.Printf("Nikodis listening on :%d", port)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func envString(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return n
}

func envBool(key string) bool {
	v := strings.ToLower(os.Getenv(key))
	return v == "true" || v == "1" || v == "yes"
}

func envDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, err2 := strconv.Atoi(v)
		if err2 != nil {
			log.Fatalf("invalid %s: %v", key, err)
		}
		return time.Duration(n) * time.Second
	}
	return d
}

