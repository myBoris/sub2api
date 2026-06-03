package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

type RedeemCode struct {
	ID            int64
	Code          string
	Type          string
	Value         float64
	BalanceSource string
	Status        string
	UsedBy        *int64
	UsedAt        *time.Time
	Notes         string
	CreatedAt     time.Time
	ExpiresAt     *time.Time

	GroupID      *int64
	ValidityDays int

	User  *User
	Group *Group
}

func (r *RedeemCode) IsUsed() bool {
	return r.Status == StatusUsed
}

func (r *RedeemCode) IsExpired() bool {
	return r.IsExpiredAt(time.Now())
}

func (r *RedeemCode) IsExpiredAt(now time.Time) bool {
	if r == nil {
		return false
	}
	if r.Status == StatusExpired {
		return true
	}
	return r.Status == StatusUnused && r.ExpiresAt != nil && !r.ExpiresAt.After(now)
}

func (r *RedeemCode) CanUse() bool {
	return r.Status == StatusUnused && !r.IsExpired()
}

func GenerateRedeemCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func normalizeBalanceSourceOrPaid(source string) string {
	return BalanceSourceOrPaid(source)
}

func BalanceSourceOrPaid(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case BalanceSourceGift:
		return BalanceSourceGift
	default:
		return BalanceSourcePaid
	}
}

func NormalizeGroupBalanceTier(tier string) string {
	switch strings.TrimSpace(strings.ToLower(tier)) {
	case GroupBalanceTierPlus:
		return GroupBalanceTierPlus
	default:
		return GroupBalanceTierFree
	}
}

func applyBalanceSourceTotals(user *User, amount float64, source string) {
	if user == nil || amount <= 0 {
		return
	}
	switch normalizeBalanceSourceOrPaid(source) {
	case BalanceSourceGift:
		user.TotalGifted += amount
		user.GiftBalance += amount
	default:
		user.TotalRecharged += amount
		user.PaidBalance += amount
	}
}

func applyBalanceBucketAdjustment(user *User, amount float64, source string) {
	if user == nil || amount == 0 {
		return
	}
	if amount > 0 {
		applyBalanceSourceTotals(user, amount, source)
		return
	}
	deductFromUserBalanceBuckets(user, -amount, GroupBalanceTierFree)
}

func clampUserBalanceBuckets(user *User) {
	if user == nil {
		return
	}
	if user.PaidBalance < 0 {
		user.PaidBalance = 0
	}
	if user.GiftBalance < 0 {
		user.GiftBalance = 0
	}
	if user.Balance <= 0 {
		user.PaidBalance = 0
		user.GiftBalance = 0
		return
	}
	if user.PaidBalance > user.Balance {
		user.PaidBalance = user.Balance
		user.GiftBalance = 0
		return
	}
	remaining := user.Balance - user.PaidBalance
	if user.GiftBalance > remaining {
		user.GiftBalance = remaining
	}
}

func deductFromUserBalanceBuckets(user *User, amount float64, tier string) {
	if user == nil || amount <= 0 {
		return
	}
	if NormalizeGroupBalanceTier(tier) == GroupBalanceTierPlus {
		user.PaidBalance -= amount
		return
	}
	user.GiftBalance -= amount
}

type balanceSourceUserRepository interface {
	UpdateBalanceWithSource(ctx context.Context, id int64, amount float64, source string) error
}

type balanceTierUserRepository interface {
	DeductBalanceWithTier(ctx context.Context, id int64, amount float64, tier string) error
}

func updateUserBalanceWithSource(ctx context.Context, repo UserRepository, userID int64, amount float64, source string) error {
	if repoWithSource, ok := repo.(balanceSourceUserRepository); ok {
		return repoWithSource.UpdateBalanceWithSource(ctx, userID, amount, source)
	}
	return repo.UpdateBalance(ctx, userID, amount)
}

func deductUserBalanceWithTier(ctx context.Context, repo UserRepository, userID int64, amount float64, tier string) error {
	if repoWithTier, ok := repo.(balanceTierUserRepository); ok {
		return repoWithTier.DeductBalanceWithTier(ctx, userID, amount, tier)
	}
	return repo.DeductBalance(ctx, userID, amount)
}
