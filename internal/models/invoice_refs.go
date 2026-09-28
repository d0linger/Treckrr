package models

// ActiveCreditFor reports whether this document is an issued credit note
// attached to the invoice with the given id — exactly the credit notes a full
// invoice storno reverses with their own storno documents. Templates use it to
// list them before the operator confirms the storno.
func (iv Invoice) ActiveCreditFor(invoiceID int64) bool {
	return iv.Kind == "gutschrift" && iv.Status == "issued" &&
		iv.ReferencesInvoiceID != nil && *iv.ReferencesInvoiceID == invoiceID
}
