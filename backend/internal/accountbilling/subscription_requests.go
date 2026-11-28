package accountbilling

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrSubscriptionRequestPending = errors.New("a subscription request is already pending")
	ErrSubscriptionRequestClosed  = errors.New("the subscription request is already completed")
)

// Public subscription DTOs deliberately have no cost or applicant identity.
type SubscriptionPlan struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Price    Money  `json:"price"`
}

type SubscriptionRequest struct {
	ID          int64      `json:"id"`
	PlanID      int64      `json:"plan_id"`
	Platform    string     `json:"platform"`
	PlanName    string     `json:"plan_name"`
	Price       Money      `json:"price"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type AdminSubscriptionRequest struct {
	SubscriptionRequest
	UserID      int64  `json:"user_id"`
	UserName    string `json:"user_name"`
	UserEmail   string `json:"user_email"`
	Cost        Money  `json:"cost"`
	CompletedBy *int64 `json:"completed_by"`
}

// Recheck the actor's database role and active state for every operation. New
// requests serialize on their owner to make concurrent POSTs return one pending
// task. Edits consistently lock actor, then request, then selected plan.
func lockSubscriptionActor(ctx context.Context, tx *sql.Tx, userID int64, role string, create bool) error {
	if userID <= 0 {
		return ErrForbidden
	}
	query := `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' AND role=$2 FOR SHARE`
	if create {
		query = `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' AND role=$2 FOR NO KEY UPDATE`
	}
	var id int64
	err := tx.QueryRowContext(ctx, query, userID, role).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}

func (s *Store) SubscriptionPlans(ctx context.Context, userID int64) ([]SubscriptionPlan, error) {
	result := []SubscriptionPlan{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, userID, "user", false); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,platform,name,price_cents FROM custom_billing_plans ORDER BY platform,name,id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var plan SubscriptionPlan
		if err = rows.Scan(&plan.ID, &plan.Platform, &plan.Name, &plan.Price); err != nil {
			_ = rows.Close()
			return result, err
		}
		result = append(result, plan)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

const subscriptionRequestColumns = `r.id,r.plan_id,r.platform,r.plan_name,r.price_cents,r.status,r.created_at,r.updated_at,r.completed_at`

func scanSubscriptionRequest(row interface{ Scan(...any) error }) (SubscriptionRequest, error) {
	var request SubscriptionRequest
	err := row.Scan(&request.ID, &request.PlanID, &request.Platform, &request.PlanName, &request.Price,
		&request.Status, &request.CreatedAt, &request.UpdatedAt, &request.CompletedAt)
	return request, databaseError(err)
}

func (s *Store) UserSubscriptionRequests(ctx context.Context, userID int64) ([]SubscriptionRequest, error) {
	result := []SubscriptionRequest{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, userID, "user", false); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+subscriptionRequestColumns+` FROM custom_subscription_requests r WHERE r.user_id=$1 ORDER BY r.created_at DESC,r.id DESC`, userID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		request, scanErr := scanSubscriptionRequest(rows)
		if scanErr != nil {
			_ = rows.Close()
			return result, scanErr
		}
		result = append(result, request)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func subscriptionPlanSnapshot(ctx context.Context, tx *sql.Tx, planID int64) (Plan, error) {
	var plan Plan
	err := tx.QueryRowContext(ctx, `SELECT id,platform,name,price_cents,cost_cents FROM custom_billing_plans WHERE id=$1 FOR SHARE`, planID).
		Scan(&plan.ID, &plan.Platform, &plan.Name, &plan.Price, &plan.Cost)
	return plan, databaseError(err)
}

func (s *Store) CreateSubscriptionRequest(ctx context.Context, userID, planID int64) (SubscriptionRequest, error) {
	if userID <= 0 || planID <= 0 {
		return SubscriptionRequest{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, userID, "user", true); err != nil {
		return SubscriptionRequest{}, err
	}
	request, err := scanSubscriptionRequest(tx.QueryRowContext(ctx, `SELECT `+subscriptionRequestColumns+` FROM custom_subscription_requests r WHERE r.user_id=$1 AND r.status='pending' FOR NO KEY UPDATE OF r`, userID))
	if err == nil {
		if request.PlanID != planID {
			return SubscriptionRequest{}, ErrSubscriptionRequestPending
		}
		return request, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return SubscriptionRequest{}, err
	}
	plan, err := subscriptionPlanSnapshot(ctx, tx, planID)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO custom_subscription_requests(user_id,plan_id,platform,plan_name,price_cents,cost_cents)
		VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at,updated_at`, userID, plan.ID, plan.Platform, plan.Name, plan.Price, plan.Cost).
		Scan(&request.ID, &request.CreatedAt, &request.UpdatedAt)
	if err != nil {
		return SubscriptionRequest{}, databaseError(err)
	}
	request.PlanID, request.Platform, request.PlanName, request.Price, request.Status = plan.ID, plan.Platform, plan.Name, plan.Price, "pending"
	return request, tx.Commit()
}

func (s *Store) UpdateSubscriptionRequest(ctx context.Context, userID, requestID, planID int64) (SubscriptionRequest, error) {
	if userID <= 0 || requestID <= 0 || planID <= 0 {
		return SubscriptionRequest{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, userID, "user", false); err != nil {
		return SubscriptionRequest{}, err
	}
	request, err := scanSubscriptionRequest(tx.QueryRowContext(ctx, `SELECT `+subscriptionRequestColumns+` FROM custom_subscription_requests r WHERE r.id=$1 AND r.user_id=$2 FOR NO KEY UPDATE OF r`, requestID, userID))
	if err != nil {
		return SubscriptionRequest{}, err
	}
	if request.Status != "pending" {
		return SubscriptionRequest{}, ErrSubscriptionRequestClosed
	}
	plan, err := subscriptionPlanSnapshot(ctx, tx, planID)
	if err != nil {
		return SubscriptionRequest{}, err
	}
	err = tx.QueryRowContext(ctx, `UPDATE custom_subscription_requests SET plan_id=$1,platform=$2,plan_name=$3,price_cents=$4,cost_cents=$5,updated_at=NOW()
		WHERE id=$6 AND status='pending' RETURNING updated_at`, plan.ID, plan.Platform, plan.Name, plan.Price, plan.Cost, requestID).Scan(&request.UpdatedAt)
	if err != nil {
		return SubscriptionRequest{}, databaseError(err)
	}
	request.PlanID, request.Platform, request.PlanName, request.Price = plan.ID, plan.Platform, plan.Name, plan.Price
	return request, tx.Commit()
}

const adminSubscriptionRequestColumns = subscriptionRequestColumns + `,r.user_id,COALESCE(NULLIF(u.username,''),u.email),u.email,r.cost_cents,r.completed_by`

func scanAdminSubscriptionRequest(row interface{ Scan(...any) error }) (AdminSubscriptionRequest, error) {
	var request AdminSubscriptionRequest
	err := row.Scan(&request.ID, &request.PlanID, &request.Platform, &request.PlanName, &request.Price,
		&request.Status, &request.CreatedAt, &request.UpdatedAt, &request.CompletedAt,
		&request.UserID, &request.UserName, &request.UserEmail, &request.Cost, &request.CompletedBy)
	return request, databaseError(err)
}

func (s *Store) AdminSubscriptionRequests(ctx context.Context, adminID int64, status, search string) ([]AdminSubscriptionRequest, error) {
	result := []AdminSubscriptionRequest{}
	if status != "" && status != "pending" && status != "completed" {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, adminID, "admin", false); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+adminSubscriptionRequestColumns+` FROM custom_subscription_requests r JOIN users u ON u.id=r.user_id
		WHERE ($1='' OR r.status=$1) AND ($2='' OR strpos(lower(u.username||' '||u.email||' '||r.platform||' '||r.plan_name),lower($2))>0)
		ORDER BY CASE WHEN r.status='pending' THEN 0 ELSE 1 END,r.created_at DESC,r.id DESC`, status, strings.TrimSpace(search))
	if err != nil {
		return result, err
	}
	for rows.Next() {
		request, scanErr := scanAdminSubscriptionRequest(rows)
		if scanErr != nil {
			_ = rows.Close()
			return result, scanErr
		}
		result = append(result, request)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// Completing a request records an administrator's manual task only. It never
// creates accounts, changes balances or subscriptions, or sends notifications.
func (s *Store) CompleteSubscriptionRequest(ctx context.Context, adminID, requestID int64, expectedUpdate ...time.Time) (AdminSubscriptionRequest, error) {
	if adminID <= 0 || requestID <= 0 {
		return AdminSubscriptionRequest{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminSubscriptionRequest{}, err
	}
	defer tx.Rollback()
	if err = lockSubscriptionActor(ctx, tx, adminID, "admin", false); err != nil {
		return AdminSubscriptionRequest{}, err
	}
	request, err := scanAdminSubscriptionRequest(tx.QueryRowContext(ctx, `SELECT `+adminSubscriptionRequestColumns+`
		FROM custom_subscription_requests r JOIN users u ON u.id=r.user_id WHERE r.id=$1 FOR NO KEY UPDATE OF r`, requestID))
	if err != nil {
		return AdminSubscriptionRequest{}, err
	}
	if request.Status == "completed" {
		return request, tx.Commit()
	}
	if len(expectedUpdate) > 0 && (expectedUpdate[0].IsZero() || !request.UpdatedAt.Equal(expectedUpdate[0])) {
		return AdminSubscriptionRequest{}, ErrConflict
	}
	err = tx.QueryRowContext(ctx, `UPDATE custom_subscription_requests SET status='completed',completed_at=NOW(),completed_by=$1,updated_at=NOW()
		WHERE id=$2 AND status='pending' RETURNING completed_at,updated_at`, adminID, requestID).Scan(&request.CompletedAt, &request.UpdatedAt)
	if err != nil {
		return AdminSubscriptionRequest{}, databaseError(err)
	}
	request.Status, request.CompletedBy = "completed", &adminID
	return request, tx.Commit()
}
