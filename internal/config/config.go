package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr      string
	TCPAddr       string
	UDPAddr       string
	GRPCAddr      string
	GRPCTarget    string
	BackendDBPath string
	CatalogDBPath string
	JWTSecret     string
	JWTTTL        time.Duration
}

func Load() (Config, error) {
	// Load .env if present. Environment variables take priority.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	hours, err := strconv.Atoi(getEnv("JWT_TTL_HOURS", "24"))
	if err != nil || hours <= 0 || hours > 720 {
		return Config{}, fmt.Errorf("JWT_TTL_HOURS must be between 1 and 720")
	}

	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 || strings.HasPrefix(secret, "CHANGE_ME") {
		return Config{}, fmt.Errorf("JWT_SECRET must contain at least 32 characters and must not be a placeholder")
	}

	cfg := Config{
		HTTPAddr:      getEnv("HTTP_ADDR", ":8080"),
		TCPAddr:       getEnv("TCP_ADDR", ":9090"),
		UDPAddr:       getEnv("UDP_ADDR", ":9091"),
		GRPCAddr:      getEnv("GRPC_ADDR", ":9092"),
		GRPCTarget:    getEnv("GRPC_TARGET", "localhost:9092"),
		BackendDBPath: getEnv("BACKEND_DB_PATH", "data/backend.db"),
		CatalogDBPath: getEnv("CATALOG_DB_PATH", "data/catalog.db"),
		JWTSecret:     secret,
		JWTTTL:        time.Duration(hours) * time.Hour,
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
