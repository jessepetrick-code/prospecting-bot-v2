package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/conductorone/prospecting-bot/internal/config"
	"github.com/conductorone/prospecting-bot/internal/llm"
)

// Poster is the interface the scheduler uses to post to Slack.
// The bot.Bot struct satisfies this interface.
type Poster interface {
	PostScheduled(ctx context.Context, text string) error
}

// Scheduler manages the cron-based morning kickoff job.
type Scheduler struct {
	cron   *cron.Cron
	cfg    *config.Config
	poster Poster
}

// New creates a Scheduler.
func New(cfg *config.Config, poster Poster) *Scheduler {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.UTC
	}
	c := cron.New(cron.WithLocation(loc))
	return &Scheduler{cron: c, cfg: cfg, poster: poster}
}

// Start registers the morning kickoff cron job and starts the scheduler.
func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(s.cfg.ScheduleCron, func() {
		slog.Info("scheduler: running morning kickoff")
		if err := s.poster.PostScheduled(context.Background(), llm.MorningKickoffPrompt); err != nil {
			slog.Error("scheduler: morning kickoff failed", "err", err)
		}
	})
	if err != nil {
		return err
	}
	s.cron.Start()
	slog.Info("scheduler: started", "cron", s.cfg.ScheduleCron)
	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

