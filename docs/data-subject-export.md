# Data-subject export inventory and delivery

`GET /neighbors/{id}/dsgvo-export.json` is an authenticated, audited export of
structured records belonging to one neighbor. It is not, by itself, a complete
response covering every role, log or backup in which the person may appear.
The JSON contains `additional_delivery_required` to make this distinction visible
to the operator and recipient.

## Automatically included

| Source | Exported data |
| --- | --- |
| `neighbors` | Identity/contact fields, note, individual payment terms, archived/anonymized state and creation time |
| `entries` | Booking amounts and descriptive fields, including voided entries and void reasons |
| `invoices` | Document number, type, status, issue date and available frozen content |
| `payments` | Retained active **and soft-deleted** records, stable ID, amounts, date, method, note, invoice reference, creation/deletion timestamps |
| `neighbor_ledger` | Amount/date/description and void state/reason |
| `entry_photos` | Metadata and authenticated retrieval URLs; files require accompanying delivery |
| `beleg_sends`, `beleg_shares` | Delivery and share-link metadata, **not bearer tokens or token hashes** |
| `payment_plans`, `dunning_notices` | Installment notes/amounts/dates and recorded reminder details |
| `recurring_entries` | Subject-scoped templates, cadence, next/last run, active state and creation time, independent of billing-year membership |
| `mail_outbox` | Subject-scoped pending/failed/sent correspondence, status/timing and attachment name/type/byte count, including messages without a billing year |

The additional payment accessor is export-only. Deleted payments remain excluded
from ordinary lists and balances. No historical invoice, payment or audit record
is rewritten by this export.

## Accompanying delivery checklist

1. Verify the requester's identity and the records they are entitled to receive.
   Review the JSON for information concerning other people before delivery.
2. For each authorized photo, retrieve the file through its authenticated URL and
   include the image file in the secure delivery. An internal URL alone is not a
   portable copy; do not send login credentials or turn it into a public link.
3. For each non-empty outbox attachment, have an authorized administrator extract
   the stored attachment using its outbox ID **and the same neighbor ID**. Verify
   the name, media type and byte count against the JSON and deliver the approved
   file alongside it. There is currently no automatic attachment bundle endpoint.
4. Review subject-related audit and operational logs separately. Follow stable
   neighbor and related-record IDs, then inspect text-only historical events;
   searching only the current name misses renamed/deleted records. Redact other
   people's data, authentication material and technical secrets from the approved
   extract. Never hand over the entire audit database. Raw SMTP errors are not
   automatically exported because they may expose server or authentication data.
5. Check whether the individual also has a helper/person or user-account record,
   and assess relevant archives/backups under the operator's applicable policy.
   These identities are not automatically matched by name. Record what was
   supplied, what was excluded and why, and which reviews remain outstanding.

Use an approved secure delivery channel. Do not claim a complete subject response
until the supplementary review and delivery are finished. The current JSON
implementation assembles structured records in memory; very large histories may
require a supervised export. Automatic paginated bundles and automatic safe
subject-level audit attribution remain follow-ups, not guarantees of this endpoint.

## Previously anonymized records

Deployment does not automatically rewrite existing records. If an earlier version
left live void reasons or subsequently entered personal text on an anonymized
neighbor, an authorized operator must repeat that neighbor's erasure action after
review. The repeat now scrubs those live fields; financial amounts and frozen
invoice snapshots remain untouched. New personal-data writes and reactivation are
rejected for anonymized neighbors.
