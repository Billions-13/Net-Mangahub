package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mangahub/internal/auth"
	"mangahub/internal/config"
	"mangahub/internal/database"
	"mangahub/internal/httpapi"
	tcpserver "mangahub/internal/tcp/server"
	"mangahub/proto/mangapb"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	catalogTimeout  = 3 * time.Second
	shutdownTimeout = 8 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("backend: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// This SQLite database belongs to the backend. The Catalog process owns
	// its own separate SQLite database and is accessed only through gRPC.
	backendDB, err := database.OpenBackend(cfg.BackendDBPath)
	if err != nil {
		return fmt.Errorf("open backend SQLite: %w", err)
	}
	defer backendDB.Close()

	// grpc.NewClient is non-blocking. If the Catalog process stops, individual
	// REST calls will return controlled 503 errors after their deadlines.
	catalogConn, err := grpc.NewClient(
		cfg.GRPCTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("create catalog gRPC client: %w", err)
	}
	defer catalogConn.Close()
	catalogClient := mangapb.NewCatalogServiceClient(catalogConn)

	// Use the same database and JWT secret for HTTP and TCP authentication.
	tcpService, err := tcpserver.NewServer(backendDB, cfg.JWTSecret)
	if err != nil {
		return fmt.Errorf("initialize TCP server: %w", err)
	}

	router, stopExtensions, err := buildRouter(backendDB, catalogClient, cfg, tcpService)
	if err != nil {
		return fmt.Errorf("initialize HTTP routes: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := stopExtensions(ctx); err != nil {
			log.Printf("stop optional services: %v", err)
		}
	}()

	tcpListener, err := net.Listen("tcp", cfg.TCPAddr)
	if err != nil {
		return fmt.Errorf("listen TCP on %s: %w", cfg.TCPAddr, err)
	}
	defer tcpListener.Close()

	httpListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP on %s: %w", cfg.HTTPAddr, err)
	}
	defer httpListener.Close()

	httpServer := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("Backend SQLite: %s", cfg.BackendDBPath)
	log.Printf("Catalog gRPC target: %s", cfg.GRPCTarget)
	log.Printf("HTTP API listening on %s", httpListener.Addr())
	log.Printf("TCP progress listening on %s", tcpListener.Addr())

	type serverResult struct {
		name string
		err  error
	}
	results := make(chan serverResult, 2)
	go func() { results <- serverResult{"HTTP", httpServer.Serve(httpListener)} }()
	go func() { results <- serverResult{"TCP", tcpService.Serve(tcpListener)} }()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var runErr error
	select {
	case <-signalCtx.Done():
		log.Print("Shutting down backend")
	case result := <-results:
		// Either server stopping unexpectedly means the backend is not healthy.
		if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) {
			runErr = fmt.Errorf("%s server stopped: %w", result.name, result.err)
		} else {
			runErr = fmt.Errorf("%s server stopped unexpectedly", result.name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP graceful shutdown: %v", err)
		_ = httpServer.Close()
		if runErr == nil {
			runErr = err
		}
	}
	if err := tcpService.Shutdown(ctx); err != nil {
		log.Printf("TCP graceful shutdown: %v", err)
		if runErr == nil {
			runErr = err
		}
	}
	return runErr
}

// buildRouter integrates the already-tested modules without redefining their
// HTTP handlers. It also provides an isolated hook for Member 2's UDP, WS,
// and admin-release features in extensions.go.
func buildRouter(
	backendDB *sql.DB,
	catalog mangapb.CatalogServiceClient,
	cfg config.Config,
	notifier *tcpserver.Server,
) (*gin.Engine, func(context.Context) error, error) {
	authHandler, err := auth.NewHandler(backendDB, cfg.JWTSecret, cfg.JWTTTL)
	if err != nil {
		return nil, nil, err
	}
	mangaHandler, err := httpapi.NewMangaHandler(catalog, catalogTimeout)
	if err != nil {
		return nil, nil, err
	}
	libraryHandler, err := httpapi.NewLibraryHandler(backendDB, catalog, catalogTimeout, notifier)
	if err != nil {
		return nil, nil, err
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	router.GET("/api/health", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := backendDB.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "backend"})
	})

	authHandler.RegisterRoutes(router)
	mangaHandler.RegisterRoutes(router)
	libraryHandler.RegisterRoutes(router, authHandler.RequireAuth())

	// Member 2 will only need to implement extensions.go, not edit these core
	// HTTP/JWT/SQLite/TCP or gRPC wiring paths.
	stopExtensions, err := registerMember2Extensions(router, cfg, backendDB, catalog, authHandler)
	if err != nil {
		return nil, nil, err
	}
	return router, stopExtensions, nil
}
