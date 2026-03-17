package main

import (
	"flag"
	"log"
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
	mode := flag.String("mode", "slack", "Run mode: slack or cli")
	flag.Parse()

	cfg, err := config.Load()
	if *mode == "cli" && err != nil {
		// CLI mode only needs ANTHROPIC_API_KEY; relax validation
		slog.Warn("config load warning (ok for cli mode)", "err", err)
		cfg = config.LoadPartial()
	} else if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	switch *mode {
	case "cli":
		cli.Run(cfg)
	case "slack":
		runSlack(cfg)
	default:
		log.Fatalf("unknown mode: %s (expected slack or cli)", *mode)
	}
}

func runSlack(cfg *config.Config) {
	b, err := bot.New(cfg)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	sched := scheduler.New(cfg, b)
	if err := sched.Start(); err != nil {
		log.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sig
		slog.Info("shutting down")
		sched.Stop()
		os.Exit(0)
	}()

	slog.Info("starting slack bot")
	if err := b.Run(); err != nil {
		log.Fatalf("bot exited with error: %v", err)
	}
}
