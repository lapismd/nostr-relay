# Blossom BUD reference

The [pinned source](provenance.md) is included verbatim in every BUD page. Status below describes the current implementation, not the planned target. See [Lapis extensions](lapis-extensions.md) for application-specific contracts.

| BUD | Relay | Community | Scope |
| --- | --- | --- | --- |
| [BUD-00](00.md) | implemented | not applicable | Definitions |
| [BUD-01](01.md) | implemented | partial | Blob retrieval |
| [BUD-02](02.md) | implemented | partial | Blob upload |
| [BUD-03](03.md) | transport-only | partial | User server list |
| [BUD-04](04.md) | implemented | unsupported | Mirroring |
| [BUD-05](05.md) | implemented | unsupported | Static JPEG/PNG/WebP media optimization |
| [BUD-06](06.md) | implemented | unsupported | Upload requirements |
| [BUD-07](07.md) | unsupported | unsupported | Payments |
| [BUD-08](08.md) | implemented | partial | NIP-94 descriptor tags |
| [BUD-09](09.md) | implemented | unsupported | Blob reports |
| [BUD-10](10.md) | not applicable | implemented | Blossom URIs |
| [BUD-11](11.md) | partial | partial | Nostr authorization |
| [BUD-12](12.md) | implemented | unsupported | Blob management |

`implemented` means the relevant protocol role is complete; `partial` means an endpoint or behavior has known gaps; `transport-only` means generic Nostr event handling without BUD-specific behavior; `unsupported` means unavailable; `not applicable` means the component has no role. Client-only conventions have no relay endpoint requirement.
