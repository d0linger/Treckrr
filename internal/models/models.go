// Package models holds the domain types shared across the application.
package models

import (
	"strings"
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
	Email              string
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
	Address  string // optional, for the invoice recipient block
	TaxID    string // optional recipient UID/tax number (§ 11 on invoices > 10k)
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
	// Unit/Quantity/UnitPrice generalize billing beyond hours: cost = quantity ×
	// unit price. For hour bookings Unit is "h", Quantity == Hours and UnitPrice
	// == HourlyRate. Other units: "ha", "Ballen", "m3", "Fuhre", "t" (or custom).
	Unit       string
	Quantity   decimal.Decimal
	UnitPrice  decimal.Decimal
	Note       string
	Voided     bool
	VoidReason string
	Created    time.Time
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
	// TransferID links the two sides of a carry-forward; non-empty means this
	// posting is one half of a cross-year transfer that reverses as a unit.
	TransferID string
}

// Payment is a dated amount a neighbor paid toward a billing year. Payments are
// decoupled from year status (a completed year still accepts them) and there may
// be several per (year, neighbor) — partial payments settle the balance over time.
type Payment struct {
	ID            int64
	BillingYearID int64
	NeighborID    int64
	Amount        decimal.Decimal
	PaidOn        time.Time
	Note          string
	Created       time.Time
}

// BackupSettings is the GUI-editable backup schedule: independent volume and S3
// cron expressions and retention counts. An empty cron disables that destination.
type BackupSettings struct {
	VolumeCron string
	VolumeKeep int
	S3Cron     string
	S3Keep     int
}

// Company holds the sender (Absender) details shown on an invoice-mode Beleg,
// plus the tax treatment: "pauschal" shows only TaxNote; "regel" adds a VAT
// breakdown at VATRate.
type Company struct {
	Name    string
	Address string
	TaxID   string
	TaxNote string
	TaxMode string
	VATRate decimal.Decimal
	IBAN    string // optional issuer bank account for a payable invoice
}

// InvoiceParty is a frozen issuer/recipient block on an invoice snapshot.
type InvoiceParty struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	TaxID   string `json:"tax_id"`         // issuer UID / recipient UID or tax number
	IBAN    string `json:"iban,omitempty"` // issuer bank account, frozen for payment (recipient: unset)
}

// InvoiceLine is one frozen line item of an invoice snapshot.
type InvoiceLine struct {
	Date      time.Time       `json:"date"`
	Label     string          `json:"label"`
	Unit      string          `json:"unit"`
	Quantity  decimal.Decimal `json:"qty"`
	UnitPrice decimal.Decimal `json:"unit_price"`
	Cost      decimal.Decimal `json:"cost"`
}

// InvoiceContent is the immutable substance of an invoice, frozen at issuance
// (Festschreibung) so the document never changes even as bookings/prices do.
// The settlement side (ledger, payments, remaining) is deliberately NOT part of
// it — that stays live because it evolves after the invoice is handed over.
type InvoiceContent struct {
	Net         decimal.Decimal `json:"net"`
	VATRate     decimal.Decimal `json:"vat_rate"`
	VATAmount   decimal.Decimal `json:"vat_amount"`
	Gross       decimal.Decimal `json:"gross"`
	ShowVAT     bool            `json:"show_vat"`
	TaxMode     string          `json:"tax_mode"`
	TaxNote     string          `json:"tax_note"`
	ServiceFrom time.Time       `json:"service_from"`
	ServiceTo   time.Time       `json:"service_to"`
	Issuer      InvoiceParty    `json:"issuer"`
	Recipient   InvoiceParty    `json:"recipient"`
	Lines       []InvoiceLine   `json:"lines"`
	Hash        string          `json:"-"` // sha256 over the canonical content
}

// invoiceUIDThreshold is the gross amount (§ 11 Abs. 1 Z 6 UStG) above which the
// recipient's UID is a mandatory invoice field.
var invoiceUIDThreshold = decimal.NewFromInt(10000)

// MandatoryCheck is one § 11 UStG line item for the pre-issuance checklist.
type MandatoryCheck struct {
	Label  string // what is required
	Detail string // the current value (or empty when missing)
	OK     bool   // satisfied
}

// MandatoryChecks returns the § 11 UStG requirements as a checklist (both passed
// and failed), the single source the block-on-issue and the confirmation UI share.
// It is tax-mode aware: the VAT line applies only to pauschal/regel, the UID line
// only above the €10.000 threshold.
func (c InvoiceContent) MandatoryChecks() []MandatoryCheck {
	firstLine := func(s string) string {
		s = strings.TrimSpace(s)
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return strings.TrimSpace(s[:i])
		}
		return s
	}
	period := ""
	if !c.ServiceFrom.IsZero() {
		period = c.ServiceFrom.Format("02.01.2006")
		if !c.ServiceTo.IsZero() && !c.ServiceTo.Equal(c.ServiceFrom) {
			period += "–" + c.ServiceTo.Format("02.01.2006")
		}
	}
	checks := []MandatoryCheck{
		{"Absender-Name", firstLine(c.Issuer.Name), strings.TrimSpace(c.Issuer.Name) != ""},
		{"Absender-Adresse", firstLine(c.Issuer.Address), strings.TrimSpace(c.Issuer.Address) != ""},
		{"Empfänger-Name", firstLine(c.Recipient.Name), strings.TrimSpace(c.Recipient.Name) != ""},
		{"Empfänger-Adresse", firstLine(c.Recipient.Address), strings.TrimSpace(c.Recipient.Address) != ""},
		{"Leistungszeitraum", period, len(c.Lines) > 0},
	}
	if c.TaxMode == "pauschal" || c.TaxMode == "regel" {
		checks = append(checks, MandatoryCheck{"USt-Ausweis", c.VATRate.StringFixed(0) + " %", c.ShowVAT})
	}
	if c.Gross.GreaterThan(invoiceUIDThreshold) {
		checks = append(checks, MandatoryCheck{"Empfänger-UID (> 10.000 €)", firstLine(c.Recipient.TaxID), strings.TrimSpace(c.Recipient.TaxID) != ""})
	}
	return checks
}

// MissingMandatory returns the labels of the § 11 requirements not yet satisfied.
// An empty slice means the content may be issued as a formal Rechnung.
func (c InvoiceContent) MissingMandatory() []string {
	var m []string
	for _, ch := range c.MandatoryChecks() {
		if !ch.OK {
			m = append(m, ch.Label)
		}
	}
	return m
}

// Invoice is a Beleg issued as a formal Rechnung. Since Festschreibung it also
// carries the frozen content snapshot and the document kind/status so an invoice
// can be stornoed and a Gutschrift issued (a document history per neighbor+year).
type Invoice struct {
	ID            int64
	BillingYearID int64
	NeighborID    int64
	Number        string
	IssuedOn      time.Time
	Created       time.Time

	Kind                string // invoice | storno | gutschrift
	Status              string // issued | canceled
	ReferencesInvoiceID *int64 // storno/gutschrift → the original invoice
	PaymentReference    string
	Content             *InvoiceContent // nil for legacy rows not yet backfilled
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
