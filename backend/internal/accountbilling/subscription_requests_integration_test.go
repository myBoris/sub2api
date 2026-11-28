package accountbilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func subscriptionRequestTestDatabase(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	db, store := balancePaymentTestDatabase(t)
	_, err := db.Exec(`ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '', ADD COLUMN username TEXT NOT NULL DEFAULT '';
		INSERT INTO users(id,status,role,email,username) VALUES(1,'active','admin','admin@example.com','Admin'),
		(2,'active','admin','second-admin@example.com','Second'),(7,'active','user','user@example.com','Applicant'),
		(8,'active','user','other@example.com','Other'),(9,'disabled','user','disabled@example.com','Disabled');
		INSERT INTO custom_billing_plans(id,platform,name,price_cents,cost_cents) VALUES(5,'openai','Plus',12345,6500),(6,'anthropic','Pro',23456,17000);`)
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "259_custom_subscription_requests.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db, store
}

func TestSubscriptionRequestPostgresLifecycleSnapshotsAndIsolation(t *testing.T) {
	db, store := subscriptionRequestTestDatabase(t)
	ctx := context.Background()
	plans, err := store.SubscriptionPlans(ctx, 7)
	require.NoError(t, err)
	require.Len(t, plans, 2)
	raw, err := json.Marshal(plans)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "cost")
	request, err := store.CreateSubscriptionRequest(ctx, 7, 5)
	require.NoError(t, err)
	require.Equal(t, Money(12345), request.Price)
	require.Equal(t, "pending", request.Status)
	_, err = db.Exec(`UPDATE custom_billing_plans SET name='Renamed',price_cents=14000,cost_cents=7000 WHERE id=5`)
	require.NoError(t, err)
	replay, err := store.CreateSubscriptionRequest(ctx, 7, 5)
	require.NoError(t, err)
	require.Equal(t, request, replay, "a retry returns its original snapshot")
	_, err = store.CreateSubscriptionRequest(ctx, 7, 6)
	require.ErrorIs(t, err, ErrSubscriptionRequestPending)
	_, err = store.UpdateSubscriptionRequest(ctx, 8, request.ID, 6)
	require.ErrorIs(t, err, ErrNotFound)
	other, err := store.UserSubscriptionRequests(ctx, 8)
	require.NoError(t, err)
	require.Empty(t, other)
	_, err = store.UpdateSubscriptionRequest(ctx, 7, request.ID, 999)
	require.ErrorIs(t, err, ErrNotFound)
	updated, err := store.UpdateSubscriptionRequest(ctx, 7, request.ID, 6)
	require.NoError(t, err)
	require.Equal(t, Money(23456), updated.Price)
	require.Equal(t, "Pro", updated.PlanName)
	admin, err := store.AdminSubscriptionRequests(ctx, 1, "pending", "appLICant")
	require.NoError(t, err)
	require.Len(t, admin, 1)
	require.Equal(t, Money(17000), admin[0].Cost)
	require.Equal(t, "user@example.com", admin[0].UserEmail)
	_, err = store.CompleteSubscriptionRequest(ctx, 1, request.ID, request.UpdatedAt)
	require.ErrorIs(t, err, ErrConflict, "an administrator must refresh when the applicant changed the displayed plan")
	completed, err := store.CompleteSubscriptionRequest(ctx, 1, request.ID, updated.UpdatedAt)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, int64(1), *completed.CompletedBy)
	repeated, err := store.CompleteSubscriptionRequest(ctx, 2, request.ID, request.UpdatedAt)
	require.NoError(t, err)
	require.Equal(t, completed, repeated, "completion retries preserve the original operator and timestamps")
	_, err = store.UpdateSubscriptionRequest(ctx, 7, request.ID, 5)
	require.ErrorIs(t, err, ErrSubscriptionRequestClosed)
	_, err = db.Exec(`UPDATE custom_billing_plans SET price_cents=1,cost_cents=1 WHERE id=6`)
	require.NoError(t, err)
	list, err := store.UserSubscriptionRequests(ctx, 7)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, Money(23456), list[0].Price)
	raw, err = json.Marshal(list)
	require.NoError(t, err)
	for _, private := range []string{"cost", "user_id", "user_name", "user_email", "completed_by"} {
		require.NotContains(t, string(raw), private)
	}
	require.ErrorIs(t, store.DeleteCatalog(ctx, "plans", 6), ErrConflict)
	newRequest, err := store.CreateSubscriptionRequest(ctx, 7, 5)
	require.NoError(t, err)
	require.NotEqual(t, request.ID, newRequest.ID)
	require.Equal(t, Money(14000), newRequest.Price)
	var changes int
	require.NoError(t, db.QueryRow(`SELECT (SELECT count(*) FROM accounts)+(SELECT count(*) FROM payment_orders)+(SELECT count(*) FROM redeem_codes)+(SELECT count(*) FROM users WHERE balance<>0 OR total_recharged<>0)`).Scan(&changes))
	require.Zero(t, changes, "manual completion creates no account, payment, recharge, or wallet side effects")
}

func TestSubscriptionRequestPostgresRoleChecksAndConcurrentTransitions(t *testing.T) {
	db, store := subscriptionRequestTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, actor := range []int64{1, 9, 999} {
		_, err := store.CreateSubscriptionRequest(ctx, actor, 5)
		require.ErrorIs(t, err, ErrForbidden)
		_, err = store.SubscriptionPlans(ctx, actor)
		require.ErrorIs(t, err, ErrForbidden)
	}
	_, err := store.AdminSubscriptionRequests(ctx, 7, "", "")
	require.ErrorIs(t, err, ErrForbidden)
	_, err = store.CompleteSubscriptionRequest(ctx, 7, 1)
	require.ErrorIs(t, err, ErrForbidden)
	var group sync.WaitGroup
	results := make(chan SubscriptionRequest, 12)
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			request, createErr := store.CreateSubscriptionRequest(ctx, 7, 5)
			results <- request
			errCh <- createErr
		}()
	}
	group.Wait()
	var request SubscriptionRequest
	for i := 0; i < 12; i++ {
		require.NoError(t, <-errCh)
		next := <-results
		if request.ID != 0 {
			require.Equal(t, request.ID, next.ID)
		}
		request = next
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM custom_subscription_requests WHERE user_id=7 AND status='pending'`).Scan(&count))
	require.Equal(t, 1, count)
	group.Add(2)
	go func() {
		defer group.Done()
		_, updateErr := store.UpdateSubscriptionRequest(ctx, 7, request.ID, 6)
		errCh <- updateErr
	}()
	go func() {
		defer group.Done()
		_, completeErr := store.CompleteSubscriptionRequest(ctx, 1, request.ID)
		errCh <- completeErr
	}()
	group.Wait()
	for i := 0; i < 2; i++ {
		err = <-errCh
		require.True(t, err == nil || errors.Is(err, ErrSubscriptionRequestClosed), "%v", err)
	}
	completed, err := store.CompleteSubscriptionRequest(ctx, 2, request.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), *completed.CompletedBy)
	if completed.PlanID == 5 {
		require.Equal(t, Money(12345), completed.Price)
		require.Equal(t, Money(6500), completed.Cost)
	} else {
		require.Equal(t, int64(6), completed.PlanID)
		require.Equal(t, Money(23456), completed.Price)
		require.Equal(t, Money(17000), completed.Cost)
	}
	_, err = store.UpdateSubscriptionRequest(ctx, 7, request.ID, 5)
	require.ErrorIs(t, err, ErrSubscriptionRequestClosed)
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=1`)
	require.NoError(t, err)
	_, err = store.CompleteSubscriptionRequest(ctx, 1, request.ID)
	require.ErrorIs(t, err, ErrForbidden, "a stale administrator session cannot bypass the live database state")
}

func TestSubscriptionRequestPostgresPlanDeletionDoesNotDeadlockEditing(t *testing.T) {
	db, store := subscriptionRequestTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := store.CreateSubscriptionRequest(ctx, 7, 5)
	require.NoError(t, err)
	// Reproduce the formerly conflicting lock order: editing owns a child lock
	// while a catalog deletion owns the parent plan's update lock. The FK's key
	// share check must remain compatible with the request's no-key update lock.
	edit, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer edit.Rollback()
	_, err = edit.ExecContext(ctx, `SELECT id FROM custom_subscription_requests WHERE id=$1 FOR NO KEY UPDATE`, request.ID)
	require.NoError(t, err)
	deletion, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer deletion.Rollback()
	_, err = deletion.ExecContext(ctx, `SELECT id FROM custom_billing_plans WHERE id=5 FOR UPDATE`)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, deleteErr := deletion.ExecContext(ctx, `DELETE FROM custom_billing_plans WHERE id=5`)
		done <- deleteErr
	}()
	select {
	case err = <-done:
		require.ErrorIs(t, databaseError(err), ErrConflict)
	case <-ctx.Done():
		t.Fatal("deleting a referenced plan waited for the pending request's non-key lock")
	}
	require.NoError(t, deletion.Rollback())
	_, err = edit.ExecContext(ctx, `SELECT id FROM custom_billing_plans WHERE id=5 FOR SHARE`)
	require.NoError(t, err)
	require.NoError(t, edit.Commit())
	updated, err := store.UpdateSubscriptionRequest(ctx, 7, request.ID, 5)
	require.NoError(t, err)
	require.Equal(t, request.PlanID, updated.PlanID)
}
