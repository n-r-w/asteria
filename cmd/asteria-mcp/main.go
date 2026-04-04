// Package main - entrypoint for the Asteria MCP.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/n-r-w/asteria/internal/appinit"
)

// build-time variables that can be set via ldflags
//
//nolint:gochecknoglobals // ok for build info
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
	builtBy = "unknown"
)

// buildInfo holds build-time information.
type buildInfo struct {
	version string
	commit  string
	date    string
	builtBy string
}

// getBuildInfo returns build-time information.
func getBuildInfo() buildInfo {
	return buildInfo{
		version: version,
		commit:  commit,
		date:    date,
		builtBy: builtBy,
	}
}

func main() {
	ctx := context.Background()

	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	info := getBuildInfo()

	if *showVersion {
		//nolint:exhaustruct // stdlib struct with optional fields
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
		logger.Info("asteria-mcp version info",
			"version", info.version,
			"commit", info.commit,
			"built", info.date,
			"built_by", info.builtBy,
		)
		os.Exit(0)
	}

	//nolint:exhaustruct // SDK struct with optional fields
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(ctx, info.version); err != nil {
		slog.Error("server failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run builds the real application graph once and keeps the signal-aware server lifecycle isolated from DI setup.
func run(ctx context.Context, version string) (runErr error) {
	di, err := appinit.CreateDIContainer(version)
	if err != nil {
		return err
	}

	return runServerLifecycle(ctx, di.MCPServer.Run, di.Close)
}

// runServerLifecycle keeps signal handling and shutdown cleanup testable without adding a dedicated wrapper type
// around the real DI container.
func runServerLifecycle(
	ctx context.Context,
	runServer func(context.Context) error,
	closeApp func(context.Context) error,
) (runErr error) {
	ctxStop, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	defer func() {
		closeErr := closeApp(context.WithoutCancel(ctxStop))
		if closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	slog.Info("starting server")
	if errSrv := runServer(ctxStop); errSrv != nil {
		return errSrv
	}

	slog.Info("server stopped gracefully")
	return nil
}
