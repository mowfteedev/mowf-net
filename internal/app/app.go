package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	ipamhttp "github.com/mowfteedev/mowf-net/internal/ipam/http"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
	"github.com/mowfteedev/mowf-net/internal/platform/database"
)

// App represents the application composition root.
type App struct {
	DB     *sql.DB
	Server *http.Server
	Mux    *http.ServeMux
}

// New constructs and wires all application dependencies.
func New(cfg Config) (*App, error) {
	db, err := database.Open(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database connection pool: %w", err)
	}

	subnetRepo := postgres.NewSubnetRepository(db)
	subnetService := service.NewSubnetService(subnetRepo)
	subnetHandler := ipamhttp.NewSubnetHandler(subnetService)
	allocationRepo := postgres.NewAllocationRepository(db)
	allocationService := service.NewAllocationService(allocationRepo)
	allocationHandler := ipamhttp.NewAllocationHandler(allocationService)

	mux := http.NewServeMux()
	subnetHandler.RegisterRoutes(mux)
	allocationHandler.RegisterRoutes(mux)

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &App{
		DB:     db,
		Server: server,
		Mux:    mux,
	}, nil
}

// Run starts the HTTP server and handles graceful shutdown when ctx is canceled.
func (a *App) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		if err := a.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var shutdownErr error
		if err := a.Server.Shutdown(shutdownCtx); err != nil {
			shutdownErr = fmt.Errorf("failed to gracefully shutdown HTTP server: %w", err)
		}

		if err := a.DB.Close(); err != nil && shutdownErr == nil {
			shutdownErr = fmt.Errorf("failed to close database connection: %w", err)
		}

		return shutdownErr

	case err := <-errCh:
		_ = a.DB.Close()
		return err
	}
}

// Close closes all underlying application resources.
func (a *App) Close() error {
	var closeErr error
	if a.Server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := a.Server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeErr = err
		}
	}
	if a.DB != nil {
		if err := a.DB.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}
