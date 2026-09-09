-- E-Mail-Vorlagen und Empfängerpflege (Ausbaukarte 99). Additive, defaults keep
-- today's wording and recipients exactly.
--
-- mail_signature: the closing under every outgoing mail. Empty keeps the
-- built-in "Mit freundlichen Grüßen / <Betriebsname>".
--
-- mail_cc: an address that receives a copy of every Beleg- and Mahnungs-Mail —
-- the Steuerberater or a second person on the farm. Empty = no copy, so
-- nothing changes for anyone who does not set it.
ALTER TABLE company
    ADD COLUMN mail_signature TEXT NOT NULL DEFAULT '',
    ADD COLUMN mail_cc        TEXT NOT NULL DEFAULT '';
