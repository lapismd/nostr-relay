package projecttask

import (
	"context"
	"fiatjaf.com/nostr"
	"fmt"
)

func (state *resolutionState) resolveCIActivity(ctx context.Context, event nostr.Event, depth int) (resolutionResult, error) {
	result := resolutionResult{relevant: true, channels: make(map[string]struct{})}
	coordinates := tagsNamed(event.Tags, "a")
	if len(coordinates) != 1 {
		return result, fmt.Errorf("%w: CI activity repository is missing or ambiguous", ErrScopeUnresolved)
	}
	repository, err := state.resolveRepositoryTag(ctx, coordinates[0], depth)
	if err != nil {
		return result, err
	}
	mergeChannels(result.channels, repository.channels)
	rootTag := event.Tags.Find("e")
	if rootTag == nil {
		return state.requireSingleScope(result)
	}
	root, err := state.readEventTag(ctx, rootTag, "CI pull request root")
	if err != nil {
		return result, err
	}
	if root.Kind != KindPullRequest || repositoryTagValue(root) != coordinates[0][1] {
		return result, fmt.Errorf("%w: CI activity root must be a pull request in the same repository", ErrScopeUnresolved)
	}
	rootScope, err := state.resolve(ctx, root, depth+1)
	if err != nil {
		return result, err
	}
	if err := directScopeMustBeInherited(event, rootScope.channels); err != nil {
		return result, err
	}
	mergeResolution(&result, rootScope)
	return result, nil
}

func (state *resolutionState) resolveDeletion(ctx context.Context, event nostr.Event, depth int) (resolutionResult, error) {
	result := resolutionResult{channels: make(map[string]struct{})}
	for _, tag := range tagsNamed(event.Tags, "a") {
		if len(tag) < 2 {
			continue
		}
		kind, owner, id, ok := parseAddress(tag[1])
		if !ok || (kind != KindProject && kind != nostr.KindRepositoryAnnouncement) {
			continue
		}
		if owner != event.PubKey {
			return result, fmt.Errorf("%w: deletion signer does not own the addressed event", ErrScopeUnresolved)
		}
		target, err := state.resolveAddress(ctx, kind, owner, id, depth)
		if err != nil {
			return result, err
		}
		mergeResolution(&result, target)
	}
	for _, tag := range tagsNamed(event.Tags, "e") {
		target, err := state.readEventTag(ctx, tag, "deletion target")
		if err != nil {
			return result, err
		}
		targetScope, err := state.resolve(ctx, target, depth+1)
		mergeResolution(&result, targetScope)
		if err != nil {
			return result, err
		}
		if !targetScope.relevant {
			continue
		}
		if target.PubKey != event.PubKey {
			return result, fmt.Errorf("%w: only an event signer may delete it", ErrScopeUnresolved)
		}
	}
	if !result.relevant {
		return result, nil
	}
	if result.pullRequestGraph {
		if err := directScopeMustBeInherited(event, result.channels); err != nil {
			return result, err
		}
		return result, nil
	}
	addDirectChannels(result.channels, event)
	return state.requireSingleScope(result)
}

func (state *resolutionState) validateNIP22Parent(ctx context.Context, event, root nostr.Event, depth int) error {
	parentTag := event.Tags.Find("e")
	parentKindTag := event.Tags.Find("k")
	parentAuthorTag := event.Tags.Find("p")
	if parentTag == nil || parentKindTag == nil || parentAuthorTag == nil || len(parentKindTag) < 2 || len(parentAuthorTag) < 2 {
		return fmt.Errorf("%w: NIP-22 immediate parent is incomplete", ErrScopeUnresolved)
	}
	parentID, err := nostr.IDFromHex(parentTag[1])
	if err != nil {
		return fmt.Errorf("%w: invalid NIP-22 immediate parent ID", ErrScopeUnresolved)
	}
	if parentID == root.ID {
		if parentKindTag[1] != kindNumber(root.Kind) || parentAuthorTag[1] != root.PubKey.Hex() {
			return fmt.Errorf("%w: NIP-22 immediate root metadata conflicts", ErrScopeUnresolved)
		}
		return nil
	}
	parent, err := state.readEventTag(ctx, parentTag, "NIP-22 immediate parent")
	if err != nil {
		return err
	}
	if parent.Kind != nostr.KindComment || parentKindTag[1] != kindNumber(parent.Kind) || parentAuthorTag[1] != parent.PubKey.Hex() {
		return fmt.Errorf("%w: NIP-22 immediate parent metadata conflicts", ErrScopeUnresolved)
	}
	if firstTagValue(parent.Tags, "E") != root.ID.Hex() || firstTagValue(parent.Tags, "K") != kindNumber(root.Kind) || firstTagValue(parent.Tags, "P") != root.PubKey.Hex() {
		return fmt.Errorf("%w: NIP-22 parent belongs to another root", ErrScopeUnresolved)
	}
	_, err = state.resolve(ctx, parent, depth+1)
	return err
}

func (state *resolutionState) resolveRepositoryTag(ctx context.Context, tag nostr.Tag, depth int) (resolutionResult, error) {
	empty := resolutionResult{relevant: true, channels: make(map[string]struct{})}
	if len(tag) < 2 {
		return empty, fmt.Errorf("%w: repository coordinate is missing", ErrScopeUnresolved)
	}
	coordinate, err := ParseRepositoryCoordinate(tag[1])
	if err != nil {
		return empty, fmt.Errorf("%w: %v", ErrScopeUnresolved, err)
	}
	return state.resolveAddress(ctx, nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID, depth)
}

func (state *resolutionState) resolveAddress(ctx context.Context, kind nostr.Kind, owner nostr.PubKey, id string, depth int) (resolutionResult, error) {
	empty := resolutionResult{relevant: true, channels: make(map[string]struct{})}
	key := fmt.Sprintf("%d:%s:%s", kind, owner.Hex(), id)
	if _, seen := state.visitedA[key]; seen {
		return empty, fmt.Errorf("%w: cyclic address ancestry", ErrScopeUnresolved)
	}
	state.visitedA[key] = struct{}{}
	defer delete(state.visitedA, key)
	if state.resolver.Reader == nil {
		return empty, fmt.Errorf("%w: no event reader", ErrScopeUnresolved)
	}
	event, found, err := state.resolver.Reader.EventByAddress(ctx, kind, owner, id)
	if err != nil {
		return empty, fmt.Errorf("%w: reading %s: %v", ErrScopeUnresolved, key, err)
	}
	if !found {
		return empty, fmt.Errorf("%w: address %s was not found", ErrScopeUnresolved, key)
	}
	return state.resolve(ctx, event, depth+1)
}
