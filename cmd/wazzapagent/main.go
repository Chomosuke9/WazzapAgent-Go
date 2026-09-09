package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/app"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/backup"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup", "verify-backup", "restore-backup":
			return runOfflineCommand(os.Args[1:])
		}
	}
	cfg, err := config.LoadRuntime(os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
		return 2
	}

	logger, _, err := observability.NewLogger(os.Stdout, cfg.LogLevel(), cfg.LogFormat())
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize logger: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.New(cfg, logger).Run(ctx); err != nil {
		logger.Error("application stopped with error", "error", err)
		return 1
	}
	return 0
}

func runOfflineCommand(arguments []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch arguments[0] {
	case "backup":
		if len(arguments) != 2 {
			fmt.Fprintln(os.Stderr, "usage: wazzapagent backup <destination-parent>")
			return 2
		}
		dataDir, err := config.LoadDataDirRuntime(os.LookupEnv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid data directory configuration: %v\n", err)
			return 2
		}
		path, err := backup.Create(ctx, dataDir, arguments[1], time.Now().UTC())
		if err != nil {
			fmt.Fprintf(os.Stderr, "backup failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "backup created: %s\n", path)
		return 0
	case "verify-backup":
		if len(arguments) != 2 {
			fmt.Fprintln(os.Stderr, "usage: wazzapagent verify-backup <backup-directory>")
			return 2
		}
		manifest, err := backup.Verify(ctx, arguments[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "backup verification failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "backup verified: %d files\n", len(manifest.Files))
		return 0
	case "restore-backup":
		if len(arguments) != 3 {
			fmt.Fprintln(os.Stderr, "usage: wazzapagent restore-backup <backup-directory> <new-data-directory>")
			return 2
		}
		if err := backup.Restore(ctx, arguments[1], arguments[2]); err != nil {
			fmt.Fprintf(os.Stderr, "restore failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "backup restored into: %s\n", arguments[2])
		return 0
	default:
		return 2
	}
}
