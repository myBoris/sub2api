package service

import (
	"context"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
)

// ResourceOwner is stored in the isolated ownership extension, not upstream
// resource schemas. A null UserID denotes a resource managed by administrators.
type ResourceOwner struct {
	ResourceID int64  `json:"resource_id"`
	UserID     *int64 `json:"user_id"`
	Email      string `json:"email"`
	Username   string `json:"username"`
}

// ResourceOwnerService is optional so the upstream AdminService interface and
// its many adapters do not need to change.
type ResourceOwnerService interface {
	ResourceOwners(context.Context, string, []int64) ([]ResourceOwner, error)
	SetResourceOwner(context.Context, string, int64, *int64) (*ResourceOwner, error)
}

var (
	ErrResourceOwnerForbidden      = infraerrors.New(http.StatusForbidden, "RESOURCE_OWNER_FORBIDDEN", "只有管理员可以更改资源所属用户")
	ErrResourceOwnerInvalid        = infraerrors.New(http.StatusBadRequest, "RESOURCE_OWNER_INVALID", "资源类型或所属用户无效")
	ErrResourceOwnerConflict       = infraerrors.New(http.StatusConflict, "RESOURCE_OWNER_REFERENCES_CONFLICT", "请先移除与目标所属用户不一致的模型路由、固定账号、兜底分组或母子账号引用，再更改所属用户")
	ErrResourceOwnerConnectionPool = infraerrors.New(http.StatusServiceUnavailable, "RESOURCE_OWNER_CONNECTION_POOL", "用户资源写入保护至少需要两个数据库连接，请提高数据库最大连接数")
)

type resourceOwnerRepository interface {
	ResourceOwners(context.Context, string, []int64) ([]ResourceOwner, error)
	TransferResourceOwner(context.Context, string, int64, *int64) (*ResourceOwner, []int64, error)
}

// WithResourceOwnershipReadLock is an optional handler guard. Hold it from
// ownership validation through the end of an ordinary management write so an
// administrative transfer cannot pass between the check and the mutation.
func (s *adminServiceImpl) WithResourceOwnershipReadLock(ctx context.Context, fn func() error) error {
	if _, scoped := ownership.UserID(ctx); !scoped {
		return fn()
	}
	if repo, ok := s.groupRepo.(interface {
		WithResourceOwnershipReadLock(context.Context, func() error) error
	}); ok {
		return repo.WithResourceOwnershipReadLock(ctx, fn)
	}
	return fn()
}

func (s *adminServiceImpl) ResourceOwners(ctx context.Context, kind string, ids []int64) ([]ResourceOwner, error) {
	repo, ok := s.groupRepo.(resourceOwnerRepository)
	if !ok {
		return nil, infraerrors.New(http.StatusNotImplemented, "RESOURCE_OWNER_UNAVAILABLE", "Resource ownership is unavailable")
	}
	return repo.ResourceOwners(ctx, kind, ids)
}

func (s *adminServiceImpl) SetResourceOwner(ctx context.Context, kind string, id int64, userID *int64) (*ResourceOwner, error) {
	if _, scoped := ownership.UserID(ctx); scoped {
		return nil, ErrResourceOwnerForbidden
	}
	repo, ok := s.groupRepo.(resourceOwnerRepository)
	if !ok {
		return nil, infraerrors.New(http.StatusNotImplemented, "RESOURCE_OWNER_UNAVAILABLE", "Resource ownership is unavailable")
	}
	owner, accounts, err := repo.TransferResourceOwner(ctx, kind, id, userID)
	if err != nil {
		return nil, err
	}
	// TransferResourceOwner commits before returning. Match the normal binding
	// APIs by refreshing affected account snapshots in addition to durable outbox.
	if refresher, ok := s.accountRepo.(interface {
		RefreshResourceOwnerAccounts(context.Context, []int64)
	}); ok {
		refresher.RefreshResourceOwnerAccounts(ctx, accounts)
	}
	if kind == "groups" && s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, id)
	}
	return owner, nil
}
