package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/stretchr/testify/require"
)

type ownedUsageSnapshotRepo struct {
	AccountRepository
	calls  int
	ctx    context.Context
	result map[string]any
}

func (r *ownedUsageSnapshotRepo) UpdateExtra(ctx context.Context, _ int64, updates map[string]any) error {
	r.ctx = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls++
	r.result = updates
	return nil
}

func TestUserOwnedUsageSnapshotPersistsBeforeGuardReturns(t *testing.T) {
	ctx := ownership.WithUser(context.Background(), 1)
	repo := &ownedUsageSnapshotRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	updates := map[string]any{"codex_5h_used_percent": 25.0}
	svc.persistOpenAICodexProbeSnapshotContext(ctx, 101, updates)
	require.Equal(t, 1, repo.calls, "quota persistence must finish before the caller can release its ownership guard")
	require.Equal(t, updates, repo.result)
	owner, scoped := ownership.UserID(repo.ctx)
	require.True(t, scoped)
	require.Equal(t, int64(1), owner)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	svc.persistOpenAICodexProbeSnapshotContext(cancelled, 101, updates)
	require.Equal(t, 1, repo.calls, "a cancelled request must not continue writing after its guard releases")
	require.ErrorIs(t, repo.ctx.Err(), context.Canceled)
}
