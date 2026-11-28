package accountbilling

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRequestPublicDTOOmitsFinancialCostAndApplicantDetails(t *testing.T) {
	admin := AdminSubscriptionRequest{SubscriptionRequest: SubscriptionRequest{ID: 12, PlanID: 5, Platform: "openai", PlanName: "Plus", Price: 12345, Status: "pending"},
		UserID: 7, UserName: "Private name", UserEmail: "private@example.com", Cost: 6500, CompletedBy: billingTestInt(1)}
	raw, err := json.Marshal(admin.SubscriptionRequest)
	require.NoError(t, err)
	for _, field := range []string{"cost", "user_id", "user_name", "user_email", "completed_by", "private@example.com"} {
		require.NotContains(t, string(raw), field)
	}
	raw, err = json.Marshal(SubscriptionPlan{ID: 5, Platform: "openai", Name: "Plus", Price: 12345})
	require.NoError(t, err)
	require.JSONEq(t, `{"id":5,"platform":"openai","name":"Plus","price":123.45}`, string(raw))
}

func TestSubscriptionRequestsRecheckDatabaseRoleAndStatusBeforeDataAccess(t *testing.T) {
	for _, role := range []string{"user", "admin"} {
		for _, operation := range []string{"list", "mutate"} {
			t.Run(role+"/"+operation, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer db.Close()
				store := NewStore(db, "UTC")
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role=\$2 FOR (SHARE|NO KEY UPDATE)`).
					WithArgs(int64(7), role).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectRollback()
				if role == "user" && operation == "list" {
					_, err = store.UserSubscriptionRequests(context.Background(), 7)
				} else if role == "user" {
					_, err = store.CreateSubscriptionRequest(context.Background(), 7, 5)
				} else if operation == "list" {
					_, err = store.AdminSubscriptionRequests(context.Background(), 7, "", "")
				} else {
					_, err = store.CompleteSubscriptionRequest(context.Background(), 7, 12)
				}
				require.ErrorIs(t, err, ErrForbidden)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
