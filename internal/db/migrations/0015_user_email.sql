-- Optional contact e-mail per user, editable by admins. Additive: default ''
-- means "no e-mail set", so existing rows are unaffected and login stays by
-- username. Not unique (a contact address may legitimately repeat).
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
