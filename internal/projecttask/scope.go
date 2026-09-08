package projecttask

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"fiatjaf.com/nostr"
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

	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft,
		nostr.KindTextNote:
		if event.Kind == nostr.KindTextNote && !isBuzzWorkItemNote(event) {
			return result, nil
		}
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

	case nostr.KindDeletion:
		return state.resolveDeletion(ctx, event, depth)
	}
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

func (state *resolutionState) readLowerRootWorkItem(ctx context.Context, event nostr.Event) (nostr.Event, error) {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "e" && (len(tag) < 4 || tag[3] == "root") {
			return state.readWorkItemID(ctx, tag[1], 0, "work-item root")
		}
	}
	for _, tag := range tagsNamed(event.Tags, "E") {
		if len(tag) >= 2 {
			return state.readWorkItemID(ctx, tag[1], 0, "work-item root")
		}
	}
	return nostr.Event{}, fmt.Errorf("%w: work-item root tag was not found", ErrScopeUnresolved)
}

func (state *resolutionState) readUpperRoot(ctx context.Context, event nostr.Event, kind nostr.Kind, label string) (nostr.Event, error) {
	tag := event.Tags.Find("E")
	if tag == nil || len(tag) < 2 {
		return nostr.Event{}, fmt.Errorf("%w: %s E tag was not found", ErrScopeUnresolved, label)
	}
	return state.readWorkItemID(ctx, tag[1], kind, label)
}

func (state *resolutionState) readWorkItemID(ctx context.Context, value string, kind nostr.Kind, label string) (nostr.Event, error) {
	id, err := nostr.IDFromHex(value)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: invalid %s event ID", ErrScopeUnresolved, label)
	}
	if state.resolver.Reader == nil {
		return nostr.Event{}, fmt.Errorf("%w: no event reader", ErrScopeUnresolved)
	}
	event, found, err := state.resolver.Reader.EventByID(ctx, id)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: reading %s: %v", ErrScopeUnresolved, label, err)
	}
	if !found || (kind != 0 && event.Kind != kind) || (kind == 0 && event.Kind != nostr.KindIssue && event.Kind != KindPullRequest) {
		return nostr.Event{}, fmt.Errorf("%w: %s was not found", ErrScopeUnresolved, label)
	}
	return event, nil
}

func (state *resolutionState) readEventTag(ctx context.Context, tag nostr.Tag, label string) (nostr.Event, error) {
	if len(tag) < 2 {
		return nostr.Event{}, fmt.Errorf("%w: %s ID is missing", ErrScopeUnresolved, label)
	}
	id, err := nostr.IDFromHex(tag[1])
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: invalid %s ID", ErrScopeUnresolved, label)
	}
	if state.resolver.Reader == nil {
		return nostr.Event{}, fmt.Errorf("%w: no event reader", ErrScopeUnresolved)
	}
	event, found, err := state.resolver.Reader.EventByID(ctx, id)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: reading %s: %v", ErrScopeUnresolved, label, err)
	}
	if !found {
		return nostr.Event{}, fmt.Errorf("%w: %s was not found", ErrScopeUnresolved, label)
	}
	return event, nil
}

func (state *resolutionState) requireSingleScope(result resolutionResult) (resolutionResult, error) {
	if len(result.channels) > 1 {
		return result, fmt.Errorf("%w: conflicting buzz-channel bindings", ErrScopeUnresolved)
	}
	return result, nil
}

func directScopeMustBeInherited(event nostr.Event, inherited map[string]struct{}) error {
	direct := make(map[string]struct{})
	addDirectChannels(direct, event)
	for channel := range direct {
		if _, allowed := inherited[channel]; !allowed {
			return fmt.Errorf("%w: descendant h conflicts with pull-request ancestry", ErrScopeUnresolved)
		}
	}
	return nil
}

func addDirectChannels(channels map[string]struct{}, event nostr.Event) {
	addTagChannels(channels, event.Tags, TagBuzzChannel)
	addTagChannels(channels, event.Tags, "h")
}

func addTagChannels(channels map[string]struct{}, tags nostr.Tags, name string) {
	for _, tag := range tagsNamed(tags, name) {
		if len(tag) >= 2 && tag[1] != "" {
			channels[tag[1]] = struct{}{}
		}
	}
}

func mergeChannels(target, source map[string]struct{}) {
	for channel := range source {
		target[channel] = struct{}{}
	}
}

func mergeResolution(target *resolutionResult, source resolutionResult) {
	target.relevant = target.relevant || source.relevant
	target.pullRequestGraph = target.pullRequestGraph || source.pullRequestGraph
	mergeChannels(target.channels, source.channels)
}

func repositoryCoordinateForRoot(event nostr.Event) (RepositoryCoordinate, error) {
	tag := event.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		return RepositoryCoordinate{}, fmt.Errorf("%w: root repository is missing", ErrScopeUnresolved)
	}
	coordinate, err := ParseRepositoryCoordinate(tag[1])
	if err != nil {
		return RepositoryCoordinate{}, fmt.Errorf("%w: root repository is invalid", ErrScopeUnresolved)
	}
	return coordinate, nil
}

func hasRepositoryCoordinate(tags nostr.Tags) bool {
	for _, tag := range tagsNamed(tags, "a") {
		if len(tag) >= 2 {
			if _, err := ParseRepositoryCoordinate(tag[1]); err == nil {
				return true
			}
		}
	}
	return false
}

func repositoryTagValue(event nostr.Event) string {
	tag := event.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		return ""
	}
	return tag[1]
}
