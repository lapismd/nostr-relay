package gitforge

import (
	"context"
	"net/http"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"zooid/internal/forge"
)

func LogReviewRef(branch string, author nostr.PubKey) (string, bool) {
	name := reviewBranchName(branch)
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[0] != "review" {
		return "", false
	}
	prefix, value, err := nip19.Decode(parts[1])
	if err != nil || prefix != "npub" {
		return "", false
	}
	pubkey, ok := value.(nostr.PubKey)
	if !ok || pubkey != author || author == nostr.ZeroPK {
		return "", false
	}
	changeID := strings.ToLower(parts[2])
	if len(changeID) != 32 || !isLowerHex(changeID) {
		return "", false
	}
	return "refs/heads/review/" + parts[1] + "/" + changeID, true
}

func IsLogReviewBranchName(branch string) bool {
	return strings.HasPrefix(reviewBranchName(branch), "review/")
}

func reviewBranchName(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	return branch
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return value != ""
}

func materializeReviewRef(ctx context.Context, gitPath, directory, configDir string, env []string, remote, reviewRef, head string) error {
	if !strings.HasPrefix(reviewRef, "refs/heads/review/") {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_merge_request"}
	}
	if !gitHasCommit(ctx, gitPath, directory, env, head) && !fetchReviewCommit(ctx, gitPath, directory, env, reviewRef, head) {
		if err := fetchMergeHead(ctx, gitPath, directory, env, head); err != nil {
			return err
		}
	}
	if !gitHasCommit(ctx, gitPath, directory, env, head) {
		return &forge.Error{Status: http.StatusConflict, Code: "merge_head_not_found"}
	}
	return publishReviewRef(ctx, gitPath, directory, configDir, env, remote, reviewRef, head)
}

func fetchReviewCommit(ctx context.Context, gitPath, directory string, env []string, reviewRef, head string) bool {
	if runGit(ctx, gitPath, directory, env, "fetch", "--no-tags", "origin", "+"+reviewRef+":"+reviewRef) != nil {
		return false
	}
	return gitHasCommit(ctx, gitPath, directory, env, head)
}

func publishReviewRef(ctx context.Context, gitPath, directory, configDir string, env []string, remote, reviewRef, head string) error {
	if err := runGit(ctx, gitPath, directory, env, "push", "origin", head+":"+reviewRef); err != nil {
		return err
	}
	published, err := lsRemoteRefsIncluding(ctx, gitPath, configDir, env, remote, reviewRef)
	if err != nil {
		return err
	}
	if !strings.EqualFold(published[reviewRef], head) {
		return &forge.Error{Status: http.StatusConflict, Code: "merge_ref_unconfirmed"}
	}
	return nil
}
