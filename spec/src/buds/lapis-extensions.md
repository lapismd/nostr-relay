# Lapis Blossom extensions

Lapis uses signed Nostr events and registry manifests as the authority for application use of blobs. A blob descriptor's MIME type and NIP-94 tags describe file bytes; they do not prove that an avatar or plugin release uses those bytes.

Planned extension contracts are `lapis-purpose`, `lapis-plugin`, and `lapis-slot` declarations in upload authorization; a namespaced `lapis` descriptor field; `tag.<key>=<value>` filters on the owner's BUD-12 list; and signed, access-controlled asset history and report views under `/api/assets`. These are not part of the upstream BUDs. Publish their exact request, response, permission, and migration contracts here alongside implementation.

An upload begins unreferenced. A successful signed profile update or verified registry release establishes use. Replacement and removal are recorded as durable history. Explicit deletion is blocked while a retained reference requires the blob, and shared blobs remain while another owner or reference needs them. Plugin artwork changes create a new signed release under the existing registry review process.
