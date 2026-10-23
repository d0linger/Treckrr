-- Built-in accounting export presets. Additive.
--
-- Export profiles mapped columns freely, so every operator had to rebuild
-- their tax adviser's import layout by hand. A profile can now follow a fixed
-- preset for the BMD NTCS or RZL booking import (columns, header names, date
-- and decimal format, debit/credit code); its accounts, tax code and cost
-- center still come from the profile. 'custom' keeps the free mapping, so
-- every existing profile exports exactly as before.
ALTER TABLE accounting_export_profiles
    ADD COLUMN format TEXT NOT NULL DEFAULT 'custom' CHECK (format IN ('custom', 'bmd', 'rzl'));
