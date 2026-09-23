package gitforge

import (
	"context"
	"net/http"
	"os/exec"
	"strings"

	"zooid/internal/forge"
)

func fetchMergeHead(ctx context.Context, gitPath, directory string, env []string, head string) error {
	if gitHasCommit(ctx, gitPath, directory, env, head) {
		return nil
	}
	_ = runGit(ctx, gitPath, directory, env, "fetch", "--no-tags", "origin", "+refs/jj-sync/*:refs/jj-sync/*")
	if gitHasCommit(ctx, gitPath, directory, env, head) {
		return nil
	}
	_ = runGit(ctx, gitPath, directory, env, "fetch", "--no-tags", "origin", "+refs/heads/jj-sync/*:refs/remotes/origin/jj-sync/*")
	if gitHasCommit(ctx, gitPath, directory, env, head) {
		return nil
	}
	_ = runGit(ctx, gitPath, directory, env, "fetch", "--no-tags", "origin", head)
	if gitHasCommit(ctx, gitPath, directory, env, head) {
		return nil
	}
	return &forge.Error{Status: http.StatusConflict, Code: "merge_head_not_found"}
}

func applyProtectedMerge(ctx context.Context, gitPath, directory string, env []string, method, message, head string) error {
	switch method {
	case "merge":
		output, err := runGitCombined(ctx, gitPath, directory, env, "merge", "--no-ff", "-m", message, head)
		if err == nil {
			return nil
		}
		if mergeFailureCode(output) == "unrelated_histories" {
			output, err = runGitCombined(ctx, gitPath, directory, env, "merge", "--allow-unrelated-histories", "--no-ff", "-m", message, head)
			if err == nil {
				return nil
			}
		}
		return &forge.Error{Status: http.StatusConflict, Code: mergeFailureCode(output)}
	case "squash":
		if output, err := runGitCombined(ctx, gitPath, directory, env, "merge", "--squash", head); err != nil {
			return &forge.Error{Status: http.StatusConflict, Code: mergeFailureCode(output)}
		}
		if output, err := runGitCombined(ctx, gitPath, directory, env, "commit", "-m", message); err != nil {
			return &forge.Error{Status: http.StatusConflict, Code: mergeFailureCode(output)}
		}
		return nil
	case "rebase":
		output, err := runGitCombined(ctx, gitPath, directory, env, "rebase", head)
		if err != nil {
			return &forge.Error{Status: http.StatusConflict, Code: mergeFailureCode(output)}
		}
		return nil
	default:
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_merge_request"}
	}
}

func gitHasCommit(ctx context.Context, gitPath, directory string, env []string, head string) bool {
	return runGit(ctx, gitPath, directory, env, "cat-file", "-e", head+"^{commit}") == nil
}

func mergeFailureCode(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "not something we can merge"),
		strings.Contains(lower, "bad object"),
		strings.Contains(lower, "unknown revision"),
		strings.Contains(lower, "needed a single revision"):
		return "merge_head_not_found"
	case strings.Contains(lower, "refusing to merge unrelated histories"):
		return "unrelated_histories"
	case strings.Contains(lower, "conflict"),
		strings.Contains(lower, "automatic merge failed"):
		return "merge_conflict"
	default:
		return "walgit_merge_rejected"
	}
}

func runGitCombined(ctx context.Context, gitPath, directory string, env []string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, gitPath, arguments...)
	command.Dir, command.Env = directory, env
	var output limitedBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.buffer.String(), err
}
