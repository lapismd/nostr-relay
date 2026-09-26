package zooid

import (
	"context"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
)

func (store *BlossomStore) backfillRegistryReferences(ctx context.Context) error {
	if !store.Config.Registry.Enabled {
		return nil
	}
	latest := make(map[string]nostr.Event)
	for event := range store.Events.QueryEvents(nostr.Filter{
		Kinds: []nostr.Kind{nostr.KindSimpleGroupChatMessage},
		Tags:  nostr.TagMap{"h": {store.Config.Registry.ReleaseGroupID}, "t": {registryHeadTopic}},
	}, 1000) {
		head, err := parseRegistryHead(event)
		if err != nil {
			continue
		}
		previous, exists := latest[head.PluginID]
		if !exists {
			latest[head.PluginID] = event
			continue
		}
		previousHead, _ := parseRegistryHead(previous)
		revision, _ := canonicalDecimal(head.HeadRevision, false)
		previousRevision, _ := canonicalDecimal(previousHead.HeadRevision, false)
		if revision.Cmp(previousRevision) > 0 {
			latest[head.PluginID] = event
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := GetDb().QueryContext(ctx, fmt.Sprintf("SELECT DISTINCT reference_key FROM %s WHERE reference_type='registry'", store.Schema.Prefix("asset_references")))
	if err != nil {
		return err
	}
	stale := make([]string, 0)
	for rows.Next() {
		var pluginID string
		if err := rows.Scan(&pluginID); err != nil {
			rows.Close()
			return err
		}
		if _, exists := latest[pluginID]; !exists {
			stale = append(stale, pluginID)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, pluginID := range stale {
		if err := store.reconcileRegistryReferences(ctx, nostr.Event{}, pluginID, "", nil, time.Now().Unix()); err != nil {
			return err
		}
	}
	for _, event := range latest {
		if err := store.projectRegistryHead(ctx, event, int64(event.CreatedAt)); err != nil {
			return err
		}
	}
	return nil
}

func (store *BlossomStore) rebuildRegistryReferences(ctx context.Context, deleted nostr.Event) error {
	if !store.Config.Registry.Enabled || deleted.Kind != nostr.KindSimpleGroupChatMessage || !hasExactTopic(deleted.Tags, registryHeadTopic) {
		return nil
	}
	head, err := parseRegistryHead(deleted)
	if err != nil || exactTag(deleted.Tags, "h") != store.Config.Registry.ReleaseGroupID {
		return nil
	}
	if current, found := store.latestRegistryHead(head.PluginID); found {
		return store.projectRegistryHead(ctx, current, time.Now().Unix())
	}
	return store.reconcileRegistryReferences(ctx, deleted, head.PluginID, "", nil, time.Now().Unix())
}
