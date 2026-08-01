-- Guard invoice numbering. IssueInvoice serializes number allocation per year
-- with a transaction-level advisory lock, but a unique (billing_year_id, number)
-- pair is the durable backstop so two invoices can never share a number even
-- under a race or a bad manual insert.
ALTER TABLE invoices ADD CONSTRAINT invoices_year_number_key UNIQUE (billing_year_id, number);
