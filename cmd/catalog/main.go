package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mangahub/internal/catalog"
	"mangahub/internal/config"
	"mangahub/internal/database"
	"mangahub/proto/mangapb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("catalog service: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	db, err := database.OpenCatalog(cfg.CatalogDBPath)
	if err != nil {
		return fmt.Errorf("open catalog SQLite: %w", err)
	}
	defer db.Close()

	seedCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	inserted, err := database.SeedCatalog(seedCtx, db)
	cancel()
	if err != nil {
		return fmt.Errorf("seed catalog: %w", err)
	}
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM manga").Scan(&total); err != nil {
		return fmt.Errorf("count manga: %w", err)
	}

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}
	defer listener.Close()

	service, err := catalog.NewService(db)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	mangapb.RegisterCatalogServiceServer(server, service)

	// Health and reflection make diagnostics possible without a TUI.
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	reflection.Register(server)

	log.Printf("Catalog SQLite: %s", cfg.CatalogDBPath)
	log.Printf("Catalog ready: %d manga (%d newly seeded)", total, inserted)
	log.Printf("gRPC Catalog listening on %s", listener.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve gRPC: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Print("Shutting down catalog service")
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		done := make(chan struct{})
		go func() {
			server.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			server.Stop()
			<-done
		}
		return nil
	}
}
