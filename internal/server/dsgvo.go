package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// The DSGVO/GDPR Art. 15 (right of access) & Art. 20 (portability) export: a
// machine-readable neighbor-record export. The accompanying-delivery checklist
// identifies records needing separate delivery/review; it is not a completeness
// claim for all subject roles, third-party data, logs or backup archives.

type dsgvoExport struct {
	ExportedAt         time.Time                       `json:"exported_at"`
	Notice             string                          `json:"notice"`
	Subject            dsgvoSubject                    `json:"subject"`
	LegacyEquipment    []models.NeighborEquipment      `json:"neighbor_equipment,omitempty"`
	PriceTerms         []dsgvoPriceTerm                `json:"price_terms,omitempty"`
	Fields             []models.Field                  `json:"fields,omitempty"`
	BillingYears       []dsgvoYear                     `json:"billing_years"`
	Recurring          []store.NeighborRecurringExport `json:"recurring_rules"`
	Mail               []store.NeighborMailExport      `json:"mail_outbox"`
	AdditionalDelivery []string                        `json:"additional_delivery_required"`
}

type dsgvoSubject struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Address         string    `json:"address,omitempty"`
	TaxID           string    `json:"tax_id,omitempty"`
	Email           string    `json:"email,omitempty"`
	IBAN            string    `json:"iban,omitempty"`
	Note            string    `json:"note,omitempty"`
	Archived        bool      `json:"archived"`
	Anonymized      bool      `json:"anonymized"`
	PaymentTermDays *int      `json:"payment_term_days,omitempty"`
	Created         time.Time `json:"created"`
}

type dsgvoYear struct {
	Year           int                                 `json:"year"`
	Entries        []dsgvoEntry                        `json:"entries"`
	Invoices       []dsgvoInvoice                      `json:"invoices"`
	InvoiceSources []store.NeighborInvoiceSourceExport `json:"invoice_sources,omitempty"`
	// Ausbaukarte 86: an Auskunft listing only bookings and invoices is
	// incomplete. Money received, mutual settlements, the receipt photos held
	// about this person, when their document was handed over and through which
	// public link — all of it is personal data Treckrr stores.
	Payments       []dsgvoPayment                      `json:"payments,omitempty"`
	PaymentImports []store.NeighborPaymentImportExport `json:"payment_import_rows,omitempty"`
	Ledger         []dsgvoLedger                       `json:"ledger,omitempty"`
	Photos         []dsgvoPhoto                        `json:"photos,omitempty"`
	Sends          []dsgvoSend                         `json:"sends,omitempty"`
	ShareLinks     []dsgvoShare                        `json:"share_links,omitempty"`
	// Running statement links and what the neighbor answered through them.
	StatementLinks    []dsgvoShare             `json:"statement_links,omitempty"`
	StatementFeedback []dsgvoStatementFeedback `json:"statement_feedback,omitempty"`
	Installments      []dsgvoInstallment       `json:"installments,omitempty"`
	// The dunning history is personal data too — arguably the most sensitive
	// record held about a neighbor: which Mahnstufe went out when, through which
	// channel, with what fee. An Auskunft omitting it is incomplete.
	Dunning []dsgvoDunning `json:"dunning_notices,omitempty"`
	// On-site signatures: the signer's name and handwriting are personal data.
	Signatures []dsgvoSignature `json:"signatures,omitempty"`
}

type dsgvoDunning struct {
	SentAt     time.Time       `json:"sent_at"`
	Stage      int             `json:"stage"`
	Channel    string          `json:"channel"`
	Invoice    string          `json:"invoice,omitempty"`
	GraceUntil *time.Time      `json:"grace_until,omitempty"`
	Fee        decimal.Decimal `json:"fee"`
}

type dsgvoPayment struct {
	InvoiceID          *int64          `json:"invoice_id,omitempty"`
	PreferredInvoiceID *int64          `json:"preferred_invoice_id,omitempty"`
	ID                 int64           `json:"id"`
	PaidOn             time.Time       `json:"paid_on"`
	Amount             decimal.Decimal `json:"amount"`
	Method             string          `json:"method,omitempty"`
	Invoice            string          `json:"invoice,omitempty"`
	Note               string          `json:"note,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	DeletedAt          *time.Time      `json:"deleted_at,omitempty"`
}

type dsgvoLedger struct {
	Date        time.Time             `json:"date"`
	Amount      decimal.Decimal       `json:"amount"`
	Description string                `json:"description,omitempty"`
	Voided      bool                  `json:"voided"`
	VoidReason  string                `json:"void_reason,omitempty"`
	Booking     *models.LedgerBooking `json:"booking,omitempty"`
}

type dsgvoSignature struct {
	EntryID    int64     `json:"entry_id"`
	SignerName string    `json:"signer_name"`
	SignedAt   time.Time `json:"signed_at"`
	ReceivedAt time.Time `json:"received_at"`
	Confirmed  string    `json:"confirmed"`
	URL        string    `json:"url"`
}

type dsgvoPhoto struct {
	EntryDate time.Time `json:"entry_date"`
	Task      string    `json:"task,omitempty"`
	Uploaded  time.Time `json:"uploaded"`
	URL       string    `json:"url"`
}

type dsgvoSend struct {
	InvoiceID *int64    `json:"invoice_id,omitempty"`
	SentAt    time.Time `json:"sent_at"`
	Channel   string    `json:"channel"`
}

type dsgvoShare struct {
	InvoiceID  *int64     `json:"invoice_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedBy  string     `json:"created_by,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type dsgvoStatementFeedback struct {
	CreatedAt  time.Time        `json:"created_at"`
	Status     string           `json:"status"`
	Line       string           `json:"line,omitempty"`
	LineAmount *decimal.Decimal `json:"line_amount,omitempty"`
	Message    string           `json:"message,omitempty"`
}

type dsgvoInstallment struct {
	DueOn  time.Time       `json:"due_on"`
	Amount decimal.Decimal `json:"amount"`
	Note   string          `json:"note,omitempty"`
}

type dsgvoPriceTerm struct {
	Label       string          `json:"label"`
	DiscountPct decimal.Decimal `json:"discount_pct"`
	ValidFrom   time.Time       `json:"valid_from"`
	ValidTo     *time.Time      `json:"valid_to,omitempty"`
	Created     time.Time       `json:"created"`
}

type dsgvoEntry struct {
	ID         int64           `json:"id"`
	Date       time.Time       `json:"date"`
	Task       string          `json:"task"`
	Tractor    string          `json:"tractor,omitempty"`
	Load       string          `json:"load,omitempty"`
	Machines   string          `json:"machines,omitempty"`
	Unit       string          `json:"unit"`
	Quantity   decimal.Decimal `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unit_price"`
	Cost       decimal.Decimal `json:"cost"`
	Note       string          `json:"note,omitempty"`
	Voided     bool            `json:"voided"`
	VoidReason string          `json:"void_reason,omitempty"`
	PriceTerm  string          `json:"price_term,omitempty"`
	Field      string          `json:"field,omitempty"`
}

type dsgvoInvoice struct {
	ID       int64                  `json:"id"`
	Number   string                 `json:"number"`
	Kind     string                 `json:"kind"`
	Status   string                 `json:"status"`
	IssuedOn time.Time              `json:"issued_on"`
	Content  *models.InvoiceContent `json:"content,omitempty"`
}

func dsgvoSubjectFromNeighbor(n *models.Neighbor) dsgvoSubject {
	return dsgvoSubject{
		ID: n.ID, Name: n.Name, Address: n.Address, TaxID: n.TaxID,
		Email: n.Email, IBAN: n.IBAN,
		Note: n.Note, Archived: n.Archived, Anonymized: n.Anonymized, Created: n.Created,
		PaymentTermDays: n.PaymentTermDays,
	}
}

func dsgvoEntryFrom(e models.Entry) dsgvoEntry {
	return dsgvoEntry{
		ID: e.ID, Date: e.Date, Task: e.TaskLabel, Tractor: e.TractorLabel, Load: e.LoadLabel,
		Machines: e.MachineLabels, Unit: e.Unit, Quantity: e.Quantity,
		UnitPrice: e.UnitPrice, Cost: e.Cost, Note: e.Note,
		Voided: e.Voided, VoidReason: e.VoidReason, PriceTerm: models.PriceTermNote(e.PriceTermLabel, e.PriceTermPct),
		Field: e.FieldLabel,
	}
}

func dsgvoInvoiceFrom(iv models.Invoice) dsgvoInvoice {
	return dsgvoInvoice{
		ID: iv.ID, Number: iv.Number, Kind: iv.Kind, Status: iv.Status,
		IssuedOn: iv.IssuedOn, Content: iv.Content,
	}
}

// handleNeighborDataExport streams the DSGVO Art. 15 data-subject export for one
// neighbor as an indented JSON attachment. The export itself is audited, since a
// data-access request is a processing activity worth recording.
func (s *Server) handleNeighborDataExport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	n, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	out := dsgvoExport{
		ExportedAt: time.Now(),
		Notice:     "DSGVO Art. 15/20 Datenauskunft — strukturierte Nachbardaten; ergänzende Unterlagen gemäß Prüfliste separat bereitstellen.",
		Subject:    dsgvoSubjectFromNeighbor(n),
		AdditionalDelivery: []string{
			"Unterschriften: Die angegebenen Bild-URLs benötigen eine interne Anmeldung. Berechtigte Unterschriftsbilder herunterladen und als Dateien sicher mitliefern; URLs allein sind keine vollständige Kopie.",
			"Fotos: Die angegebenen URLs benötigen eine interne Anmeldung. Berechtigte Fotos herunterladen und als Dateien sicher mitliefern; URLs allein sind keine vollständige Kopie.",
			"E-Mail-Anhänge: Gespeicherte Dateien anhand der mail_outbox-ID und Anhangsmetadaten prüfen und separat sicher mitliefern; Anhangsbytes und technische SMTP-Fehler sind nicht enthalten.",
			"Protokolle: Personenbezogene Audit- und Betriebsprotokolle durch einen Administrator prüfen, fremde Daten und Zugangsdaten entfernen und einen berechtigten Auszug separat mitliefern. Eine Namenssuche allein ist nicht vollständig.",
			"Weitere Rollen und Archive: Personenstamm, Benutzerkonten und Sicherungen separat prüfen, falls diese dieselbe betroffene Person betreffen. Dieser Export ordnet diese Rollen nicht automatisch zu.",
		},
	}
	out.Recurring, err = s.store.ListNeighborRecurringExport(r.Context(), n.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	out.LegacyEquipment, err = s.store.ListNeighborEquipment(r.Context(), n.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	out.Fields, err = s.store.ListFields(r.Context(), n.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	terms, err := s.store.ListPriceTerms(r.Context(), n.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	for _, t := range terms {
		out.PriceTerms = append(out.PriceTerms, dsgvoPriceTerm{
			Label: t.Label, DiscountPct: t.DiscountPct, ValidFrom: t.ValidFrom, ValidTo: t.ValidTo, Created: t.Created,
		})
	}
	out.Mail, err = s.store.ListNeighborMailExport(r.Context(), n.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	for _, y := range years {
		entries, err := s.store.ListEntries(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		invoices, err := s.store.ListInvoiceDocuments(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		sources, err := s.store.ListNeighborInvoiceSourceExport(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		payments, err := s.store.ListRetainedPayments(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		paymentImports, err := s.store.ListNeighborPaymentImportExport(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		ledger, err := s.store.ListNeighborLedger(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		photos, err := s.store.ListNeighborPhotos(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		sends, err := s.store.ListBelegSends(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		shares, err := s.store.ListBelegShares(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		plans, err := s.store.ListInstallments(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		statementLinks, err := s.store.ListAllStatementShares(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		statementFeedback, err := s.store.ListAllStatementFeedback(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		dunning, err := s.store.ListDunningNotices(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		signatures, err := s.store.SignaturesByNeighborYear(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if len(entries) == 0 && len(invoices) == 0 && len(payments) == 0 && len(paymentImports) == 0 && len(ledger) == 0 &&
			len(photos) == 0 && len(sends) == 0 && len(shares) == 0 && len(plans) == 0 && len(dunning) == 0 &&
			len(statementLinks) == 0 && len(statementFeedback) == 0 && len(signatures) == 0 {
			continue // a year with no data for this person adds nothing.
		}
		dy := dsgvoYear{Year: y.Year, PaymentImports: paymentImports, InvoiceSources: sources}
		for _, e := range entries {
			dy.Entries = append(dy.Entries, dsgvoEntryFrom(e))
		}
		for _, iv := range invoices {
			dy.Invoices = append(dy.Invoices, dsgvoInvoiceFrom(iv))
		}
		for _, p := range payments {
			dy.Payments = append(dy.Payments, dsgvoPayment{
				ID: p.ID, CreatedAt: p.Created, DeletedAt: p.DeletedAt,
				PaidOn: p.PaidOn, Amount: p.Amount, Method: p.Method,
				Invoice: p.InvoiceNumber, InvoiceID: p.InvoiceID, PreferredInvoiceID: p.PreferredInvoiceID, Note: p.Note,
			})
		}
		for _, l := range ledger {
			dy.Ledger = append(dy.Ledger, dsgvoLedger{
				Date: l.Date, Amount: l.Amount, Description: l.Description, Voided: l.Voided, VoidReason: l.VoidReason, Booking: l.Booking,
			})
		}
		for _, ph := range photos {
			// The URL, not the bytes: a JSON document is the wrong carrier for
			// megabytes of images, and each photo stays retrievable at this path.
			// PhotoRef.URL picks the source table's route — a ledger receipt
			// under /entries/ would 404 or, on an id collision, name another
			// booking's photo.
			dy.Photos = append(dy.Photos, dsgvoPhoto{
				EntryDate: ph.EntryDate, Task: ph.TaskLabel, Uploaded: ph.Created,
				URL: ph.URL(),
			})
		}
		for _, snd := range sends {
			dy.Sends = append(dy.Sends, dsgvoSend{SentAt: snd.SentAt, Channel: snd.Channel, InvoiceID: snd.InvoiceID})
		}
		for _, sh := range shares {
			dy.ShareLinks = append(dy.ShareLinks, dsgvoShare{
				CreatedAt: sh.CreatedAt, ExpiresAt: sh.ExpiresAt,
				CreatedBy: sh.CreatedBy, LastUsedAt: sh.LastUsedAt, InvoiceID: sh.InvoiceID,
			})
		}
		for _, sh := range statementLinks {
			dy.StatementLinks = append(dy.StatementLinks, dsgvoShare{
				CreatedAt: sh.CreatedAt, ExpiresAt: sh.ExpiresAt, CreatedBy: sh.CreatedBy, LastUsedAt: sh.LastUsedAt,
			})
		}
		for _, f := range statementFeedback {
			item := dsgvoStatementFeedback{CreatedAt: f.CreatedAt, Status: f.Status, Line: f.LineLabel, Message: f.Message}
			if f.LineAmount.Valid {
				amount := f.LineAmount.Decimal
				item.LineAmount = &amount
			}
			dy.StatementFeedback = append(dy.StatementFeedback, item)
		}
		for _, sig := range signatures {
			dy.Signatures = append(dy.Signatures, dsgvoSignature{
				EntryID: sig.EntryID, SignerName: sig.SignerName, SignedAt: sig.SignedAt, ReceivedAt: sig.ReceivedAt,
				Confirmed: sig.ConfirmedText, URL: "/entries/" + itoa64(sig.EntryID) + "/signature.png",
			})
		}
		sort.Slice(dy.Signatures, func(i, j int) bool { return dy.Signatures[i].EntryID < dy.Signatures[j].EntryID })
		for _, pl := range plans {
			dy.Installments = append(dy.Installments, dsgvoInstallment{
				DueOn: pl.DueOn, Amount: pl.Amount, Note: pl.Note,
			})
		}
		for _, dn := range dunning {
			d := dsgvoDunning{SentAt: dn.SentAt, Stage: dn.Stage, Channel: dn.Channel,
				Invoice: dn.InvoiceNumber, Fee: dn.Fee}
			if !dn.GraceUntil.IsZero() {
				g := dn.GraceUntil
				d.GraceUntil = &g
			}
			dy.Dunning = append(dy.Dunning, d)
		}
		out.BillingYears = append(out.BillingYears, dy)
	}

	s.audit(r, "export", "neighbor", n.ID, "DSGVO-Auskunft")

	filename := "treckrr_auskunft_" + sanitizeFilename(n.Name) + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.serverError(w, r.URL.Path, err)
	}
}
