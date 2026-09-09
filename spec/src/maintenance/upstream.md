# Upstream maintenance

The fork's pinned import, license provenance, local patch inventory, and update
procedure remain recorded in the repository-root
[`UPSTREAM.md`](../../../UPSTREAM.md). That location is retained deliberately
so fork updates and license audits can find it without first building this
book.

Every upstream refresh must:

1. preserve the upstream MIT license and history;
2. review each Lapis-owned integration seam and default;
3. keep NIP-29 permission checks at the relay boundary;
4. keep `groups.admin_create_only = true` as the Lapis default;
5. avoid placing Lapis behavior in `internal/khatru`;
6. update the root patch map and any affected specification chapter; and
7. run the focused, full, tagged integration, and image validation described in
   [Verification](../verification.md).

Provider upgrades also follow the checklists in [Capability-based forge
architecture](../forge/capabilities.md), [WalGit integration](../forge/walgit.md),
and [GitHub integration](../forge/github.md).
