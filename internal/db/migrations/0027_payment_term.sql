-- Payment term (Zahlungsziel) in days, used to derive an invoice's due date
-- (issue date + term) for the dunning list (Mahnwesen). Default 14 days.
ALTER TABLE company ADD COLUMN payment_term_days INT NOT NULL DEFAULT 14;
