package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/conductorone/prospecting-bot/internal/bot"
	"github.com/conductorone/prospecting-bot/internal/cli"
	"github.com/conductorone/prospecting-bot/internal/config"
	"github.com/conductorone/prospecting-bot/internal/scheduler"
)

func main() {
	mode := flag.String("mode", "slack", "Run mode: 'slack' (default) or 'cli' for local terminal testing")
	flag.Parse()

	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	switch *mode {
	case "cli":
		// CLI mode only requires AWS_BEARER_TOKEN_BEDROCK — Slack tokens not needed.
		cfg, err := config.LoadPartial()
		if err != nil {
			slog.Error("config error", "err", err)
			os.Exit(1)
		}
		cli.Run(cfg)

	default: // "slack"
		cfg, err := config.Load()
		if err != nil {
			slog.Error("config error", "err", err)
			os.Exit(1)
		}

		b := bot.New(cfg)

		sched := scheduler.New(cfg, b)
		if err := sched.Start(); err != nil {
			slog.Error("scheduler error", "err", err)
			os.Exit(1)
		}
		defer sched.Stop()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

		slog.Info("C1ProspectingBot v2 starting", "channel", cfg.SlackChannel, "schedule", cfg.ScheduleCron)

		go func() {
			<-sig
			slog.Info("shutting down")
			cancel()
			sched.Stop()
		}()

		if err := b.Run(ctx); err != nil {
			slog.Error("bot error", "err", err)
			os.Exit(1)
		}
	}
}
