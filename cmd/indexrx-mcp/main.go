package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/config"
	"github.com/8fclss/indexrx/internal/db"
	"github.com/8fclss/indexrx/internal/tools"
)

const version = "0.1.0"

func main() {
	// stdout is the MCP protocol channel, all logs go to stderr
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	server := mcp.NewServer(&mcp.Implementation{Name: "indexrx", Version: version}, nil)
	tools.Register(server, pool)

	slog.Info("indexrx MCP server started", "version", version)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run mcp server: %w", err)
	}
	slog.Info("indexrx MCP server stopped")
	return nil
}
