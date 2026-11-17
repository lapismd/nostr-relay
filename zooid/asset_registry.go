package zooid

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"fiatjaf.com/nostr"
)

type registryAssetReference struct {
	Owner string
	Hash  string
}

func (store *BlossomStore) projectRegistryReferences(ctx context.Context, event nostr.Event) error {
	if event.Kind != nostr.KindSimpleGroupChatMessage || !hasExactTopic(event.Tags, registryHeadTopic) {
		return nil
	}
	head, err := parseRegistryHead(event)
	if err != nil || exactTag(event.Tags, "h") != store.Config.Registry.ReleaseGroupID {
		return nil
	}
	latest, found := store.latestRegistryHead(head.PluginID)
	if !found || latest.ID != event.ID {
		return nil
	}
	return store.projectRegistryHead(ctx, event, int64(event.CreatedAt))
}

func (store *BlossomStore) projectRegistryHead(ctx context.Context, event nostr.Event, occurredAt int64) error {
	pluginID, owner, assets, err := store.resolveRegistryHeadAssets(ctx, event)
	if err != nil {
		return err
	}
	return store.reconcileRegistryReferences(ctx, event, pluginID, owner, assets, occurredAt)
}

func (store *BlossomStore) validateRegistryHeadAssets(ctx context.Context, event nostr.Event) error {
	_, _, _, err := store.resolveRegistryHeadAssets(ctx, event)
	return err
}

func (store *BlossomStore) resolveRegistryHeadAssets(ctx context.Context, event nostr.Event) (string, string, map[string]string, error) {
	head, err := parseRegistryHead(event)
	if err != nil {
		return "", "", nil, err
	}
	if head.ActiveReleaseEventID == "" {
		return head.PluginID, "", nil, nil
	}
	activeManifest := ""
	for _, release := range head.Releases {
		if release.ReleaseEventID == head.ActiveReleaseEventID && release.State == "approved" {
			activeManifest = release.ManifestSHA256
			break
		}
	}
	id, err := nostr.IDFromHex(head.ActiveReleaseEventID)
	if err != nil || activeManifest == "" {
		return "", "", nil, fmt.Errorf("active registry release evidence is invalid")
	}
	release, found := store.eventByID(id)
	if !found || !isRegistryRelease(release, store.Config.Registry.ReleaseGroupID) || !hasExactTopic(release.Tags, "plugin:"+head.PluginID) {
		return "", "", nil, fmt.Errorf("active registry release event is unavailable")
	}
	attachment, err := store.releaseManifestAttachment(release)
	if err != nil || attachment.Hash != activeManifest {
		return "", "", nil, fmt.Errorf("active registry release manifest evidence does not match")
	}
	assets, err := store.registryManifestAssets(ctx, release, head.PluginID)
	if err != nil {
		return "", "", nil, err
	}
	return head.PluginID, release.PubKey.Hex(), assets, nil
}

func (store *BlossomStore) reconcileRegistryReferences(ctx context.Context, source nostr.Event, pluginID, owner string, desired map[string]string, occurredAt int64) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for slot, hash := range desired {
		var owned bool
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE pubkey=$1 AND sha256=$2)", store.Schema.Prefix("blossom_blobs")), owner, hash).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			delete(desired, slot)
		}
	}
	current, err := store.loadRegistryReferences(ctx, tx, pluginID)
	if err != nil {
		return err
	}
	slots := make([]string, 0, len(current)+len(desired))
	seen := make(map[string]struct{})
	for slot := range current {
		slots, seen[slot] = append(slots, slot), struct{}{}
	}
	for slot := range desired {
		if _, exists := seen[slot]; !exists {
			slots = append(slots, slot)
		}
	}
	sort.Strings(slots)
	for _, slot := range slots {
		previous, hadPrevious := current[slot]
		hash, wanted := desired[slot]
		if hadPrevious && (!wanted || previous.Owner != owner || previous.Hash != hash) {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE reference_type='registry' AND reference_key=$1 AND slot=$2", store.Schema.Prefix("asset_references")), pluginID, slot); err != nil {
				return err
			}
			if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
				Owner: previous.Owner, SHA256: previous.Hash, Action: assetReferenceRemoved,
				ReferenceType: "registry", ReferenceKey: pluginID, Slot: slot,
				SourceEventID: source.ID.Hex(), OccurredAt: occurredAt,
			}); err != nil {
				return err
			}
		}
		if !wanted {
			continue
		}
		if hadPrevious && previous.Owner == owner && previous.Hash == hash {
			_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET source_event_id=$1, source_created_at=$2
				WHERE reference_type='registry' AND reference_key=$3 AND slot=$4`, store.Schema.Prefix("asset_references")), source.ID.Hex(), occurredAt, pluginID, slot)
			if err != nil {
				return err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
			(owner_pubkey, reference_type, reference_key, slot, sha256, source_event_id, source_created_at)
			VALUES ($1, 'registry', $2, $3, $4, $5, $6)`, store.Schema.Prefix("asset_references")), owner, pluginID, slot, hash, source.ID.Hex(), occurredAt)
		if err != nil {
			return err
		}
		if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
			Owner: owner, SHA256: hash, Action: assetReferenceAdded,
			ReferenceType: "registry", ReferenceKey: pluginID, Slot: slot,
			SourceEventID: source.ID.Hex(), OccurredAt: occurredAt,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *BlossomStore) loadRegistryReferences(ctx context.Context, tx *sql.Tx, pluginID string) (map[string]registryAssetReference, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT slot, owner_pubkey, sha256 FROM %s
		WHERE reference_type='registry' AND reference_key=$1 FOR UPDATE`, store.Schema.Prefix("asset_references")), pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]registryAssetReference)
	for rows.Next() {
		var slot string
		var reference registryAssetReference
		if err := rows.Scan(&slot, &reference.Owner, &reference.Hash); err != nil {
			return nil, err
		}
		result[slot] = reference
	}
	return result, rows.Err()
}

func (store *BlossomStore) latestRegistryHead(pluginID string) (nostr.Event, bool) {
	var best nostr.Event
	var bestRevision string
	for event := range store.Events.QueryEvents(nostr.Filter{
		Kinds: []nostr.Kind{nostr.KindSimpleGroupChatMessage},
		Tags:  nostr.TagMap{"h": {store.Config.Registry.ReleaseGroupID}, "t": {registryHeadTopic}},
	}, 1000) {
		head, err := parseRegistryHead(event)
		if err != nil || head.PluginID != pluginID {
			continue
		}
		revision, _ := canonicalDecimal(head.HeadRevision, false)
		current, _ := canonicalDecimal(bestRevision, false)
		if bestRevision == "" || revision.Cmp(current) > 0 {
			best, bestRevision = event, head.HeadRevision
		}
	}
	return best, bestRevision != ""
}

func (store *BlossomStore) eventByID(id nostr.ID) (nostr.Event, bool) {
	for event := range store.Events.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return event, true
	}
	return nostr.Event{}, false
}
