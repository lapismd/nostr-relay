package projecttask

import (
	"cmp"
	"fmt"
	"slices"

	"fiatjaf.com/nostr"
)

type CommentEncoding string

const (
	CommentBuzzKind1 CommentEncoding = "buzz-kind-1"
	CommentNIP22     CommentEncoding = "nip-22"
)

type Activity struct {
	Event               nostr.Event
	Encoding            CommentEncoding
	AssignmentOperation bool
}

type TaskState struct {
	Status          nostr.Kind
	StatusEvent     *nostr.Event
	Assignees       []nostr.PubKey
	AssignmentHeads map[nostr.PubKey]nostr.ID
	Activity        []Activity
}

// ReduceTask derives deterministic current state from ordinary wire events.
// It does not write synthetic events or hide the raw event stream.
func ReduceTask(issue, repository nostr.Event, related []nostr.Event) (TaskState, error) {
	if issue.Kind != nostr.KindIssue {
		return TaskState{}, fmt.Errorf("task root must be kind 1621")
	}
	if repository.Kind != nostr.KindRepositoryAnnouncement {
		return TaskState{}, fmt.Errorf("task repository must be kind 30617")
	}

	events := append([]nostr.Event(nil), related...)
	slices.SortFunc(events, chronological)
	state := TaskState{
		Status:          nostr.KindStatusOpen,
		AssignmentHeads: make(map[nostr.PubKey]nostr.ID),
	}

	trustedStatus := map[nostr.PubKey]struct{}{issue.PubKey: {}, repository.PubKey: {}}
	for _, tag := range repository.Tags {
		if len(tag) == 0 || tag[0] != "maintainers" {
			continue
		}
		for _, value := range tag[1:] {
			if pubkey, err := nostr.PubKeyFromHex(value); err == nil {
				trustedStatus[pubkey] = struct{}{}
			}
		}
	}

	var uncausedSelf, authoritative, causalSelf []assignmentOperation
	for i := range events {
		event := events[i]
		if !referencesIssue(event, issue.ID) {
			continue
		}
		switch event.Kind {
		case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
			if _, trusted := trustedStatus[event.PubKey]; trusted && statusNewer(event, state.StatusEvent) {
				copy := event
				state.Status = event.Kind
				state.StatusEvent = &copy
			}
		case nostr.KindTextNote:
			op, ok := parseAssignment(event, issue.PubKey, repository.PubKey)
			state.Activity = append(state.Activity, Activity{
				Event:               event,
				Encoding:            CommentBuzzKind1,
				AssignmentOperation: ok,
			})
			if !ok {
				continue
			}
			switch op.class {
			case assignmentUncausedSelf:
				uncausedSelf = append(uncausedSelf, op)
			case assignmentAuthoritative:
				authoritative = append(authoritative, op)
			case assignmentCausalSelf:
				causalSelf = append(causalSelf, op)
			}
		case nostr.KindComment:
			if isTaskCommentFor(event, issue.ID) {
				state.Activity = append(state.Activity, Activity{Event: event, Encoding: CommentNIP22})
			}
		}
	}

	for _, operation := range append(append(uncausedSelf, authoritative...), causalSelf...) {
		applyAssignment(&state, operation)
	}
	for pubkey := range state.AssignmentHeads {
		if stateIsAssigned(state, pubkey, append(append(uncausedSelf, authoritative...), causalSelf...)) {
			state.Assignees = append(state.Assignees, pubkey)
		}
	}
	slices.SortFunc(state.Assignees, func(left, right nostr.PubKey) int {
		return cmp.Compare(left.Hex(), right.Hex())
	})
	return state, nil
}

type assignmentClass uint8

const (
	assignmentUncausedSelf assignmentClass = iota
	assignmentAuthoritative
	assignmentCausalSelf
)

type assignmentOperation struct {
	id       nostr.ID
	assign   bool
	pubkeys  []nostr.PubKey
	prior    *nostr.ID
	class    assignmentClass
	accepted bool
}

func parseAssignment(event nostr.Event, issueAuthor, repositoryOwner nostr.PubKey) (assignmentOperation, bool) {
	assignment := tagHasValue(event.Tags, "t", "assignment")
	unassignment := tagHasValue(event.Tags, "t", "unassignment")
	if assignment == unassignment {
		return assignmentOperation{}, false
	}

	pubkeys := make([]nostr.PubKey, 0)
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pubkey, err := nostr.PubKeyFromHex(tag[1])
			if err != nil {
				return assignmentOperation{}, false
			}
			pubkeys = append(pubkeys, pubkey)
		}
	}
	authoritative := event.PubKey == issueAuthor || event.PubKey == repositoryOwner
	self := len(pubkeys) == 1 && pubkeys[0] == event.PubKey
	if !authoritative && !self {
		return assignmentOperation{}, false
	}
	op := assignmentOperation{id: event.ID, assign: assignment, pubkeys: pubkeys}
	if authoritative {
		op.class = assignmentAuthoritative
		return op, true
	}

	priorTags := tagsNamed(event.Tags, "prior")
	if len(priorTags) == 0 {
		op.class = assignmentUncausedSelf
		return op, true
	}
	if len(priorTags) != 1 || len(priorTags[0]) < 2 {
		return assignmentOperation{}, false
	}
	prior, err := nostr.IDFromHex(priorTags[0][1])
	if err != nil {
		return assignmentOperation{}, false
	}
	op.class = assignmentCausalSelf
	op.prior = &prior
	return op, true
}

func applyAssignment(state *TaskState, operation assignmentOperation) {
	if operation.prior != nil {
		if len(operation.pubkeys) != 1 || state.AssignmentHeads[operation.pubkeys[0]] != *operation.prior {
			return
		}
	}
	for _, pubkey := range operation.pubkeys {
		state.AssignmentHeads[pubkey] = operation.id
	}
	operation.accepted = true
}

func stateIsAssigned(state TaskState, pubkey nostr.PubKey, operations []assignmentOperation) bool {
	head, exists := state.AssignmentHeads[pubkey]
	if !exists {
		return false
	}
	for i := len(operations) - 1; i >= 0; i-- {
		operation := operations[i]
		if operation.id != head {
			continue
		}
		for _, candidate := range operation.pubkeys {
			if candidate == pubkey {
				return operation.assign
			}
		}
	}
	return false
}

func referencesIssue(event nostr.Event, issueID nostr.ID) bool {
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		if (tag[0] == "E" || tag[0] == "e") && tag[1] == issueID.Hex() {
			if tag[0] == "E" || len(tag) < 4 || tag[3] == "root" {
				return true
			}
		}
	}
	return false
}

func isTaskCommentFor(event nostr.Event, issueID nostr.ID) bool {
	return tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue)) &&
		tagHasValue(event.Tags, "E", issueID.Hex())
}

func chronological(left, right nostr.Event) int {
	if order := cmp.Compare(left.CreatedAt, right.CreatedAt); order != 0 {
		return order
	}
	return cmp.Compare(left.ID.Hex(), right.ID.Hex())
}

func statusNewer(candidate nostr.Event, current *nostr.Event) bool {
	if current == nil || candidate.CreatedAt > current.CreatedAt {
		return true
	}
	return candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() < current.ID.Hex()
}
