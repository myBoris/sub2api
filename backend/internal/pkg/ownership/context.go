// Package ownership carries the resource owner for the user management extension.
// An absent scope preserves the upstream administrator and background-job behavior.
package ownership

import "context"

const (
	GroupTable   = "custom_user_groups"
	AccountTable = "custom_user_accounts"
)

type userKey struct{}

func WithUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

func UserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userKey{}).(int64)
	return id, ok && id > 0
}
