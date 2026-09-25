package projecttask

import (
	"context"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"slices"
)

var ErrScopeUnresolved = errors.New("project/task scope unresolved")

// EventReader is the persistence boundary used by scope resolution. A managed
// owner implementation can be added alongside it without changing wire events.
type EventReader interface {
	EventByID(context.Context, nostr.ID) (nostr.Event, bool, error)
	EventByAddress(context.Context, nostr.Kind, nostr.PubKey, string) (nostr.Event, bool, error)
}

type ResolvedScope struct {
	Relevant bool
	Channels []string
}

type ScopeResolver struct {
	Reader EventReader
}

// Resolve derives a sorted, unique NIP-29 channel intersection without
// mutating the signed event. Multiple channels are permitted only for a PR
// graph: its repository scope plus the root's optional origin h tag.
func (resolver ScopeResolver) Resolve(ctx context.Context, event nostr.Event) (ResolvedScope, error) {
	state := resolutionState{
		resolver:   resolver,
		visitedIDs: make(map[nostr.ID]struct{}),
		visitedA:   make(map[string]struct{}),
	}
	result, err := state.resolve(ctx, event, 0)
	if err != nil {
		return ResolvedScope{Relevant: result.relevant}, err
	}
	channels := make([]string, 0, len(result.channels))
	for channel := range result.channels {
		channels = append(channels, channel)
	}
	slices.Sort(channels)
	return ResolvedScope{Relevant: result.relevant, Channels: channels}, nil
}

type resolutionResult struct {
	relevant         bool
	pullRequestGraph bool
	channels         map[string]struct{}
}

type resolutionState struct {
	resolver   ScopeResolver
	visitedIDs map[nostr.ID]struct{}
	visitedA   map[string]struct{}
}

func (state *resolutionState) resolve(ctx context.Context, event nostr.Event, depth int) (resolutionResult, error) {
	result := resolutionResult{channels: make(map[string]struct{})}
	if depth > 8 {
		result.relevant = true
		return result, fmt.Errorf("%w: ancestry exceeds eight events", ErrScopeUnresolved)
	}
	if event.ID != nostr.ZeroID {
		if _, seen := state.visitedIDs[event.ID]; seen {
			result.relevant = true
			return result, fmt.Errorf("%w: cyclic event ancestry", ErrScopeUnresolved)
		}
		state.visitedIDs[event.ID] = struct{}{}
		defer delete(state.visitedIDs, event.ID)
	}

	switch event.Kind {
	case KindProject:
		result.relevant = true
		addDirectChannels(result.channels, event)
		for _, tag := range tagsNamed(event.Tags, "a") {
			repository, err := state.resolveRepositoryTag(ctx, tag, depth)
			if err != nil {
				return result, err
			}
			mergeChannels(result.channels, repository.channels)
		}
		return state.requireSingleScope(result)

	case nostr.KindRepositoryAnnouncement:
		result.relevant = true
		addDirectChannels(result.channels, event)
		return state.requireSingleScope(result)

	case nostr.KindIssue:
		result.relevant = true
		addDirectChannels(result.channels, event)
		coordinates := tagsNamed(event.Tags, "a")
		if len(coordinates) != 1 {
			return result, fmt.Errorf("%w: issue repository is missing or ambiguous", ErrScopeUnresolved)
		}
		repository, err := state.resolveRepositoryTag(ctx, coordinates[0], depth)
		if err != nil {
			return result, err
		}
		mergeChannels(result.channels, repository.channels)
		if parent := event.Tags.Find("parent"); parent != nil {
			parentEvent, err := state.readEventTag(ctx, parent, "subtask parent")
			if err != nil {
				return result, err
			}
			if parentEvent.Kind != nostr.KindIssue || repositoryTagValue(parentEvent) != repositoryTagValue(event) {
				return result, fmt.Errorf("%w: subtask parent must be an issue in the same repository", ErrScopeUnresolved)
			}
			parentScope, err := state.resolve(ctx, parentEvent, depth+1)
			if err != nil {
				return result, err
			}
			mergeChannels(result.channels, parentScope.channels)
		}
		return state.requireSingleScope(result)

	case KindPullRequest:
		result.relevant = true
		result.pullRequestGraph = true
		addTagChannels(result.channels, event.Tags, "h")
		coordinates := tagsNamed(event.Tags, "a")
		if len(coordinates) != 1 {
			return result, fmt.Errorf("%w: pull request repository is missing or ambiguous", ErrScopeUnresolved)
		}
		repository, err := state.resolveRepositoryTag(ctx, coordinates[0], depth)
		if err != nil {
			return result, err
		}
		mergeChannels(result.channels, repository.channels)
		return result, nil

	case KindPullRequestUpdate:
		result.relevant = true
		root, err := state.readUpperRoot(ctx, event, KindPullRequest, "pull request update")
		if err != nil {
			return result, err
		}
		if firstTagValue(event.Tags, "P") != root.PubKey.Hex() {
			return result, fmt.Errorf("%w: pull request update P tag does not identify the root author", ErrScopeUnresolved)
		}
		if repositoryTagValue(event) != repositoryTagValue(root) {
			return result, fmt.Errorf("%w: pull request update repository does not match the root", ErrScopeUnresolved)
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

	case nostr.KindTextNote:
		if isCIActivity(event) {
			return state.resolveCIActivity(ctx, event, depth)
		}
		if !isBuzzWorkItemNote(event) {
			return result, nil
		}
		fallthrough
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
		result.relevant = true
		root, err := state.readLowerRootWorkItem(ctx, event)
		if err != nil {
			return result, err
		}
		coordinate, err := repositoryCoordinateForRoot(root)
		if err != nil {
			return result, err
		}
		if !repositoryTagMatchesIfPresent(event, coordinate) {
			return result, fmt.Errorf("%w: descendant repository does not match the root", ErrScopeUnresolved)
		}
		rootScope, err := state.resolve(ctx, root, depth+1)
		if err != nil {
			return result, err
		}
		if root.Kind == KindPullRequest {
			if err := directScopeMustBeInherited(event, rootScope.channels); err != nil {
				return result, err
			}
			mergeResolution(&result, rootScope)
			return result, nil
		}
		addDirectChannels(result.channels, event)
		mergeChannels(result.channels, rootScope.channels)
		return state.requireSingleScope(result)

	case nostr.KindComment:
		if !isNIP22WorkItemComment(event) {
			return result, nil
		}
		result.relevant = true
		rootKind := nostr.KindIssue
		if tagHasValue(event.Tags, "K", kindNumber(KindPullRequest)) {
			rootKind = KindPullRequest
		}
		root, err := state.readUpperRoot(ctx, event, rootKind, "NIP-22 comment")
		if err != nil {
			return result, err
		}
		if firstTagValue(event.Tags, "P") != root.PubKey.Hex() {
			return result, fmt.Errorf("%w: NIP-22 P tag does not identify the root author", ErrScopeUnresolved)
		}
		if err := state.validateNIP22Parent(ctx, event, root, depth); err != nil {
			return result, err
		}
		rootScope, err := state.resolve(ctx, root, depth+1)
		if err != nil {
			return result, err
		}
		if root.Kind == KindPullRequest {
			if err := directScopeMustBeInherited(event, rootScope.channels); err != nil {
				return result, err
			}
			mergeResolution(&result, rootScope)
			return result, nil
		}
		addDirectChannels(result.channels, event)
		mergeChannels(result.channels, rootScope.channels)
		return state.requireSingleScope(result)

	case nostr.KindReaction:
		result.relevant = hasRepositoryCoordinate(event.Tags)
		if !result.relevant {
			return result, nil
		}
		target, err := state.readEventTag(ctx, event.Tags.Find("e"), "reaction target")
		if err != nil {
			return result, err
		}
		if firstTagValue(event.Tags, "p") != target.PubKey.Hex() || firstTagValue(event.Tags, "k") != kindNumber(target.Kind) {
			return result, fmt.Errorf("%w: reaction target metadata conflicts", ErrScopeUnresolved)
		}
		if repositoryTagValue(target) == "" || repositoryTagValue(event) != repositoryTagValue(target) {
			return result, fmt.Errorf("%w: reaction repository does not match its target", ErrScopeUnresolved)
		}
		targetScope, err := state.resolve(ctx, target, depth+1)
		if err != nil {
			return result, err
		}
		if !targetScope.relevant {
			return result, fmt.Errorf("%w: reaction target is outside the project graph", ErrScopeUnresolved)
		}
		if err := directScopeMustBeInherited(event, targetScope.channels); err != nil {
			return result, err
		}
		mergeResolution(&result, targetScope)
		if result.pullRequestGraph {
			return result, nil
		}
		return state.requireSingleScope(result)

	case nostr.KindDeletion:
		return state.resolveDeletion(ctx, event, depth)
	}
	return result, nil
}
