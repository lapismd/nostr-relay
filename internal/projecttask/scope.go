package projecttask

import (
	"context"
	"errors"
	"fmt"

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
	Channel  string
}

type ScopeResolver struct {
	Reader EventReader
}

// Resolve derives the NIP-29 channel without mutating the signed event.
func (resolver ScopeResolver) Resolve(ctx context.Context, event nostr.Event) (ResolvedScope, error) {
	state := resolutionState{
		resolver:   resolver,
		channels:   make(map[string]struct{}),
		visitedIDs: make(map[nostr.ID]struct{}),
		visitedA:   make(map[string]struct{}),
	}
	relevant, err := state.resolve(ctx, event, 0)
	if err != nil {
		return ResolvedScope{Relevant: relevant}, err
	}
	if len(state.channels) > 1 {
		return ResolvedScope{Relevant: relevant}, fmt.Errorf("%w: conflicting buzz-channel bindings", ErrScopeUnresolved)
	}
	scope := ResolvedScope{Relevant: relevant}
	for channel := range state.channels {
		scope.Channel = channel
	}
	return scope, nil
}

type resolutionState struct {
	resolver   ScopeResolver
	channels   map[string]struct{}
	visitedIDs map[nostr.ID]struct{}
	visitedA   map[string]struct{}
}

func (state *resolutionState) resolve(ctx context.Context, event nostr.Event, depth int) (bool, error) {
	if depth > 8 {
		return true, fmt.Errorf("%w: ancestry exceeds eight events", ErrScopeUnresolved)
	}
	relevant := scopeRelevant(event)
	if !relevant {
		return false, nil
	}
	if event.ID != nostr.ZeroID {
		if _, seen := state.visitedIDs[event.ID]; seen {
			return true, fmt.Errorf("%w: cyclic event ancestry", ErrScopeUnresolved)
		}
		state.visitedIDs[event.ID] = struct{}{}
		defer delete(state.visitedIDs, event.ID)
	}

	for _, name := range []string{TagBuzzChannel, "h"} {
		for _, tag := range tagsNamed(event.Tags, name) {
			if len(tag) >= 2 && tag[1] != "" {
				state.channels[tag[1]] = struct{}{}
			}
		}
	}

	switch event.Kind {
	case KindProject:
		for _, tag := range tagsNamed(event.Tags, "a") {
			if err := state.resolveRepositoryTag(ctx, tag, depth); err != nil {
				return true, err
			}
		}
	case nostr.KindRepositoryAnnouncement:
		// Direct binding only.
	case nostr.KindIssue:
		coordinates := tagsNamed(event.Tags, "a")
		if len(coordinates) != 1 {
			return true, fmt.Errorf("%w: issue repository is missing or ambiguous", ErrScopeUnresolved)
		}
		if err := state.resolveRepositoryTag(ctx, coordinates[0], depth); err != nil {
			return true, err
		}
		if parent := event.Tags.Find("parent"); parent != nil {
			parentEvent, err := state.readEventTag(ctx, parent, "subtask parent")
			if err != nil {
				return true, err
			}
			if parentEvent.Kind != nostr.KindIssue || repositoryTagValue(parentEvent) != repositoryTagValue(event) {
				return true, fmt.Errorf("%w: subtask parent must be an issue in the same repository", ErrScopeUnresolved)
			}
			if _, err := state.resolve(ctx, parentEvent, depth+1); err != nil {
				return true, err
			}
		}
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft,
		nostr.KindTextNote:
		if err := state.resolveRepositoryTags(ctx, event, depth); err != nil {
			return true, err
		}
		root, err := state.readRootIssue(ctx, event)
		if err != nil {
			return true, err
		}
		if _, err := state.resolve(ctx, root, depth+1); err != nil {
			return true, err
		}
	case nostr.KindComment:
		root, err := state.readUpperRootIssue(ctx, event)
		if err != nil {
			return true, err
		}
		if _, err := state.resolve(ctx, root, depth+1); err != nil {
			return true, err
		}
	case nostr.KindDeletion:
		for _, tag := range tagsNamed(event.Tags, "a") {
			kind, owner, id, ok := parseAddress(tag[1])
			if !ok || (kind != KindProject && kind != nostr.KindRepositoryAnnouncement) {
				continue
			}
			if err := state.resolveAddress(ctx, kind, owner, id, depth); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}

func (state *resolutionState) resolveRepositoryTags(ctx context.Context, event nostr.Event, depth int) error {
	for _, tag := range tagsNamed(event.Tags, "a") {
		if len(tag) < 2 {
			return fmt.Errorf("%w: repository coordinate is missing", ErrScopeUnresolved)
		}
		if _, err := ParseRepositoryCoordinate(tag[1]); err != nil {
			continue
		}
		if err := state.resolveRepositoryTag(ctx, tag, depth); err != nil {
			return err
		}
	}
	return nil
}

func (state *resolutionState) resolveRepositoryTag(ctx context.Context, tag nostr.Tag, depth int) error {
	if len(tag) < 2 {
		return fmt.Errorf("%w: repository coordinate is missing", ErrScopeUnresolved)
	}
	coordinate, err := ParseRepositoryCoordinate(tag[1])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrScopeUnresolved, err)
	}
	return state.resolveAddress(ctx, nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID, depth)
}

func (state *resolutionState) resolveAddress(ctx context.Context, kind nostr.Kind, owner nostr.PubKey, id string, depth int) error {
	key := fmt.Sprintf("%d:%s:%s", kind, owner.Hex(), id)
	if _, seen := state.visitedA[key]; seen {
		return fmt.Errorf("%w: cyclic address ancestry", ErrScopeUnresolved)
	}
	state.visitedA[key] = struct{}{}
	defer delete(state.visitedA, key)
	if state.resolver.Reader == nil {
		return fmt.Errorf("%w: no event reader", ErrScopeUnresolved)
	}
	event, found, err := state.resolver.Reader.EventByAddress(ctx, kind, owner, id)
	if err != nil {
		return fmt.Errorf("%w: reading %s: %v", ErrScopeUnresolved, key, err)
	}
	if !found {
		return fmt.Errorf("%w: address %s was not found", ErrScopeUnresolved, key)
	}
	_, err = state.resolve(ctx, event, depth+1)
	return err
}

func (state *resolutionState) readRootIssue(ctx context.Context, event nostr.Event) (nostr.Event, error) {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "e" && (len(tag) < 4 || tag[3] == "root") {
			return state.readIssueID(ctx, tag[1])
		}
	}
	return nostr.Event{}, fmt.Errorf("%w: task root e tag was not found", ErrScopeUnresolved)
}

func (state *resolutionState) readUpperRootIssue(ctx context.Context, event nostr.Event) (nostr.Event, error) {
	tag := event.Tags.Find("E")
	if tag == nil {
		return nostr.Event{}, fmt.Errorf("%w: task root E tag was not found", ErrScopeUnresolved)
	}
	return state.readIssueID(ctx, tag[1])
}

func (state *resolutionState) readIssueID(ctx context.Context, value string) (nostr.Event, error) {
	id, err := nostr.IDFromHex(value)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: invalid task root event ID", ErrScopeUnresolved)
	}
	if state.resolver.Reader == nil {
		return nostr.Event{}, fmt.Errorf("%w: no event reader", ErrScopeUnresolved)
	}
	event, found, err := state.resolver.Reader.EventByID(ctx, id)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: reading task root: %v", ErrScopeUnresolved, err)
	}
	if !found || event.Kind != nostr.KindIssue {
		return nostr.Event{}, fmt.Errorf("%w: task root issue was not found", ErrScopeUnresolved)
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
	event, found, err := state.resolver.Reader.EventByID(ctx, id)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("%w: reading %s: %v", ErrScopeUnresolved, label, err)
	}
	if !found {
		return nostr.Event{}, fmt.Errorf("%w: %s was not found", ErrScopeUnresolved, label)
	}
	return event, nil
}

func scopeRelevant(event nostr.Event) bool {
	switch event.Kind {
	case KindProject, nostr.KindRepositoryAnnouncement, nostr.KindIssue:
		return true
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
		return hasRootEventTag(event.Tags) || hasRepositoryCoordinate(event.Tags)
	case nostr.KindTextNote:
		return isBuzzTaskNote(event)
	case nostr.KindComment:
		return isNIP22TaskComment(event)
	case nostr.KindDeletion:
		return targetsProjectOrRepository(event)
	default:
		return false
	}
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
	if tag == nil {
		return ""
	}
	return tag[1]
}
