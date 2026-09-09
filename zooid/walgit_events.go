package zooid

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

const maxWalGitWebhookBody = 4 << 20

type walGitRefEvent struct {
	Action        string `json:"action"`
	RefType       string `json:"ref_type"`
	RefName       string `json:"ref_name"`
	Old           string `json:"old"`
	New           string `json:"new"`
	Pusher        string `json:"pusher"`
	CorrelationID string `json:"correlation_id"`
	Repository    string `json:"repo"`
	WalGit        struct {
		SchemaVersion uint32 `json:"schema_version"`
		Sequence      string `json:"seq"`
		EntryKind     string `json:"entry_kind"`
		RequestID     string `json:"request_id"`
	} `json:"_walgit"`
}

type normalizedRefEvent struct {
	Coordinate    projecttask.RepositoryCoordinate
	Action        string
	RefName       string
	Old           string
	New           string
	Pusher        string
	CorrelationID string
	Sequence      string
}

// ServeWalGitEvents consumes WalGit's at-least-once webhook. The durable
// identity is (repository, WAL sequence, ref), and the current-ref table is a
// monotonic projection rather than collaboration authority.
func (policy *GitForgePolicy) ServeWalGitEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedForGit(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWalGitWebhookBody+1))
	if err != nil || len(body) > maxWalGitWebhookBody {
		writeGitError(w, http.StatusRequestEntityTooLarge, "webhook_too_large")
		return
	}
	if !validWalGitSignature(policy.WebhookSecret, r.Header.Get("X-Walgit-Signature"), body) {
		writeGitError(w, http.StatusUnauthorized, "invalid_webhook_signature")
		return
	}
	var incoming []walGitRefEvent
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&incoming); err != nil || len(incoming) > 10000 {
		writeGitError(w, http.StatusBadRequest, "invalid_webhook")
		return
	}
	normalized := make([]normalizedRefEvent, 0, len(incoming))
	for _, event := range incoming {
		item, err := normalizeWalGitEvent(event)
		if err != nil {
			writeGitError(w, http.StatusBadRequest, "invalid_webhook_event")
			return
		}
		normalized = append(normalized, item)
	}
	if err := policy.storeWalGitEvents(r.Context(), normalized); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "webhook_store_failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validWalGitSignature(secret, header string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func normalizeWalGitEvent(event walGitRefEvent) (normalizedRefEvent, error) {
	parts := strings.Split(event.Repository, "/")
	if len(parts) != 2 || len(parts[0]) != 64 || parts[0] != strings.ToLower(parts[0]) {
		return normalizedRefEvent{}, fmt.Errorf("invalid repository")
	}
	owner, err := nostr.PubKeyFromHex(parts[0])
	if err != nil || gitforge.ValidateHostedRepositoryID(parts[1]) != nil {
		return normalizedRefEvent{}, fmt.Errorf("invalid repository")
	}
	if event.WalGit.SchemaVersion != 1 || (event.WalGit.EntryKind != "push" && event.WalGit.EntryKind != "ref_update") {
		return normalizedRefEvent{}, fmt.Errorf("invalid extension")
	}
	if _, err := strconv.ParseUint(event.WalGit.Sequence, 10, 64); err != nil || event.WalGit.Sequence == "" || (len(event.WalGit.Sequence) > 1 && event.WalGit.Sequence[0] == '0') {
		return normalizedRefEvent{}, fmt.Errorf("invalid sequence")
	}
	if len(event.RefName) == 0 || len(event.RefName) > 1024 || !strings.HasPrefix(event.RefName, "refs/") || unsafePath(event.RefName) {
		return normalizedRefEvent{}, fmt.Errorf("invalid ref")
	}
	expectedType := ""
	if strings.HasPrefix(event.RefName, "refs/heads/") {
		expectedType = "branch"
	} else if strings.HasPrefix(event.RefName, "refs/tags/") {
		expectedType = "tag"
	}
	if event.RefType != expectedType {
		return normalizedRefEvent{}, fmt.Errorf("invalid ref type")
	}
	if !fullOID(event.Old) || !fullOID(event.New) || len(event.Old) != len(event.New) || event.Old == event.New {
		return normalizedRefEvent{}, fmt.Errorf("invalid object IDs")
	}
	oldZero, newZero := allZeroOID(event.Old), allZeroOID(event.New)
	switch event.Action {
	case "create":
		if !oldZero || newZero {
			return normalizedRefEvent{}, fmt.Errorf("invalid create")
		}
	case "update":
		if oldZero || newZero {
			return normalizedRefEvent{}, fmt.Errorf("invalid update")
		}
	case "delete":
		if oldZero || !newZero {
			return normalizedRefEvent{}, fmt.Errorf("invalid delete")
		}
	default:
		return normalizedRefEvent{}, fmt.Errorf("invalid action")
	}
	if !boundedWebhookString(event.Pusher, 1024) || !boundedWebhookString(event.CorrelationID, 1024) || !boundedWebhookString(event.WalGit.RequestID, 1024) {
		return normalizedRefEvent{}, fmt.Errorf("invalid metadata")
	}
	return normalizedRefEvent{
		Coordinate: projecttask.RepositoryCoordinate{Owner: owner, ID: parts[1]},
		Action:     event.Action, RefName: event.RefName, Old: strings.ToLower(event.Old), New: strings.ToLower(event.New),
		Pusher: event.Pusher, CorrelationID: event.CorrelationID, Sequence: event.WalGit.Sequence,
	}, nil
}

func boundedWebhookString(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func fullOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func allZeroOID(value string) bool {
	return strings.Trim(value, "0") == ""
}

func (policy *GitForgePolicy) storeWalGitEvents(ctx context.Context, events []normalizedRefEvent) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type replicationRequest struct {
		coordinate projecttask.RepositoryCoordinate
		generation int64
		sequence   string
	}
	replications := make(map[string]replicationRequest)
	for _, event := range events {
		var desiredClass string
		var replicaConnection string
		var replicationTarget string
		var integrationGeneration int64
		err := tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
			SELECT i.desired_class, COALESCE(g.connection_id, ''),
			       COALESCE(r.git_replication_target_backend, ''), COALESCE(r.generation, 0)
			FROM {{.Name}}__git_repository_intents i
			LEFT JOIN {{.Name}}__git_replicas g ON g.owner = i.owner AND g.repo = i.repo
			LEFT JOIN {{.Name}}__repository_integrations r ON r.owner = i.owner AND r.repo = i.repo
			WHERE i.owner = $1 AND i.repo = $2`),
			event.Coordinate.Owner.Hex(), event.Coordinate.ID).Scan(&desiredClass, &replicaConnection, &replicationTarget, &integrationGeneration)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		replicaGenerated := replicaConnection != "" && event.Pusher == replicaPrincipal(replicaConnection)
		if desiredClass != string(gitforge.RepositoryHosted) && !replicaGenerated {
			continue
		}
		result, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
			INSERT INTO {{.Name}}__walgit_ref_events
			  (owner, repo, sequence, ref_name, action, old_oid, new_oid, pusher, correlation_id, replica_generated)
			VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (owner, repo, sequence, ref_name) DO NOTHING`),
			event.Coordinate.Owner.Hex(), event.Coordinate.ID, event.Sequence, event.RefName,
			event.Action, event.Old, event.New, event.Pusher, event.CorrelationID, replicaGenerated)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil || inserted == 0 {
			if err != nil {
				return err
			}
			continue
		}
		if replicaGenerated {
			if event.Action == "delete" {
				_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
					UPDATE {{.Name}}__git_replica_refs SET replica_oid = '', verified_at = NULL
					WHERE owner = $1 AND repo = $2 AND ref_name = $3`),
					event.Coordinate.Owner.Hex(), event.Coordinate.ID, event.RefName)
			} else {
				_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
					UPDATE {{.Name}}__git_replica_refs SET replica_oid = $4,
					    verified_at = CASE WHEN authority_oid = $4 THEN NOW() ELSE NULL END
					WHERE owner = $1 AND repo = $2 AND ref_name = $3`),
					event.Coordinate.Owner.Hex(), event.Coordinate.ID, event.RefName, event.New)
			}
		} else if event.Action == "delete" {
			_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
				DELETE FROM {{.Name}}__walgit_refs
				WHERE owner = $1 AND repo = $2 AND ref_name = $3 AND sequence < $4::numeric`),
				event.Coordinate.Owner.Hex(), event.Coordinate.ID, event.RefName, event.Sequence)
		} else {
			_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
				INSERT INTO {{.Name}}__walgit_refs (owner, repo, ref_name, oid, sequence)
				VALUES ($1, $2, $3, $4, $5::numeric)
				ON CONFLICT (owner, repo, ref_name) DO UPDATE SET
				  oid = EXCLUDED.oid, sequence = EXCLUDED.sequence, updated_at = NOW()
				WHERE {{.Name}}__walgit_refs.sequence < EXCLUDED.sequence`),
				event.Coordinate.Owner.Hex(), event.Coordinate.ID, event.RefName, event.New, event.Sequence)
		}
		if err != nil {
			return err
		}
		if !replicaGenerated && replicationTarget != "" {
			key := event.Coordinate.String() + ":" + event.Sequence
			replications[key] = replicationRequest{coordinate: event.Coordinate, generation: integrationGeneration, sequence: event.Sequence}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, request := range replications {
		if err := policy.enqueueGitReplication(ctx, request.coordinate, request.generation, request.sequence, "walgit-ref"); err != nil {
			return err
		}
	}
	return nil
}

func (policy *GitForgePolicy) currentRef(ctx context.Context, coordinate projecttask.RepositoryCoordinate, ref string) (string, string, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var oid, sequence string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT oid, sequence::text FROM {{.Name}}__walgit_refs WHERE owner = $1 AND repo = $2 AND ref_name = $3`),
		coordinate.Owner.Hex(), coordinate.ID, ref).Scan(&oid, &sequence)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	return oid, sequence, err == nil, err
}
