package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mowfteedev/mowf-net/internal/app"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("failed to initialize application: %v", err)
	}

	log.Printf("Starting mowf-net server on %s", cfg.HTTPAddr)
	if err := application.Run(ctx); err != nil {
		log.Fatalf("application terminated with error: %v", err)
	}
	log.Println("mowf-net server stopped successfully")
}
