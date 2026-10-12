// mybooks: personal PDF library server, designed to be browsed from a Kobo
// and managed through a JSON API. It speaks plain HTTP and is meant to run
// behind a TLS-terminating reverse proxy such as Caddy.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

type Config struct {
	APIKey      string
	DataDir     string
	Addr        string
	SessionDays int
	MaxUploadMB int64
	PageSize    int
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil && v > 0 {
		return v
	}
	return def
}

func loadConfig() Config {
	return Config{
		APIKey:      os.Getenv("MYBOOKS_API_KEY"),
		DataDir:     env("MYBOOKS_DATA_DIR", "./data"),
		Addr:        env("MYBOOKS_ADDR", "127.0.0.1:8080"),
		SessionDays: envInt("MYBOOKS_SESSION_DAYS", 90),
		MaxUploadMB: int64(envInt("MYBOOKS_MAX_UPLOAD_MB", 512)),
		PageSize:    envInt("MYBOOKS_PAGE_SIZE", 12),
	}
}

func main() {
	cfg := loadConfig()
	if len(cfg.APIKey) < 12 {
		log.Fatal("MYBOOKS_API_KEY must be set (at least 12 characters)")
	}

	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           logRequests(NewApp(cfg, store).Routes()),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("mybooks %s: listening on %s, data in %s", version, cfg.Addr, cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
