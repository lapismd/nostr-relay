# NIP compatibility reference

This section records implementation profiles rather than copying upstream
specifications. The standard NIP audit is pinned to
`nostr-protocol/nips@a2494f4f81d46684e5814a9bf35e2b1df978f955`; the Buzz agent
audit is pinned to `block/buzz@cec5c8fd9280d30f56effac701e1e19d5cfe6fea`;
and the distinct Buzz Desktop/project audit remains pinned to
`block/buzz@3c7f288c60d67df78577b237e27c3dfc8831aaa1`. The implementation
revision is the repository revision containing this page, audited 2026-09-11.

Statuses mean: **implemented** has an evidence-backed end-to-end role;
**partial** implements a documented subset; **transport-only** stores or moves
events without implementing endpoint semantics; **planned** is specified but
not active; **unsupported** is rejected or unavailable; and **not applicable**
means the component has no role. Accepting an event kind or ciphertext never
upgrades transport-only support to implemented.

| Protocol | Relay | Community | External agent harness | Main limitation or divergence |
| --- | --- | --- | --- | --- |
| [NIP-01](NIP-01.md) | implemented | implemented | not applicable | Relay policy narrows otherwise generic flow. |
| [NIP-02](NIP-02.md) | transport-only | implemented | not applicable | Community uses kind `3` for agent follows. |
| [NIP-07](NIP-07.md) | not applicable | implemented | not applicable | Signer is injected by the host. |
| [NIP-09](NIP-09.md) | implemented | implemented | not applicable | Physical deletion means deleted heads do not resurrect. |
| [NIP-11](NIP-11.md) | implemented | implemented | not applicable | Mixed numeric/string draft identifiers are advertised. |
| [NIP-16](NIP-16.md) | not applicable | partial | unsupported | Historical capability heuristic; successor is NIP-18. |
| [NIP-17](NIP-17.md) | transport-only | implemented | not applicable | Encryption stays client-side. |
| [NIP-19](NIP-19.md) | not applicable | implemented | not applicable | Client encoding only. |
| [NIP-22](NIP-22.md) | partial | implemented | not applicable | Relay validates project/forum subsets. |
| [NIP-25](NIP-25.md) | partial | implemented | not applicable | Group and forge policy add authorization. |
| [NIP-29](NIP-29.md) | implemented | implemented | not applicable | Admin-only creation is a configured policy choice. |
| [NIP-30](NIP-30.md) | transport-only | implemented | not applicable | Rendering and selection are client-side. |
| [NIP-34](NIP-34.md) | partial | partial | not applicable | Patches and repository-state events remain deferred. |
| [NIP-38](NIP-38.md) | transport-only | implemented | not applicable | Community projects current status only. |
| [NIP-40](NIP-40.md) | unsupported | not applicable | not applicable | Expiration manager is disabled and NIP is not advertised. |
| [NIP-42](NIP-42.md) | implemented | implemented | required | Agent admission adds NIP-AA after normal verification. |
| [NIP-43](NIP-43.md) | partial | partial | not applicable | Advertised access workflow is relay-specific. |
| [NIP-44](NIP-44.md) | transport-only | implemented | required | Relay validates envelopes but cannot decrypt them. |
| [NIP-45](NIP-45.md) | implemented | implemented | not applicable | Unsafe visibility filters are rejected. |
| [NIP-46](NIP-46.md) | transport-only | implemented | not applicable | Remote signer lifecycle is host-owned. |
| [NIP-50](NIP-50.md) | partial | partial | not applicable | Private/group-safe search is deliberately narrower. |
| [NIP-51](NIP-51.md) | transport-only | partial | not applicable | Several preferences remain device-local. |
| [NIP-70](NIP-70.md) | implemented | transport-only | not applicable | Relay blocks protected reposts. |
| [NIP-77](NIP-77.md) | implemented | unsupported | not applicable | Community does not use negentropy. |
| [NIP-78](NIP-78.md) | transport-only | partial | not applicable | Only scoped Community state uses it. |
| [NIP-7D](NIP-7D.md) | partial | implemented | not applicable | Forum visibility follows NIP-29 group policy. |
| [NIP-86](NIP-86.md) | implemented | not applicable | not applicable | Lapis adds role methods with inert unknown roles. |
| [NIP-92](NIP-92.md) | transport-only | implemented | not applicable | Hash verification is a Community policy. |
| [NIP-98](NIP-98.md) | implemented | implemented | not applicable | Git credential-helper profile differs from generic HTTP. |
| [NIP-AA](NIP-AA.md) | implemented | not applicable | required | Grants connection-local relay admission only. |
| [NIP-AE](NIP-AE.md) | implemented | unsupported | planned | Relay sees only an opaque NIP-44 envelope. |
| [NIP-AM](NIP-AM.md) | implemented | unsupported | planned | Metrics are stored but not rendered or executed. |
| [NIP-AO](NIP-AO.md) | implemented | implemented | required | Community consumes telemetry; relay never decrypts it. |
| [NIP-AP](NIP-AP.md) | implemented | implemented | planned | Prompt is saved; runtime/profile reconciliation is absent. |
| [NIP-MP](NIP-MP.md) | partial | partial | not applicable | Lapis adds an explicit project-task anchor. |
| [NIP-OA](NIP-OA.md) | implemented | implemented | required | Ownership grants no channel or administrative privilege. |
| [NIP-PMA](NIP-PMA.md) | unsupported | unsupported | unsupported | Kind `30179` is rejected pending complete CAS/privacy. |

The pinned Buzz source also contains the following drafts that are neither advertised nor implemented by Lapis. They are included in the audit inventory without local protocol pages because no component has partial support: [NIP-CW channel windows](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-CW.md), [NIP-DV DM visibility](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-DV.md), [NIP-ER reminders](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-ER.md), [NIP-FI federated identity](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-FI.md), [NIP-GS Git object signing](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-GS.md), [NIP-IA identity archival](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-IA.md), [NIP-PL push leases](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-PL.md), [NIP-RS read-state sync](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-RS.md), and [NIP-WP workspace profiles](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-WP.md).

Blossom BUDs, the Buzz-compatible HTTP bridge, forge APIs, Smart HTTP and
Lapis project-task markers are listed as [non-NIP protocols](non-nip-protocols.md).
Update this inventory when NIP-11 advertising, validation, authorization,
storage, reducers, configuration or tests change. Refresh upstream pins only
after reviewing normative and executable differences; a documentation refresh
does not silently upgrade behavior.
