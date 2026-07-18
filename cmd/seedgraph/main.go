package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lesir831/SeedGraph/internal/auth"
	"github.com/lesir831/SeedGraph/internal/config"
	"github.com/lesir831/SeedGraph/internal/cryptox"
	"github.com/lesir831/SeedGraph/internal/deletion"
	"github.com/lesir831/SeedGraph/internal/httpapi"
	"github.com/lesir831/SeedGraph/internal/iyuu"
	"github.com/lesir831/SeedGraph/internal/store"
	"github.com/lesir831/SeedGraph/internal/syncer"
	"github.com/lesir831/SeedGraph/internal/webui"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("SeedGraph stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cipher, err := cryptox.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, database.Close())
	}()
	passwordHash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return err
	}
	if err := database.EnsureAdmin(ctx, passwordHash); err != nil {
		return err
	}

	syncService := syncer.New(database, cipher, logger, cfg.SyncInterval, cfg.FullSyncInterval)
	iyuuClient, err := iyuu.New(iyuu.Config{BaseURL: cfg.IYUUSitesURL})
	if err != nil {
		return err
	}
	iyuuService, err := iyuu.NewService(iyuuClient, database, logger, cfg.IYUUSyncInterval)
	if err != nil {
		return err
	}
	deleteService := deletion.New(database, syncService, logger, cfg.StaleAfter)
	deleteService.Start(ctx)
	sessionManager := auth.NewSessionManager(cipher, 12*time.Hour)
	api, err := httpapi.New(httpapi.Options{
		Store: database, Cipher: cipher, Sessions: sessionManager,
		Syncer: syncService, Deletions: deleteService, Logger: logger,
		IYUU:         iyuuService,
		CookieSecure: cfg.CookieSecure, StaleAfter: cfg.StaleAfter,
	})
	if err != nil {
		return err
	}
	frontend, err := webui.New(cfg.WebDirectory)
	if err != nil {
		logger.Warn("frontend assets unavailable", "directory", cfg.WebDirectory, "error", err)
	}
	var frontendHandler http.Handler
	if frontend != nil {
		frontendHandler = frontend
	}

	server := &http.Server{
		Addr: cfg.ListenAddress, Handler: api.Handler(frontendHandler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 1 << 20,
	}
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		syncService.Run(ctx)
	}()
	if cfg.IYUUSyncEnabled {
		background.Add(1)
		go func() {
			defer background.Done()
			iyuuService.Run(ctx)
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				logger.Error("force HTTP server close failed", "error", closeErr)
			}
		}
	}()

	logger.Info("SeedGraph listening", "address", cfg.ListenAddress)
	err = server.ListenAndServe()
	// ListenAndServe can also fail before a signal arrives (for example, when
	// the port is already occupied). Cancel and join scheduled services before
	// deferred database closure in either path.
	stop()
	<-shutdownDone
	background.Wait()
	deleteService.Wait()
	iyuuService.Wait()
	syncService.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
