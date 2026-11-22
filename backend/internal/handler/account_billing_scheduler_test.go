package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAccountBillingHandlerConstructorDoesNotStartScheduler(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	h := NewAccountBillingHandler(db, nil)
	require.NotNil(t, h.scheduler)
	require.False(t, h.scheduler.started)
	require.Equal(t, time.Minute, h.scheduler.interval)
	require.Equal(t, 5*time.Second, h.scheduler.timeout)
	h.Stop()
	h.Stop()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountBillingSchedulerRunsImmediatelyAndPeriodically(t *testing.T) {
	calls := make(chan struct{}, 10)
	s := newAccountBillingScheduler(func(context.Context) error {
		calls <- struct{}{}
		return nil
	})
	s.interval = 10 * time.Millisecond
	t.Cleanup(s.Stop)
	s.Start(context.Background())
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("missing renewal run %d", i+1)
		}
	}
}

func TestAccountBillingSchedulerRepeatedStartAndStopAreSafe(t *testing.T) {
	calls := make(chan struct{}, 10)
	s := newAccountBillingScheduler(func(context.Context) error {
		calls <- struct{}{}
		return nil
	})
	s.interval = time.Hour
	t.Cleanup(s.Stop)
	s.Start(context.Background())
	s.Start(context.Background())
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("initial renewal run did not execute")
	}
	s.Stop()
	s.Stop()
	s.Start(context.Background())
	select {
	case <-calls:
		t.Fatal("repeated Start created another renewal worker")
	default:
	}
}

func TestAccountBillingSchedulerStopCancelsAndWaitsForActiveRun(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	s := newAccountBillingScheduler(func(ctx context.Context) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	s.interval = time.Millisecond
	s.timeout = time.Hour
	t.Cleanup(s.Stop)
	s.Start(context.Background())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial renewal run did not execute")
	}
	s.Stop()
	select {
	case <-finished:
	default:
		t.Fatal("Stop returned before the active renewal run exited")
	}
	require.EqualValues(t, 1, calls.Load(), "ticker must not overlap an active renewal run")
}

func TestAccountBillingSchedulerRunHasDeadline(t *testing.T) {
	result := make(chan error, 1)
	s := newAccountBillingScheduler(func(ctx context.Context) error {
		<-ctx.Done()
		result <- ctx.Err()
		return ctx.Err()
	})
	s.interval = time.Hour
	s.timeout = 10 * time.Millisecond
	t.Cleanup(s.Stop)
	s.Start(context.Background())
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("renewal operation did not receive its timeout")
	}
}

func TestAccountBillingSchedulerStopBeforeStartIsSafe(t *testing.T) {
	var calls atomic.Int32
	s := newAccountBillingScheduler(func(context.Context) error {
		calls.Add(1)
		return nil
	})
	s.Stop()
	s.Stop()
	s.Start(context.Background())
	require.False(t, s.started)
	require.Zero(t, calls.Load())
}
