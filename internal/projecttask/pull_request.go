package projecttask

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
)

type PullRequestLifecycle string

const (
	PullRequestOpen   PullRequestLifecycle = "Open"
	PullRequestMerged PullRequestLifecycle = "Merged"
	PullRequestClosed PullRequestLifecycle = "Closed"
	PullRequestDraft  PullRequestLifecycle = "Draft"
)

type ReviewDecisionKind string

const (
	ReviewApproved         ReviewDecisionKind = "approved"
	ReviewChangesRequested ReviewDecisionKind = "changes-requested"
)

type ReviewDecisionStatus string

const (
	ReviewDecisionCurrent    ReviewDecisionStatus = "current"
	ReviewDecisionHistorical ReviewDecisionStatus = "historical"
)

type InlineCommentStatus string

const (
	InlineCommentCurrent  InlineCommentStatus = "current"
	InlineCommentOutdated InlineCommentStatus = "outdated"
)

type PullRequestUpdate struct {
	Event      nostr.Event
	Commit     string
	CloneURLs  []string
	BaseCommit string
	MergeBase  string
}

type InlineAnchor struct {
	Path string
	Side string
	Line uint64
}

type PullRequestComment struct {
	Event                   nostr.Event
	Encoding                CommentEncoding
	Commit                  string
	Anchor                  *InlineAnchor
	IsInlineComment         bool
	InlineCommentStatus     InlineCommentStatus
	IsReviewRequest         bool
	IsTrustedReviewRequest  bool
	ReviewerPubkeys         []nostr.PubKey
	ReviewDecision          ReviewDecisionKind
	IsTrustedReviewDecision bool
	ReviewDecisionStatus    ReviewDecisionStatus
}

type PullRequestDecision struct {
	Event    nostr.Event
	Reviewer nostr.PubKey
	Commit   string
	Decision ReviewDecisionKind
}

// PullRequestState is a deterministic read model over ordinary Nostr events.
// It is intentionally reproducible and never persisted as authority.
type PullRequestState struct {
	Root            nostr.Event
	Repository      RepositoryCoordinate
	Title           string
	Content         string
	Labels          []string
	Recipients      []nostr.PubKey
	InitialCommit   string
	CurrentCommit   string
	CloneURLs       []string
	BranchName      string
	TargetBranch    string
	BaseCommit      string
	MergeBase       string
	Lifecycle       PullRequestLifecycle
	StatusEvent     *nostr.Event
	MergeCommit     string
	MergeReferences []string
	Reviewers       []nostr.PubKey
	Approvals       []PullRequestDecision
	ChangeRequests  []PullRequestDecision
	Updates         []PullRequestUpdate
	Comments        []PullRequestComment
	CreatedAt       nostr.Timestamp
	UpdatedAt       nostr.Timestamp
}

// ReducePullRequest matches the Buzz Desktop v0.5.23 reducer while retaining
// both Buzz kind-1 notes and standards-based NIP-22 comments as their original
// encodings.
func ReducePullRequest(root, repository nostr.Event, related []nostr.Event, authority ...CollaborationAuthority) (PullRequestState, error) {
	if root.Kind != KindPullRequest {
		return PullRequestState{}, fmt.Errorf("pull request root must be kind 1618")
	}
	if repository.Kind != nostr.KindRepositoryAnnouncement {
		return PullRequestState{}, fmt.Errorf("pull request repository must be kind 30617")
	}
	coordinate, err := validateRequiredRepositoryTag(root.Tags, "pull request")
	if err != nil {
		return PullRequestState{}, err
	}
	if repository.PubKey != coordinate.Owner || repository.Tags.GetD() != coordinate.ID {
		return PullRequestState{}, fmt.Errorf("pull request repository does not match its a tag")
	}
	if err := validatePullRequest(root); err != nil {
		return PullRequestState{}, err
	}

	state := PullRequestState{
		Root:          root,
		Repository:    coordinate,
		Title:         firstTagValue(root.Tags, "subject"),
		Content:       root.Content,
		Labels:        allTagValues(root.Tags, "t"),
		Recipients:    validPubkeyTagValues(root.Tags, "p"),
		InitialCommit: firstTagValue(root.Tags, "c"),
		CurrentCommit: firstTagValue(root.Tags, "c"),
		CloneURLs:     cloneLocators(root.Tags),
		BranchName:    firstTagValue(root.Tags, "branch-name"),
		TargetBranch:  firstTagValue(root.Tags, "target-branch"),
		BaseCommit:    firstTagValue(root.Tags, "base-commit"),
		MergeBase:     firstTagValue(root.Tags, "merge-base"),
		Lifecycle:     defaultPullRequestLifecycle(root),
		CreatedAt:     root.CreatedAt,
		UpdatedAt:     root.CreatedAt,
	}

	auth := CollaborationAuthority{}
	if len(authority) > 0 {
		auth = authority[0]
	}
	trustedRootActors := map[nostr.PubKey]struct{}{root.PubKey: {}, repository.PubKey: {}}
	events := append([]nostr.Event(nil), related...)
	slices.SortFunc(events, chronological)
	var latestUpdate, latestStatus *nostr.Event
	comments := make([]PullRequestComment, 0)
	for i := range events {
		event := events[i]
		switch event.Kind {
		case KindPullRequestUpdate:
			if !validPullRequestUpdateFor(event, root, coordinate) {
				continue
			}
			if _, trusted := trustedRootActors[event.PubKey]; !trusted {
				continue
			}
			state.Updates = append(state.Updates, PullRequestUpdate{
				Event: event, Commit: firstTagValue(event.Tags, "c"),
				CloneURLs: cloneLocators(event.Tags), BaseCommit: firstTagValue(event.Tags, "base-commit"),
				MergeBase: firstTagValue(event.Tags, "merge-base"),
			})
			if latestUpdate == nil || eventNewerSmallestID(event, *latestUpdate) {
				copy := event
				latestUpdate = &copy
			}
		case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
			if !referencesPullRequest(event, root.ID) || !repositoryTagMatchesIfPresent(event, coordinate) {
				continue
			}
			if !trustedPullRequestStatus(event, root, auth) {
				continue
			}
			if latestStatus == nil || eventNewerSmallestID(event, *latestStatus) {
				copy := event
				latestStatus = &copy
			}
		case nostr.KindTextNote:
			if referencesPullRequest(event, root.ID) && repositoryTagMatchesIfPresent(event, coordinate) {
				comments = append(comments, parsePullRequestComment(event, CommentBuzzKind1))
			}
		case nostr.KindComment:
			if isPullRequestNIP22Comment(event, root.ID) {
				comments = append(comments, parsePullRequestComment(event, CommentNIP22))
			}
		}
	}

	if latestUpdate != nil {
		state.CurrentCommit = firstTagValue(latestUpdate.Tags, "c")
		state.CloneURLs = cloneLocators(latestUpdate.Tags)
		if baseCommit := firstTagValue(latestUpdate.Tags, "base-commit"); baseCommit != "" {
			state.BaseCommit = baseCommit
		}
		if mergeBase := firstTagValue(latestUpdate.Tags, "merge-base"); mergeBase != "" {
			state.MergeBase = mergeBase
		}
	}
	if latestStatus != nil {
		state.StatusEvent = latestStatus
		state.Lifecycle = lifecycleForKind(latestStatus.Kind)
		state.MergeCommit = firstTagValue(latestStatus.Tags, "merge-commit")
		state.MergeReferences = allTagValues(latestStatus.Tags, "r")
	}

	state.Reviewers = pullRequestReviewers(root, comments, trustedRootActors)

	latestDecisionByReviewer := make(map[nostr.PubKey]PullRequestDecision)
	for i := range comments {
		comment := &comments[i]
		if comment.IsReviewRequest {
			_, comment.IsTrustedReviewRequest = trustedRootActors[comment.Event.PubKey]
		}
		if comment.ReviewDecision != "" {
			comment.IsTrustedReviewDecision = comment.Event.PubKey != root.PubKey && auth.Allows(comment.Event.PubKey)
			if comment.IsTrustedReviewDecision {
				commit := comment.Commit
				if commit == "" {
					commit = state.InitialCommit
				}
				if commit == state.CurrentCommit && state.CurrentCommit != "" {
					comment.ReviewDecisionStatus = ReviewDecisionCurrent
					candidate := PullRequestDecision{
						Event: comment.Event, Reviewer: comment.Event.PubKey,
						Commit: commit, Decision: comment.ReviewDecision,
					}
					current, found := latestDecisionByReviewer[comment.Event.PubKey]
					if !found || decisionNewerLargestID(candidate.Event, current.Event) {
						latestDecisionByReviewer[comment.Event.PubKey] = candidate
					}
				} else {
					comment.ReviewDecisionStatus = ReviewDecisionHistorical
				}
			}
		}
		if comment.Anchor != nil {
			commit := comment.Commit
			if commit == "" {
				commit = state.InitialCommit
			}
			if commit == state.CurrentCommit && state.CurrentCommit != "" {
				comment.InlineCommentStatus = InlineCommentCurrent
			} else {
				comment.InlineCommentStatus = InlineCommentOutdated
			}
		}
		if comment.Event.CreatedAt > state.UpdatedAt {
			state.UpdatedAt = comment.Event.CreatedAt
		}
	}
	state.Comments = comments

	for _, decision := range latestDecisionByReviewer {
		switch decision.Decision {
		case ReviewApproved:
			state.Approvals = append(state.Approvals, decision)
		case ReviewChangesRequested:
			state.ChangeRequests = append(state.ChangeRequests, decision)
		}
	}
	slices.SortFunc(state.Approvals, comparePullRequestDecisions)
	slices.SortFunc(state.ChangeRequests, comparePullRequestDecisions)
	for _, update := range state.Updates {
		if update.Event.CreatedAt > state.UpdatedAt {
			state.UpdatedAt = update.Event.CreatedAt
		}
	}
	if latestStatus != nil && latestStatus.CreatedAt > state.UpdatedAt {
		state.UpdatedAt = latestStatus.CreatedAt
	}
	return state, nil
}

func validPullRequestUpdateFor(event, root nostr.Event, coordinate RepositoryCoordinate) bool {
	if err := validatePullRequestUpdate(event); err != nil {
		return false
	}
	return firstTagValue(event.Tags, "E") == root.ID.Hex() &&
		firstTagValue(event.Tags, "P") == root.PubKey.Hex() &&
		firstTagValue(event.Tags, "a") == coordinate.String()
}

func parsePullRequestComment(event nostr.Event, encoding CommentEncoding) PullRequestComment {
	labels := allTagValuesFolded(event.Tags, "t")
	isRequest := slices.Contains(labels, "review-request")
	isApproval := slices.Contains(labels, "approval")
	isChangeRequest := slices.Contains(labels, "changes-requested")
	comment := PullRequestComment{
		Event: event, Encoding: encoding, Commit: firstTagValue(event.Tags, "c"),
		IsReviewRequest: isRequest,
	}
	if isApproval != isChangeRequest {
		if isApproval {
			comment.ReviewDecision = ReviewApproved
		} else {
			comment.ReviewDecision = ReviewChangesRequested
		}
	}
	if isRequest {
		comment.ReviewerPubkeys = validPubkeyTagValues(event.Tags, "p")
	}
	if !isRequest && !isApproval {
		comment.Anchor = parseInlineAnchor(event.Tags)
	}
	comment.IsInlineComment = comment.Anchor != nil || slices.Contains(labels, "inline-comment")
	return comment
}

func parseInlineAnchor(tags nostr.Tags) *InlineAnchor {
	path := firstTagValue(tags, "file")
	side := firstTagValue(tags, "side")
	lineValue := firstTagValue(tags, "line")
	if path == "" || len(path) > 4096 || strings.HasPrefix(path, "/") ||
		strings.ContainsAny(path, "\\\x00") || (side != "old" && side != "new") {
		return nil
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil
		}
	}
	if lineValue == "" || strings.TrimLeft(lineValue, "0123456789") != "" || lineValue[0] == '0' {
		return nil
	}
	line, err := strconv.ParseUint(lineValue, 10, 53)
	if err != nil || line == 0 {
		return nil
	}
	return &InlineAnchor{Path: path, Side: side, Line: line}
}

func pullRequestReviewers(root nostr.Event, comments []PullRequestComment, trusted map[nostr.PubKey]struct{}) []nostr.PubKey {
	reviewers := make(map[nostr.PubKey]struct{})
	for _, reviewer := range validPubkeyTagValues(root.Tags, "p") {
		reviewers[reviewer] = struct{}{}
	}
	for _, comment := range comments {
		if !comment.IsReviewRequest {
			continue
		}
		if _, allowed := trusted[comment.Event.PubKey]; !allowed {
			continue
		}
		for _, reviewer := range comment.ReviewerPubkeys {
			reviewers[reviewer] = struct{}{}
		}
	}
	delete(reviewers, root.PubKey)
	result := make([]nostr.PubKey, 0, len(reviewers))
	for reviewer := range reviewers {
		result = append(result, reviewer)
	}
	slices.SortFunc(result, func(left, right nostr.PubKey) int {
		return cmp.Compare(left.Hex(), right.Hex())
	})
	return result
}

func referencesPullRequest(event nostr.Event, root nostr.ID) bool {
	for _, name := range []string{"e", "E"} {
		if tagHasValue(event.Tags, name, root.Hex()) {
			return true
		}
	}
	return false
}

func isPullRequestNIP22Comment(event nostr.Event, root nostr.ID) bool {
	return event.Kind == nostr.KindComment &&
		tagHasValue(event.Tags, "K", kindNumber(KindPullRequest)) &&
		tagHasValue(event.Tags, "E", root.Hex())
}

func repositoryTagMatchesIfPresent(event nostr.Event, coordinate RepositoryCoordinate) bool {
	tags := tagsNamed(event.Tags, "a")
	return len(tags) == 0 || (len(tags) == 1 && len(tags[0]) >= 2 && tags[0][1] == coordinate.String())
}

func eventNewerSmallestID(candidate, current nostr.Event) bool {
	return candidate.CreatedAt > current.CreatedAt ||
		(candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() < current.ID.Hex())
}

func decisionNewerLargestID(candidate, current nostr.Event) bool {
	return candidate.CreatedAt > current.CreatedAt ||
		(candidate.CreatedAt == current.CreatedAt && candidate.ID.Hex() > current.ID.Hex())
}

func comparePullRequestDecisions(left, right PullRequestDecision) int {
	if order := cmp.Compare(left.Event.CreatedAt, right.Event.CreatedAt); order != 0 {
		return order
	}
	return cmp.Compare(left.Event.ID.Hex(), right.Event.ID.Hex())
}

func defaultPullRequestLifecycle(root nostr.Event) PullRequestLifecycle {
	for _, label := range allTagValues(root.Tags, "t") {
		if strings.EqualFold(label, "draft") {
			return PullRequestDraft
		}
	}
	return PullRequestOpen
}

func trustedPullRequestStatus(event, root nostr.Event, authority CollaborationAuthority) bool {
	if event.Kind == nostr.KindStatusApplied {
		return authority.Allows(event.PubKey) || providerAuthoredStatus(event, root)
	}
	return event.PubKey == root.PubKey || authority.Allows(event.PubKey)
}

func providerAuthoredStatus(event, root nostr.Event) bool {
	return event.PubKey == root.PubKey && firstTagValue(root.Tags, "external-provider") != ""
}

func lifecycleForKind(kind nostr.Kind) PullRequestLifecycle {
	switch kind {
	case nostr.KindStatusApplied:
		return PullRequestMerged
	case nostr.KindStatusClosed:
		return PullRequestClosed
	case nostr.KindStatusDraft:
		return PullRequestDraft
	default:
		return PullRequestOpen
	}
}

func firstTagValue(tags nostr.Tags, name string) string {
	tag := tags.Find(name)
	if tag == nil || len(tag) < 2 {
		return ""
	}
	return tag[1]
}

func allTagValues(tags nostr.Tags, name string) []string {
	values := make([]string, 0)
	for _, tag := range tagsNamed(tags, name) {
		values = append(values, tag[1:]...)
	}
	return values
}

func allTagValuesFolded(tags nostr.Tags, name string) []string {
	values := allTagValues(tags, name)
	for i := range values {
		values[i] = strings.ToLower(values[i])
	}
	return values
}

func validPubkeyTagValues(tags nostr.Tags, name string) []nostr.PubKey {
	values := make([]nostr.PubKey, 0)
	for _, tag := range tagsNamed(tags, name) {
		for _, value := range tag[1:] {
			if pubkey, err := nostr.PubKeyFromHex(value); err == nil {
				values = append(values, pubkey)
			}
		}
	}
	return values
}
