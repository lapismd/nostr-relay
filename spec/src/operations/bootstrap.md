# Clean boot and data population

A clean relay deployment starts empty. It does not load application fixtures,
create a default project, or attach a GitHub repository.

Startup performs only infrastructure and relay-owned bootstrap:

1. The container entrypoint creates a host configuration when its config
   volume is empty.
2. The relay creates or migrates the PostgreSQL schemas and indexes.
3. The relay signs its relay-level administrator list and ensures its relay
   identity, owner, and configured role pubkeys are represented by internal
   membership events.
4. The local Compose stack prepares an empty object-store bucket for WalGit.
   The Git reconciler resumes intents from existing kind `30617` events; a new
   database has none.
5. Forge migrations create empty integration, replica, replication-job,
   outbox, webhook, migration, and CI tables. They do not create connections or
   provider objects.

Application state is populated with signed client events. With
`groups.admin_create_only = true`, a configured relay administrator first
creates each NIP-29 group with kind `9007`. The relay derives the group
metadata, creator membership, member snapshot, and administrator snapshot.
Authenticated clients may then publish:

- kind `30621` projects;
- kind `30617` repositories or Project Task Anchors;
- kind `1621` tasks;
- kinds `1618` and `1619` pull-request roots and updates; and
- comments, reviews, statuses, assignments, and deletion events.

Events may arrive through WebSocket `EVENT` or authenticated `POST /events`.
The [project model](../protocol/projects-tasks-and-pull-requests.md) defines
their validation and scope.

A hosted `30617` creates a durable provisioning intent. The background worker
creates the empty WalGit repository asynchronously; commits arrive later
through Smart HTTP. A Project Task Anchor never provisions Git storage.
GitHub requires explicit administrator connection and repository attachment
after an owner-signed announcement already exists.

Production startup never runs test fixtures or tagged integration setup. Any
seed data is therefore an application/deployment concern and must be published
as valid signed events.
