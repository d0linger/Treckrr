// Package models holds the domain types shared across the application.
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Roles control what a user may do.
const (
	RoleAdmin  = "admin"  // full access incl. user management
	RoleEditor = "editor" // may create/edit data
	RoleViewer = "viewer" // read-only
)

// User is an application account. Admins manage other users.
type User struct {
	ID                 int64
	Username           string
	Role               string
	IsAdmin            bool // derived: Role == admin (kept for templates/handlers)
	MustChangePassword bool
	TotpEnabled        bool
	CreatedAt          time.Time
}

// CanWrite reports whether the user may modify data (not a viewer).
func (u User) CanWrite() bool { return u.Role != RoleViewer }

// RoleLabel returns a German label for the user's role.
func (u User) RoleLabel() string {
	switch u.Role {
	case RoleAdmin:
		return "Administrator"
	case RoleViewer:
		return "Nur-Lesen"
	default:
		return "Erfasser"
	}
}

// Session is an active login session (for the management view).
type Session struct {
	Token     string
	UserID    int64
	UserAgent string
	IP        string
	LastSeen  time.Time
	Created   time.Time
	ExpiresAt time.Time
	Current   bool // set at render time for the requesting session
}

// PriceBase is a pricing basis (Bemessungsgrundlage). It is published roughly
// every few years and reused by several billing years. Year documents when the
// basis becomes valid ("gültig ab"). Locking freezes its values.
type PriceBase struct {
	ID      int64
	Year    int
	Name    string
	Locked  bool
	Created time.Time
}

// Billing year workflow statuses.
const (
	YearInProgress = "in_progress"
	YearCompleted  = "completed"
)

// BillingYear (Abrechnungsjahr) is the unit the user works in: a calendar year
// bound to one pricing basis, with its own participating neighbors.
type BillingYear struct {
	ID      int64
	Year    int
	BaseID  int64
	Label   string
	Status  string
	Created time.Time
	// Base is populated on demand for convenience (may be nil).
	Base *PriceBase
}

// Completed reports whether the billing year has been closed.
func (y BillingYear) Completed() bool { return y.Status == YearCompleted }

// LoadLevel is a Belastungsstufe: cost per PS per hour (leicht/mittel/schwer).
type LoadLevel struct {
	ID        int64
	BaseID    int64
	Name      string
	CostPerPS decimal.Decimal
	SortOrder int
}

// Tractor: hourly rate = PS * LoadLevel.CostPerPS.
type Tractor struct {
	ID        int64
	BaseID    int64
	Ident     string
	Name      string
	PS        decimal.Decimal
	Active    bool
	SortOrder int
}

// Label returns a human-readable identifier, e.g. "4095 (100 PS)".
func (t Tractor) Label() string {
	base := t.Ident
	if t.Name != "" {
		base = t.Ident + " " + t.Name
	}
	return base + " (" + t.PS.String() + " PS)"
}

// Machine: hourly rate = WorkingWidth * CostPerAB.
type Machine struct {
	ID           int64
	BaseID       int64
	Name         string
	WorkingWidth decimal.Decimal
	CostPerAB    decimal.Decimal
	Active       bool
	Category     string
	SortOrder    int
}

// HourlyRate returns the machine's contribution to a Gespann's hourly rate.
func (m Machine) HourlyRate() decimal.Decimal { return m.WorkingWidth.Mul(m.CostPerAB).Round(2) }

// Gespann is a named fixed combination of a tractor, a load level and machines.
type Gespann struct {
	ID          int64
	BaseID      int64
	Name        string
	TractorID   *int64
	LoadLevelID *int64
	MachineIDs  []int64
	SortOrder   int
}

// Neighbor (Nachbar) is billed for booked work per year.
type Neighbor struct {
	ID       int64
	Name     string
	Note     string
	Archived bool
	Created  time.Time
}

// Entry is a booked unit of work with snapshotted pricing for stable exports.
type Entry struct {
	ID            int64
	NeighborID    int64
	BillingYearID int64
	Date          time.Time
	TaskLabel     string
	GespannID     *int64
	TractorID     *int64
	LoadLevelID   *int64
	TractorLabel  string
	LoadLabel     string
	MachineLabels string
	Hours         decimal.Decimal
	HourlyRate    decimal.Decimal
	Cost          decimal.Decimal
	Note          string
	Voided        bool
	VoidReason    string
	Created       time.Time
}

// WebauthnCredential is a registered passkey (public key only).
type WebauthnCredential struct {
	ID           int64
	CredentialID []byte
	PublicKey    []byte
	AAGUID       []byte
	SignCount    uint32
	Transports   string
	Name         string
	// BackupEligible/BackupState are the WebAuthn BE/BS flags captured at
	// registration. BE is fixed for the credential's life and must be replayed
	// on login or go-webauthn rejects the assertion.
	BackupEligible bool
	BackupState    bool
	Created        time.Time
	LastUsed       *time.Time
}

// LedgerEntry is one manual account posting for a neighbor in a billing year.
// A positive amount is an extra receivable (they owe more); a negative amount is
// a payable (I owe them). It nets against the work bookings for the year.
type LedgerEntry struct {
	ID          int64
	Amount      decimal.Decimal
	Description string
	Date        time.Time // editable posting date (like a booking's date)
	Voided      bool
	VoidReason  string
	Created     time.Time
}

// AuditEntry is one recorded action in the audit trail.
type AuditEntry struct {
	ID       int64
	UserID   *int64
	Username string
	Action   string
	Entity   string
	EntityID string
	Detail   string
	IP       string
	Created  time.Time
}
