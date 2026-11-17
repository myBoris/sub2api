package ownership

import "context"

type groupUserKey struct{}

// WithGroupUser confines gateway group resolution without limiting the accounts
// that an administrator has intentionally attached to an owned group.
func WithGroupUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, groupUserKey{}, userID)
}

func GroupUserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(groupUserKey{}).(int64)
	return id, ok && id > 0
}
