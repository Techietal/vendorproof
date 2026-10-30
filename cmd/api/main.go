package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vendorproof/internal/assignments"
	"vendorproof/internal/auth"
	"vendorproof/internal/config"
	"vendorproof/internal/contractors"
	"vendorproof/internal/database"
	"vendorproof/internal/engagements"
	"vendorproof/internal/httpserver"
	"vendorproof/internal/logging"
	"vendorproof/internal/obligations"
	"vendorproof/internal/organizations"
	"vendorproof/internal/requirements"
	"vendorproof/internal/sites"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel, os.Stdout)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	verifier, err := auth.NewVerifier(cfg.OIDC)
	if err != nil {
		return err
	}
	handler := httpserver.New(httpserver.Deps{
		Logger:        logger,
		Verifier:      verifier,
		Organizations: organizations.NewService(pool, organizations.NewRepository(pool)),
		Sites:         sites.NewService(pool),
		Contractors:   contractors.NewService(pool),
		Engagements:   engagements.NewService(pool),
		Requirements:  requirements.NewService(pool),
		Assignments:   assignments.NewService(pool),
		Obligations:   obligations.NewService(pool),
		Readiness:     pool,
		MaxBodyBytes:  cfg.MaxBodyBytes,
		Timeout:       cfg.RequestTimeout,
	})
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api listening",
			"addr", cfg.HTTPAddr,
			"database_host", safeDBHost(cfg.Database.URL),
			"issuer", cfg.OIDC.Issuer,
		)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		_ = server.Close()
	}
	logger.Info("api stopped")
	return nil
}

func safeDBHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "unknown"
	}
	host := u.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}
