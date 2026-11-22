// Package accountbilling implements the isolated subscription collection ledger.
package accountbilling

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrNotFound  = errors.New("billing record not found")
	ErrConflict  = errors.New("name already exists or record is still in use")
	ErrForbidden = errors.New("billing summary is only available to active ordinary users")
	ErrInvalid   = errors.New("invalid billing input")
)

// Money is an exact number of CNY cents. Its JSON representation is a yuan
// number, matching the UI while retaining exact arithmetic in the database.
type Money int64

func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d.%02d", int64(m)/100, int64(m)%100)), nil
}

func (m *Money) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) || len(raw) == 0 || len(raw) > 64 || raw[0] == '"' {
		return fmt.Errorf("%w: amount must be a number", ErrInvalid)
	}
	d, err := decimal.NewFromString(string(raw))
	if err != nil || d.Exponent() < -20 || d.Exponent() > 20 || d.IsNegative() || d.GreaterThan(decimal.New(999999999999, -2)) {
		return fmt.Errorf("%w: amount must be between 0 and 9999999999.99", ErrInvalid)
	}
	cents := d.Shift(2)
	if !cents.Equal(cents.Truncate(0)) {
		return fmt.Errorf("%w: amount supports at most two decimal places", ErrInvalid)
	}
	*m = Money(cents.IntPart())
	return nil
}

type Plan struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Price    Money  `json:"price"`
}

type Payer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Catalog struct {
	Plans  []Plan  `json:"plans"`
	Payers []Payer `json:"payers"`
}

type History struct {
	Month     string `json:"month"`
	Status    string `json:"status"`
	Amount    Money  `json:"amount"`
	PayerID   *int64 `json:"payer_id,omitempty"`
	PayerName string `json:"payer_name,omitempty"`
}

type Account struct {
	AccessState
	AccountID     int64     `json:"account_id"`
	AccountName   string    `json:"account_name"`
	Platform      string    `json:"platform"`
	RenewalDate   *string   `json:"renewal_date"`
	PlanID        *int64    `json:"plan_id"`
	PlanName      string    `json:"plan_name"`
	Price         Money     `json:"price"`
	PayerID       *int64    `json:"payer_id"`
	PayerName     string    `json:"payer_name"`
	PaymentStatus string    `json:"payment_status"`
	History       []History `json:"history"`
	createdAt     time.Time
}

type AccountUpdate struct {
	RenewalDate *string `json:"renewal_date"`
	// Explicit false means the form did not change its original date. This
	// protects a recurring anchor when a scheduler has already rolled that date.
	// Omitted retains the date-based behavior for existing API clients.
	RenewalDateChanged *bool  `json:"renewal_date_changed,omitempty"`
	PlanID             *int64 `json:"plan_id"`
	Price              Money  `json:"price"`
	PayerID            *int64 `json:"payer_id"`
	PaymentStatus      string `json:"payment_status"`
}

type PayerTotal struct {
	PayerID   *int64 `json:"payer_id"`
	PayerName string `json:"payer_name"`
	Total     Money  `json:"total"`
}

type Summary struct {
	PaidCount    int          `json:"paid_count"`
	UnpaidCount  int          `json:"unpaid_count"`
	DueSoonCount int          `json:"due_soon_count"`
	Payers       []PayerTotal `json:"payers"`
}

type AccountList struct {
	Items   []Account `json:"items"`
	Month   string    `json:"month"`
	Summary Summary   `json:"summary"`
}

// UserBillingAccount contains only the fields needed for the header reminder.
// Internal plan, payer, account names and historical records stay administrator-only.
type UserBillingAccount struct {
	AccessState
	AccountID     int64   `json:"account_id"`
	RenewalDate   *string `json:"renewal_date"`
	Price         Money   `json:"price"`
	PaymentStatus string  `json:"payment_status"`
}

type UserBillingSummary struct {
	Month       string               `json:"month"`
	AmountDue   Money                `json:"amount_due"`
	TotalAmount Money                `json:"total_amount"`
	Accounts    []UserBillingAccount `json:"accounts"`
}

func ValidMonth(month string) bool {
	t, err := time.Parse("2006-01", month)
	return err == nil && t.Year() >= 2000 && t.Format("2006-01") == month
}

func validStatus(status string) bool { return status == "paid" || status == "unpaid" }

func ValidateUpdate(u AccountUpdate) error {
	if !validStatus(u.PaymentStatus) || u.Price < 0 || u.Price > 999999999999 || (u.PlanID != nil && *u.PlanID <= 0) || (u.PayerID != nil && *u.PayerID <= 0) {
		return fmt.Errorf("%w: invalid payment status, amount, plan or payer", ErrInvalid)
	}
	if u.RenewalDate != nil {
		t, err := time.Parse("2006-01-02", *u.RenewalDate)
		if err != nil || t.Year() < 2000 || t.Format("2006-01-02") != *u.RenewalDate {
			return fmt.Errorf("%w: renewal date must be YYYY-MM-DD", ErrInvalid)
		}
	}
	return nil
}

func validName(s string) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= 100 }

// Fill every elapsed month, preserving paid records and the amount/payer that
// were recorded for earlier months. A new month always starts unpaid.
func fillHistory(a *Account, now time.Time) bool {
	existing := make(map[string]bool, len(a.History))
	for _, h := range a.History {
		existing[h.Month] = true
	}
	first := a.createdAt.In(now.Location())
	month := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, now.Location())
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	changed := false
	for !month.After(last) {
		key := month.Format("2006-01")
		if !existing[key] {
			a.History = append(a.History, History{Month: key, Status: "unpaid", Amount: a.Price, PayerID: a.PayerID, PayerName: a.PayerName})
			changed = true
		}
		month = month.AddDate(0, 1, 0)
	}
	sort.Slice(a.History, func(i, j int) bool { return a.History[i].Month > a.History[j].Month })
	return changed
}

func historyFor(a Account, month string) (History, bool) {
	for _, h := range a.History {
		if h.Month == month {
			return h, true
		}
	}
	return History{}, false
}

// Historical payer names are snapshots; the current month reflects a renamed
// payer immediately, provided that the underlying assignment is unchanged.
func applyHistory(a *Account, entry History, current bool) {
	name := entry.PayerName
	if current && ((a.PayerID == nil && entry.PayerID == nil) || (a.PayerID != nil && entry.PayerID != nil && *a.PayerID == *entry.PayerID)) {
		name = a.PayerName
	}
	a.Price = entry.Amount
	a.PaymentStatus = entry.Status
	a.PayerID = entry.PayerID
	a.PayerName = name
}

func setHistory(a *Account, entry History) {
	for i := range a.History {
		if a.History[i].Month == entry.Month {
			a.History[i] = entry
			return
		}
	}
	a.History = append(a.History, entry)
	sort.Slice(a.History, func(i, j int) bool { return a.History[i].Month > a.History[j].Month })
}

func historyJSON(history []History) ([]byte, error) {
	if history == nil {
		history = []History{}
	}
	return json.Marshal(history)
}

func summarize(accounts []Account, now time.Time, payers []Payer) Summary {
	s := Summary{Payers: make([]PayerTotal, 0, len(payers)+1)}
	totals := make(map[int64]Money)
	for _, a := range accounts {
		if a.PaymentStatus == "paid" {
			s.PaidCount++
		} else {
			s.UnpaidCount++
		}
		if a.RenewalDate != nil && *a.RenewalDate >= now.Format("2006-01-02") && *a.RenewalDate <= now.AddDate(0, 0, 3).Format("2006-01-02") {
			s.DueSoonCount++
		}
		id := int64(0)
		if a.PayerID != nil {
			id = *a.PayerID
		}
		totals[id] += a.Price
	}
	for _, payer := range payers {
		id := payer.ID
		s.Payers = append(s.Payers, PayerTotal{PayerID: &id, PayerName: payer.Name, Total: totals[id]})
		delete(totals, id)
	}
	if total, ok := totals[0]; ok {
		s.Payers = append(s.Payers, PayerTotal{PayerName: "未分配", Total: total})
		delete(totals, 0)
	}
	// A deleted or renamed payer can still exist in historical snapshots.
	for id, total := range totals {
		payerID := id
		name := ""
		for _, a := range accounts {
			if a.PayerID != nil && *a.PayerID == id {
				name = a.PayerName
				break
			}
		}
		s.Payers = append(s.Payers, PayerTotal{PayerID: &payerID, PayerName: name, Total: total})
	}
	return s
}
