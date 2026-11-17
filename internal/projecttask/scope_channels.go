package projecttask

import (
	"context"
	"fiatjaf.com/nostr"
	"fmt"
)

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
