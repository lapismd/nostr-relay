package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const runID = 7001

type service struct {
	mu         sync.Mutex
	ciRefs     map[string]string
	dispatched bool
	commit     string
	tag        string
}

func main() {
	head, err := os.ReadFile("/git-data/refs/heads/main")
	if err != nil {
		log.Fatal(err)
	}
	tag, err := os.ReadFile("/git-data/refs/tags/v1")
	if err != nil {
		log.Fatal(err)
	}
	api := &service{ciRefs: make(map[string]string), commit: strings.TrimSpace(string(head)), tag: strings.TrimSpace(string(tag))}
	log.Fatal(http.ListenAndServe(":8080", api))
}

func (api *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/lapis/notes.git/") {
		http.StripPrefix("/lapis/notes.git/", http.FileServer(http.Dir("/git-data"))).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		writeJSON(w, http.StatusOK, map[string]any{"ready": true})
	case r.Method == http.MethodGet && r.URL.Path == "/app/installations/99":
		writeJSON(w, http.StatusOK, map[string]any{"id": 99, "account": map[string]any{"id": 7, "login": "lapis"}})
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
		writeJSON(w, http.StatusCreated, map[string]any{
			"token": "fake-installation-token-do-not-use", "expires_at": time.Now().UTC().Add(time.Hour),
		})
	case r.Method == http.MethodGet && r.URL.Path == "/repositories/42":
		writeJSON(w, http.StatusOK, repository())
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/git/ref/heads/main":
		writeJSON(w, http.StatusOK, gitRef("refs/heads/main", api.commit))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/git/matching-refs/heads":
		writeJSON(w, http.StatusOK, []any{gitRef("refs/heads/main", api.commit)})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/git/matching-refs/tags":
		writeJSON(w, http.StatusOK, []any{gitRef("refs/tags/v1", api.tag)})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/commits/"+api.commit:
		writeJSON(w, http.StatusOK, map[string]any{"sha": api.commit})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/issues":
		writeJSON(w, http.StatusOK, []any{})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/issues":
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": 8001, "number": 12, "html_url": "https://github.com/lapis/notes/issues/12", "state": "open",
			"user": map[string]any{"id": 55, "login": "lapis-bot"},
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/lapis/notes/git/ref/heads/lapis-ci/"):
		ref := "refs/heads/" + strings.TrimPrefix(r.URL.Path, "/repos/lapis/notes/git/ref/heads/")
		api.mu.Lock()
		sha, found := api.ciRefs[ref]
		api.mu.Unlock()
		if !found {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, gitRef(ref, sha))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/git/refs":
		var request struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || !strings.HasPrefix(request.Ref, "refs/heads/lapis-ci/") || request.SHA != api.commit {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "invalid test ref"})
			return
		}
		api.mu.Lock()
		api.ciRefs[request.Ref] = request.SHA
		api.mu.Unlock()
		writeJSON(w, http.StatusCreated, gitRef(request.Ref, request.SHA))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/lapis/notes/git/refs/heads/lapis-ci/"):
		ref := "refs/heads/" + strings.TrimPrefix(r.URL.Path, "/repos/lapis/notes/git/refs/heads/")
		api.mu.Lock()
		delete(api.ciRefs, ref)
		api.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/actions/workflows/ci.yml/dispatches":
		api.mu.Lock()
		api.dispatched = true
		api.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/actions/workflows/ci.yml/runs":
		api.mu.Lock()
		dispatched := api.dispatched
		api.mu.Unlock()
		runs := []any{}
		if dispatched {
			runs = append(runs, api.workflowRun("queued"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"workflow_runs": runs})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/actions/runs/7001":
		writeJSON(w, http.StatusOK, api.workflowRun("completed"))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/actions/runs/7001/cancel":
		w.WriteHeader(http.StatusAccepted)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "fake GitHub route not found", "path": r.URL.Path})
	}
}

func repository() map[string]any {
	return map[string]any{
		"id": 42, "full_name": "lapis/notes", "name": "notes", "default_branch": "main",
		"clone_url": "https://github.com/lapis/notes.git", "html_url": "https://github.com/lapis/notes",
		"private": true, "owner": map[string]any{"id": 7, "login": "lapis"},
	}
}

func gitRef(ref, sha string) map[string]any {
	return map[string]any{"ref": ref, "object": map[string]any{"type": "commit", "sha": sha}}
}

func (api *service) workflowRun(status string) map[string]any {
	api.mu.Lock()
	branch := ""
	for ref := range api.ciRefs {
		branch = strings.TrimPrefix(ref, "refs/heads/")
		break
	}
	api.mu.Unlock()
	result := map[string]any{
		"id": runID, "head_sha": api.commit, "status": status,
		"head_branch": branch, "html_url": "https://github.com/lapis/notes/actions/runs/7001",
		"updated_at": "2026-09-09T12:00:00Z",
	}
	if status == "completed" {
		result["conclusion"] = "success"
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
