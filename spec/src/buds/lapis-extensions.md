# Lapis Blossom extensions

Lapis uses signed Nostr events and registry manifests as the authority for application use of blobs. A blob descriptor's MIME type and NIP-94 tags describe file bytes; they do not prove that an avatar or plugin release uses those bytes.

Signed upload authorization may declare `lapis-purpose`, `lapis-plugin`, and `lapis-slot` tags. Values are lowercase labels of at most 128 characters; the server stores them per uploader and hash. They are uploader claims, not proof that a profile or plugin release uses the blob.

An owner may add `tag.m`, `tag.x`, `tag.size`, `tag.purpose`, `tag.plugin`, or `tag.slot` query parameters to `GET /list/<pubkey>`. Different keys combine with AND; repeated values within one key combine with OR. `limit` defaults to 100 and is capped at 1,000. `cursor` is the last returned hash; responses retain the BUD-12 descriptor array and use newest-first ordering with hash ties. The optional `lapis` field contains the owner's declared values. Owner authorization is required. This list currently scans the owner's stored blob entries before paging; indexing and lifecycle-based filtering remain planned.

Admins and configured managers may read the persisted report queue at `GET /api/assets/reports` with a signed BUD-11 `list` token. Results are newest-first, bounded, and paged by report-event ID. Signed, access-controlled asset history under `/api/assets` remains planned. These are Lapis extensions, not upstream BUD requirements.

An upload begins unreferenced. A successful signed profile update or verified registry release establishes use. Replacement and removal are recorded as durable history. Explicit deletion is blocked while a retained reference requires the blob, and shared blobs remain while another owner or reference needs them. Plugin artwork changes create a new signed release under the existing registry review process.
