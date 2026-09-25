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
6. regenerate the architecture exclusion manifest with
   `./scripts/update-architecture-baseline.sh --update` whenever the imported
   commit moves, and confirm no budget was raised;
7. update the root patch map and any affected specification chapter; and
8. run the focused, full, tagged integration, and image validation described in
   [Verification](../verification.md).

The exclusion manifest governs which inherited paths stay outside the
counted-line budgets, so a refresh that changes the inherited file set must
update it in the same change. See
[Architecture budgets](architecture-budgets.md).

Provider upgrades also follow the checklists in [Capability-based forge
architecture](../forge/capabilities.md), [WalGit integration](../forge/walgit.md),
and [GitHub integration](../forge/github.md).
