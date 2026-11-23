package main

import (
	"context"
	"database/sql"

	"mangahub/internal/auth"
	"mangahub/internal/config"
	"mangahub/proto/mangapb"

	"github.com/gin-gonic/gin"
)

// registerMember2Extensions is the only backend integration point reserved
// for Member 2. They can later attach the /ws route, the protected
// /api/admin/manga/:id/release route, and the UDP listener here, using the
// existing configuration, JWT middleware, and Catalog gRPC client.
//
// This complete no-op implementation keeps Member 1's backend runnable
// before the TUI, UDP, WebSocket, and admin release features are built.
func registerMember2Extensions(
	router *gin.Engine,
	cfg config.Config,
	backendDB *sql.DB,
	catalog mangapb.CatalogServiceClient,
	authHandler *auth.Handler,
) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}
