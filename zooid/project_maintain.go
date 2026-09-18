package zooid

import (
	"context"

	"fiatjaf.com/nostr"
	"zooid/internal/projecttask"
)

func (policy *ProjectTaskPolicy) checkMaintainRules(ctx context.Context, event nostr.Event) string {
	switch {
	case event.Kind == nostr.KindRepositoryAnnouncement:
		return policy.checkRepositoryAnnouncement(ctx, event)
	case event.Kind == nostr.KindStatusApplied:
		if !policy.canMaintainRepositoryEvent(ctx, event) {
			return "restricted: merging requires a channel maintainer role"
		}
	case projecttask.IsReviewDecision(event):
		return policy.checkReviewDecision(ctx, event)
	}
	return ""
}

func (policy *ProjectTaskPolicy) checkRepositoryAnnouncement(ctx context.Context, event nostr.Event) string {
	channel := projecttask.BuzzChannel(event)
	if channel == "" {
		return "restricted: repository announcements require a channel you can maintain"
	}
	if !policy.Groups.CanMaintain(channel, event.PubKey) {
		return "restricted: creating or replacing a repository requires a channel maintainer role"
	}
	previous, found := policy.previousRepositoryAnnouncement(ctx, event)
	if !found {
		return ""
	}
	previousChannel := projecttask.BuzzChannel(previous)
	if previousChannel == "" || previousChannel == channel {
		return ""
	}
	if !policy.Groups.CanMaintain(previousChannel, event.PubKey) {
		return "restricted: rebinding a repository channel requires maintainer or administrator access on the previous channel"
	}
	return ""
}

func (policy *ProjectTaskPolicy) checkReviewDecision(ctx context.Context, event nostr.Event) string {
	root, repository, ok := policy.reviewTargets(ctx, event)
	if !ok {
		return "restricted: review decisions require a resolvable pull request"
	}
	if !policy.Groups.CanMaintain(projecttask.BuzzChannel(repository), event.PubKey) {
		return "restricted: trusted reviews require a channel maintainer role"
	}
	if event.PubKey == root.PubKey {
		return "restricted: pull request authors cannot approve or request changes on their own pull request"
	}
	return ""
}

func (policy *ProjectTaskPolicy) canMaintainRepositoryEvent(ctx context.Context, event nostr.Event) bool {
	_, repository, ok := policy.reviewTargets(ctx, event)
	if !ok {
		coordinate, err := projecttask.ParseRepositoryCoordinate(firstTagValue(event.Tags, "a"))
		if err != nil {
			return false
		}
		var found bool
		repository, found, _ = policy.EventByAddress(ctx, nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID)
		if !found {
			return false
		}
	}
	return policy.Groups.CanMaintain(projecttask.BuzzChannel(repository), event.PubKey)
}

func (policy *ProjectTaskPolicy) repositoryAuthority(repository nostr.Event) projecttask.CollaborationAuthority {
	channel := projecttask.BuzzChannel(repository)
	return projecttask.CollaborationAuthority{
		CanMaintain: func(pubkey nostr.PubKey) bool {
			return policy.Groups.CanMaintain(channel, pubkey)
		},
		IsAdmin: func(pubkey nostr.PubKey) bool {
			return policy.Groups.IsGroupCreator(channel, pubkey) ||
				(policy.Groups.Config != nil &&
					policy.Groups.Config.Info.Pubkey != "" &&
					policy.Groups.Config.CanManage(pubkey))
		},
	}
}

func (policy *ProjectTaskPolicy) previousRepositoryAnnouncement(ctx context.Context, event nostr.Event) (nostr.Event, bool) {
	identifier := event.Tags.GetD()
	if identifier == "" {
		return nostr.Event{}, false
	}
	previous, found, err := policy.EventByAddress(ctx, nostr.KindRepositoryAnnouncement, event.PubKey, identifier)
	if err != nil || !found || previous.ID == event.ID {
		return nostr.Event{}, false
	}
	return previous, true
}

func (policy *ProjectTaskPolicy) reviewTargets(ctx context.Context, event nostr.Event) (nostr.Event, nostr.Event, bool) {
	rootID := reviewRootID(event)
	if rootID == nostr.ZeroID {
		return nostr.Event{}, nostr.Event{}, false
	}
	root, found, err := policy.EventByID(ctx, rootID)
	if err != nil || !found || root.Kind != projecttask.KindPullRequest && root.Kind != nostr.KindIssue {
		return nostr.Event{}, nostr.Event{}, false
	}
	coordinate, err := projecttask.ParseRepositoryCoordinate(firstTagValue(root.Tags, "a"))
	if err != nil {
		return nostr.Event{}, nostr.Event{}, false
	}
	repository, found, err := policy.EventByAddress(ctx, nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID)
	if err != nil || !found {
		return nostr.Event{}, nostr.Event{}, false
	}
	return root, repository, true
}

func reviewRootID(event nostr.Event) nostr.ID {
	for _, name := range []string{"E", "e"} {
		if tag := event.Tags.Find(name); tag != nil && len(tag) >= 2 {
			id, err := nostr.IDFromHex(tag[1])
			if err == nil {
				return id
			}
		}
	}
	return nostr.ZeroID
}

func firstTagValue(tags nostr.Tags, name string) string {
	tag := tags.Find(name)
	if tag == nil || len(tag) < 2 {
		return ""
	}
	return tag[1]
}
