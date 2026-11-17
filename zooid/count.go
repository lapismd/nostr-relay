package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/Masterminds/squirrel"
	"zooid/internal/khatru"
)

const (
	exactCountEstimateThreshold = uint64(50_000)
	optionalExactCountBudget    = 250 * time.Millisecond
)

var errUnsafeCountFilter = errors.New("filter visibility cannot be represented safely in SQL")

type countPlan struct {
	Plan struct {
		Rows uint64 `json:"Plan Rows"`
	} `json:"Plan"`
}

func (instance *Instance) OnCountMany(ctx context.Context, filters []nostr.Filter) (bool, string) {
	if len(filters) == 0 {
		return true, "invalid: at least one filter is required"
	}
	for _, filter := range filters {
		if reject, reason := instance.OnRequest(ctx, filter); reject {
			return true, reason
		}
		if err := instance.validateCountVisibility(ctx, filter); err != nil {
			CountRequests.WithLabelValues(instance.Config.Schema, "rejected").Inc()
			return true, "restricted: " + err.Error()
		}
	}
	return false, ""
}

func (instance *Instance) CountStored(ctx context.Context, filters []nostr.Filter) (result khatru.CountResult, err error) {
	started := time.Now()
	outcome := "error"
	defer func() {
		CountRequests.WithLabelValues(instance.Config.Schema, outcome).Inc()
		CountDuration.WithLabelValues(instance.Config.Schema, outcome).Observe(time.Since(started).Seconds())
	}()

	if len(filters) == 0 {
		outcome = "rejected"
		return khatru.CountResult{}, errors.New("invalid: at least one filter is required")
	}
	for _, filter := range filters {
		if err := instance.validateCountVisibility(ctx, filter); err != nil {
			outcome = "rejected"
			return khatru.CountResult{}, fmt.Errorf("restricted: %w", err)
		}
	}

	query, args, err := instance.Events.buildCountUnionQuery(filters)
	if err != nil {
		outcome = "rejected"
		return khatru.CountResult{}, fmt.Errorf("invalid: %w", err)
	}

	estimateCtx, cancelEstimate := context.WithTimeout(ctx, dbOpTimeout)
	estimate, err := instance.Events.estimateCountRows(estimateCtx, query, args)
	cancelEstimate()
	if err != nil {
		return khatru.CountResult{}, fmt.Errorf("count estimate failed: %w", err)
	}
	CountRowEstimate.WithLabelValues(instance.Config.Schema).Observe(float64(estimate))

	forceExact := registryDiscussionFilters(filters)
	if !forceExact && estimate > exactCountEstimateThreshold {
		outcome = "approximate"
		return khatru.CountResult{Count: estimate, Approximate: true}, nil
	}

	exactCtx := ctx
	cancel := func() {}
	if !forceExact {
		exactCtx, cancel = context.WithTimeout(ctx, optionalExactCountBudget)
	}
	defer cancel()

	count, err := instance.Events.executeCount(exactCtx, query, args)
	if err != nil {
		if !forceExact && errors.Is(err, context.DeadlineExceeded) {
			outcome = "timeout"
			return khatru.CountResult{Count: estimate, Approximate: true}, nil
		}
		return khatru.CountResult{}, fmt.Errorf("exact count failed: %w", err)
	}
	outcome = "exact"
	return khatru.CountResult{Count: count}, nil
}

func containsAnyKind(kinds []nostr.Kind, candidates ...nostr.Kind) bool {
	for _, kind := range kinds {
		for _, candidate := range candidates {
			if kind == candidate {
				return true
			}
		}
	}
	return false
}

func registryDiscussionFilters(filters []nostr.Filter) bool {
	if len(filters) == 0 {
		return false
	}
	for _, filter := range filters {
		if len(filter.IDs) > 0 || len(filter.Authors) > 0 || filter.Search != "" || len(filter.Kinds) == 0 {
			return false
		}
		if len(filter.Tags) != 1 || len(filter.Tags["h"]) == 0 {
			return false
		}
		for _, kind := range filter.Kinds {
			if kind != 11 && kind != 1111 {
				return false
			}
		}
	}
	return true
}

func (events *EventStore) buildCountUnionQuery(filters []nostr.Filter) (string, []any, error) {
	return events.buildCountUnionQueryWithVisibility(filters, true)
}

func (events *EventStore) buildRawCountUnionQuery(filters []nostr.Filter) (string, []any, error) {
	return events.buildCountUnionQueryWithVisibility(filters, false)
}

func (events *EventStore) buildCountUnionQueryWithVisibility(filters []nostr.Filter, visibleProjection bool) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, errors.New("at least one filter is required")
	}

	branches := make([]string, 0, len(filters))
	args := make([]any, 0)
	for _, filter := range filters {
		branch, branchArgs, err := events.buildCountIDQuery(filter, visibleProjection)
		if err != nil {
			return "", nil, err
		}
		branches = append(branches, "("+branch+")")
		args = append(args, branchArgs...)
	}

	query := "SELECT COUNT(*) FROM (" + strings.Join(branches, " UNION ") + ") AS matching_events"
	query, err := squirrel.Dollar.ReplacePlaceholders(query)
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

func (events *EventStore) buildCountIDQuery(filter nostr.Filter, visibleProjection bool) (string, []any, error) {
	queryBuilder := squirrel.StatementBuilder.PlaceholderFormat(squirrel.Question)
	eventsTable := events.Schema.Prefix("events")
	tagsTable := events.Schema.Prefix("event_tags")

	type tagFilter struct {
		key    string
		values []any
	}
	tagFilters := make([]tagFilter, 0, len(filter.Tags))
	for key, values := range filter.Tags {
		if len(key) != 1 || len(values) == 0 {
			return "", nil, fmt.Errorf("unsupported tag filter %q", key)
		}
		converted := make([]any, len(values))
		for index, value := range values {
			converted[index] = value
		}
		tagFilters = append(tagFilters, tagFilter{key: key, values: converted})
	}
	sort.Slice(tagFilters, func(left, right int) bool { return tagFilters[left].key < tagFilters[right].key })

	kinds := make([]any, len(filter.Kinds))
	for index, kind := range filter.Kinds {
		kinds[index] = int(kind)
	}

	column := ""
	var query squirrel.SelectBuilder
	if len(tagFilters) > 0 {
		column = "e."
		parts := make([]string, 0, len(tagFilters))
		args := make([]any, 0)
		for _, tag := range tagFilters {
			part := queryBuilder.Select("event_id").
				From(tagsTable).
				Where(squirrel.Eq{"key": tag.key}).
				Where(squirrel.Eq{"value": tag.values})
			if len(kinds) > 0 {
				part = part.Where(squirrel.Or{squirrel.Eq{"kind": kinds}, squirrel.Expr("kind IS NULL")})
			}
			partSQL, partArgs, err := part.ToSql()
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, partSQL)
			args = append(args, partArgs...)
		}
		query = queryBuilder.Select("DISTINCT e.id").
			Prefix("WITH _tag_ids AS MATERIALIZED ("+strings.Join(parts, " INTERSECT ")+")", args...).
			From(eventsTable + " e").
			Join("_tag_ids t ON t.event_id = e.id")
	} else {
		query = queryBuilder.Select("DISTINCT id").From(eventsTable)
	}

	if filter.Search != "" {
		query = query.Where(column+"search_vector @@ plainto_tsquery('english', ?)", filter.Search)
	}
	if len(filter.IDs) > 0 {
		ids := make([]any, len(filter.IDs))
		for index, id := range filter.IDs {
			ids[index] = id.Hex()
		}
		query = query.Where(squirrel.Eq{column + "id": ids})
	}
	if len(filter.Authors) > 0 {
		authors := make([]any, len(filter.Authors))
		for index, author := range filter.Authors {
			authors[index] = author.Hex()
		}
		query = query.Where(squirrel.Eq{column + "pubkey": authors})
	}
	if len(kinds) > 0 {
		query = query.Where(squirrel.Eq{column + "kind": kinds})
	}
	if filter.Since != 0 {
		query = query.Where(squirrel.GtOrEq{column + "created_at": filter.Since})
	}
	if filter.Until != 0 {
		query = query.Where(squirrel.LtOrEq{column + "created_at": filter.Until})
	}

	if visibleProjection {
		// Match QueryStored's non-generated projection without selecting event
		// bodies. These exclusions are harmless for group-scoped filters and
		// close off malformed tag combinations that could expose internals.
		query = query.Where(squirrel.NotEq{column + "kind": []any{int(RELAY_INVITE), int(RELAY_JOIN), int(RELAY_LEAVE)}})
		query = query.Where("NOT ("+column+"kind = ? AND EXISTS (SELECT 1 FROM "+tagsTable+" internal_tags WHERE internal_tags.event_id = "+column+"id AND internal_tags.key = 'd' AND internal_tags.value LIKE 'zooid/%'))", int(nostr.KindApplicationSpecificData))
	}

	return query.ToSql()
}

func (events *EventStore) estimateCountRows(ctx context.Context, countQuery string, args []any) (uint64, error) {
	query := strings.TrimPrefix(countQuery, "SELECT COUNT(*) FROM ")
	explain := "EXPLAIN (FORMAT JSON) SELECT * FROM " + query
	var raw []byte
	if err := GetDb().QueryRowContext(ctx, explain, args...).Scan(&raw); err != nil {
		return 0, err
	}
	var plans []countPlan
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		return 0, fmt.Errorf("invalid PostgreSQL count plan")
	}
	return plans[0].Plan.Rows, nil
}

func (events *EventStore) executeCount(ctx context.Context, query string, args []any) (uint64, error) {
	var count uint64
	if err := GetDb().QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}
