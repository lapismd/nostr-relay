# Blossom BUD reference

The [pinned source](provenance.md) is included verbatim in every BUD page. Status below describes the current implementation, not the planned target. See [Lapis extensions](lapis-extensions.md) for application-specific contracts.

| BUD | Relay | Community | Scope |
| --- | --- | --- | --- |
| [BUD-00](00.md) | implemented | not applicable | Definitions |
| [BUD-01](01.md) | partial | partial | Blob retrieval |
| [BUD-02](02.md) | partial | partial | Blob upload |
| [BUD-03](03.md) | transport-only | unsupported | User server list |
| [BUD-04](04.md) | partial | unsupported | Mirroring |
| [BUD-05](05.md) | unsupported | unsupported | Media optimization |
| [BUD-06](06.md) | partial | unsupported | Upload requirements |
| [BUD-07](07.md) | unsupported | unsupported | Payments |
| [BUD-08](08.md) | unsupported | unsupported | NIP-94 tags |
| [BUD-09](09.md) | unsupported | unsupported | Blob reports |
| [BUD-10](10.md) | unsupported | unsupported | Blossom URIs |
| [BUD-11](11.md) | partial | partial | Nostr authorization |
| [BUD-12](12.md) | partial | unsupported | Blob management |

`implemented` means the relevant protocol role is complete; `partial` means an endpoint or behavior has known gaps; `transport-only` means generic Nostr event handling without BUD-specific behavior; `unsupported` means unavailable; `not applicable` means the component has no role. Client-only conventions have no relay endpoint requirement.
