package main

import (
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

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	switch *mode {
	case "cli":
		// CLI mode only requires ANTHROPIC_API_KEY — Slack tokens not needed.
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

		b, err := bot.New(cfg)
		if err != nil {
			slog.Error("bot init error", "err", err)
			os.Exit(1)
		}

		sched := scheduler.New(cfg, b)
		if err := sched.Start(); err != nil {
			slog.Error("scheduler error", "err", err)
			os.Exit(1)
		}
		defer sched.Stop()

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

		slog.Info("C1ProspectingBot v2 starting", "channel", cfg.SlackChannel, "schedule", cfg.ScheduleCron)

		go func() {
			<-sig
			slog.Info("shutting down")
			sched.Stop()
			os.Exit(0)
		}()

		if err := b.Run(); err != nil {
			slog.Error("bot error", "err", err)
			os.Exit(1)
		}
	}
}
