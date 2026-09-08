package zooid

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

// GitForgePolicy owns the durable desired-state projection and the NIP-29
// boundary for the optional Git sidecar. Signed Nostr events remain authority.
type GitForgePolicy struct {
	Events        *EventStore
	Groups        *GroupStore
	ProjectTasks  *ProjectTaskPolicy
	Config        *Config
	Backend       gitforge.Backend
	WebhookSecret string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewGitForgePolicy(ctx context.Context, config *Config, events *EventStore, groups *GroupStore, projectTasks *ProjectTaskPolicy, backend gitforge.Backend) *GitForgePolicy {
	workerCtx, cancel := context.WithCancel(ctx)
	return &GitForgePolicy{
		Events: events, Groups: groups, ProjectTasks: projectTasks, Config: config, Backend: backend,
		ctx: workerCtx, cancel: cancel,
	}
}

// CheckWrite applies configuration-dependent hosted-repository constraints.
// External repositories and task anchors retain the wider NIP-34 identifier
// contract enforced by internal/projecttask.
func (policy *GitForgePolicy) CheckWrite(event nostr.Event) (bool, string) {
	if event.Kind != nostr.KindRepositoryAnnouncement {
		return false, ""
	}
	_, err := gitforge.ClassifyRepository(event, policy.Config.Git.PublicURL)
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	return true, ""
}

// Remember records desired state after the signed event is safely stored. It
// never waits for WalGit, so a sidecar outage cannot reject Nostr publication.
func (policy *GitForgePolicy) Remember(ctx context.Context, event nostr.Event) error {
	if event.Kind == nostr.KindRepositoryAnnouncement {
		repository, err := gitforge.ClassifyRepository(event, policy.Config.Git.PublicURL)
		if err != nil {
			return err
		}
		return policy.upsertIntent(ctx, repository)
	}
	if event.Kind == nostr.KindDeletion {
		for _, tag := range event.Tags {
			coordinate, ok := deletionRepositoryCoordinate(tag)
			if !ok || coordinate.Owner != event.PubKey {
				continue
			}
			if err := policy.markInactive(ctx, coordinate, "deleted"); err != nil {
				return err
			}
		}
	}
	return nil
}

func deletionRepositoryCoordinate(tag nostr.Tag) (projecttask.RepositoryCoordinate, bool) {
	if len(tag) < 2 || tag[0] != "a" {
		return projecttask.RepositoryCoordinate{}, false
	}
	coordinate, err := projecttask.ParseRepositoryCoordinate(tag[1])
	return coordinate, err == nil
}

func (policy *GitForgePolicy) upsertIntent(ctx context.Context, repository gitforge.Repository) error {
	state := "inactive"
	if repository.Class == gitforge.RepositoryHosted {
		state = "pending"
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_repository_intents
		  (owner, repo, announcement_id, channel, desired_class, generation, reconcile_state, attempt_count, reconcile_after, last_error, updated_at)
		VALUES ($1, $2, $3, $4, $5, 1, $6, 0, NOW(), '', NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  announcement_id = EXCLUDED.announcement_id,
		  channel = EXCLUDED.channel,
		  desired_class = EXCLUDED.desired_class,
		  generation = {{.Name}}__git_repository_intents.generation + 1,
		  reconcile_state = EXCLUDED.reconcile_state,
		  attempt_count = 0,
		  reconcile_after = NOW(),
		  last_error = '',
		  updated_at = NOW()`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, repository.Announcement.ID.Hex(),
		repository.Channel, string(repository.Class), state)
	return err
}

func (policy *GitForgePolicy) markInactive(ctx context.Context, coordinate projecttask.RepositoryCoordinate, class string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_repository_intents
		  (owner, repo, desired_class, reconcile_state)
		VALUES ($1, $2, $3, 'inactive')
		ON CONFLICT (owner, repo) DO UPDATE SET
		  desired_class = EXCLUDED.desired_class,
		  generation = {{.Name}}__git_repository_intents.generation + 1,
		  reconcile_state = 'inactive',
		  attempt_count = 0,
		  last_error = '',
		  updated_at = NOW()`), coordinate.Owner.Hex(), coordinate.ID, class)
	return err
}

// Start repairs any post-save crash gap, then runs one bounded reconciler.
func (policy *GitForgePolicy) Start() error {
	if err := policy.rebuildIntents(policy.ctx); err != nil {
		return err
	}
	policy.wg.Add(1)
	go func() {
		defer policy.wg.Done()
		ticker := time.NewTicker(policy.Config.GitReconcileInterval())
		defer ticker.Stop()
		for {
			if err := policy.ReconcileOnce(policy.ctx); err != nil && policy.ctx.Err() == nil {
				log.Printf("Git repository reconciliation failed: %v", err)
			}
			select {
			case <-policy.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (policy *GitForgePolicy) Close() {
	policy.cancel()
	policy.wg.Wait()
}

func (policy *GitForgePolicy) rebuildIntents(ctx context.Context) error {
	active := make(map[string]struct{})
	for event := range policy.Events.QueryEventsBefore(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.KindRepositoryAnnouncement}}, "", 0) {
		repository, err := gitforge.ClassifyRepository(event, policy.Config.Git.PublicURL)
		if err != nil {
			continue
		}
		key := repository.Coordinate.Owner.Hex() + "\x00" + repository.Coordinate.ID
		active[key] = struct{}{}
		if err := policy.upsertIntent(ctx, repository); err != nil {
			return err
		}
	}

	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	rows, err := GetDb().QueryContext(queryCtx, policy.Events.Schema.Render(`SELECT owner, repo FROM {{.Name}}__git_repository_intents`))
	if err != nil {
		cancel()
		return err
	}
	type coordinatePair struct{ owner, repo string }
	missing := make([]coordinatePair, 0)
	for rows.Next() {
		var pair coordinatePair
		if err := rows.Scan(&pair.owner, &pair.repo); err != nil {
			rows.Close()
			cancel()
			return err
		}
		if _, found := active[pair.owner+"\x00"+pair.repo]; !found {
			missing = append(missing, pair)
		}
	}
	err = rows.Err()
	rows.Close()
	cancel()
	if err != nil {
		return err
	}
	for _, pair := range missing {
		owner, err := nostr.PubKeyFromHex(pair.owner)
		if err != nil {
			continue
		}
		if err := policy.markInactive(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: pair.repo}, "deleted"); err != nil {
			return err
		}
	}
	return nil
}

type repositoryIntent struct {
	Coordinate projecttask.RepositoryCoordinate
	Generation int64
	Attempts   int
}

// ReconcileOnce claims and processes at most one intent. A generation guard
// prevents an old network response from making a replaced announcement ready.
func (policy *GitForgePolicy) ReconcileOnce(ctx context.Context) error {
	intent, found, err := policy.claimIntent(ctx)
	if err != nil || !found {
		return err
	}
	err = policy.Backend.EnsureRepository(ctx, intent.Coordinate)
	return policy.finishIntent(ctx, intent, err)
}

func (policy *GitForgePolicy) claimIntent(ctx context.Context) (repositoryIntent, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return repositoryIntent{}, false, err
	}
	defer tx.Rollback()
	var ownerHex, repo string
	var generation int64
	var attempts int
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT owner, repo, generation, attempt_count
		FROM {{.Name}}__git_repository_intents
		WHERE desired_class = 'hosted'
		  AND reconcile_state IN ('pending', 'retry')
		  AND reconcile_after <= NOW()
		ORDER BY reconcile_after, owner, repo
		FOR UPDATE SKIP LOCKED
		LIMIT 1`)).Scan(&ownerHex, &repo, &generation, &attempts)
	if err == sql.ErrNoRows {
		return repositoryIntent{}, false, nil
	}
	if err != nil {
		return repositoryIntent{}, false, err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_repository_intents
		SET reconcile_state = 'running', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND generation = $3`), ownerHex, repo, generation); err != nil {
		return repositoryIntent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return repositoryIntent{}, false, err
	}
	owner, err := nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return repositoryIntent{}, false, err
	}
	return repositoryIntent{Coordinate: projecttask.RepositoryCoordinate{Owner: owner, ID: repo}, Generation: generation, Attempts: attempts}, true, nil
}

func (policy *GitForgePolicy) finishIntent(ctx context.Context, intent repositoryIntent, reconcileErr error) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	if reconcileErr == nil {
		_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			UPDATE {{.Name}}__git_repository_intents
			SET reconcile_state = 'ready', attempt_count = 0, last_error = '', updated_at = NOW()
			WHERE owner = $1 AND repo = $2 AND generation = $3 AND desired_class = 'hosted'`),
			intent.Coordinate.Owner.Hex(), intent.Coordinate.ID, intent.Generation)
		return err
	}
	attempts := intent.Attempts + 1
	delaySeconds := math.Min(math.Pow(2, float64(min(attempts, 8))), 300)
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_repository_intents
		SET reconcile_state = 'retry', attempt_count = $4,
		    reconcile_after = NOW() + ($5 * INTERVAL '1 second'),
		    last_error = $6, updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND generation = $3 AND desired_class = 'hosted'`),
		intent.Coordinate.Owner.Hex(), intent.Coordinate.ID, intent.Generation, attempts, int(delaySeconds), sanitizeGitError(reconcileErr))
	return err
}

func sanitizeGitError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func (policy *GitForgePolicy) intentState(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (string, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var state string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT reconcile_state FROM {{.Name}}__git_repository_intents WHERE owner = $1 AND repo = $2`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&state)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return state, err == nil, err
}

func (policy *GitForgePolicy) resolveRepository(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (gitforge.Repository, bool, error) {
	event, found, err := policy.ProjectTasks.EventByAddress(ctx, nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID)
	if err != nil || !found || policy.ProjectTasks.IsTombstoned(ctx, event) {
		return gitforge.Repository{}, false, err
	}
	repository, err := gitforge.ClassifyRepository(event, policy.Config.Git.PublicURL)
	if err != nil {
		return gitforge.Repository{}, false, err
	}
	return repository, true, nil
}

func (policy *GitForgePolicy) String() string {
	return fmt.Sprintf("GitForgePolicy(%s)", policy.Config.Git.BackendURL)
}
