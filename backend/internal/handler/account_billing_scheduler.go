package handler

import (
	"context"
	"log"
	"sync"
	"time"
)

// accountBillingScheduler is started explicitly by the application provider.
// Constructing a handler or store does not start background database queries.
type accountBillingScheduler struct {
	roll     func(context.Context) error
	interval time.Duration
	timeout  time.Duration

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func newAccountBillingScheduler(roll func(context.Context) error) *accountBillingScheduler {
	return &accountBillingScheduler{
		roll:     roll,
		interval: time.Minute,
		timeout:  5 * time.Second,
	}
}

// Start runs once immediately and then once per minute, independently of HTTP
// requests. A stopped scheduler cannot be restarted.
func (h *AccountBillingHandler) Start(ctx context.Context) {
	if h != nil {
		h.scheduler.Start(ctx)
	}
}

// Stop cancels an in-flight database operation and waits for the worker to exit.
func (h *AccountBillingHandler) Stop() {
	if h != nil {
		h.scheduler.Stop()
	}
}

func (s *accountBillingScheduler) Start(parent context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped || s.roll == nil || s.interval <= 0 || s.timeout <= 0 {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	go s.run(ctx, s.done)
}

func (s *accountBillingScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if !s.started {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	cancel()
	<-done
}

func (s *accountBillingScheduler) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *accountBillingScheduler) runOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.roll(runCtx); err != nil && ctx.Err() == nil {
		log.Printf("[AccountBilling] Advance renewal dates failed: %v", err)
	}
}
