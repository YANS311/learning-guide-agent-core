package neoops

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const MaxHistoryPage = 50
const maxHistoryRecords = 10000

var storedID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type History struct {
	Items      []Operation `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func direction(a, b Operation) int {
	if n := b.Finished.Compare(a.Finished); n != 0 {
		return n
	}
	return strings.Compare(b.ID, a.ID)
}

func (s *Service) History(ctx context.Context, limit int, cursor, status string) (History, error) {
	h := History{Items: []Operation{}}
	if limit < 1 || limit > MaxHistoryPage || (cursor != "" && !storedID.MatchString(cursor)) {
		return h, fail(422, "invalid_pagination")
	}
	if status != "" && status != "committed" {
		return h, fail(422, "unsupported_history_status")
	}
	raw, err := s.repo.read(ctx, func(tx runner) (any, error) {
		if _, err := s.repo.epoch(ctx, tx, false); err != nil {
			return nil, err
		}
		rows, err := records(ctx, tx, "MATCH (o:GraphOpsOperation) RETURN o.id AS id,o.result AS result LIMIT $limit", map[string]any{"limit": maxHistoryRecords + 1})
		if err != nil {
			return nil, err
		}
		if len(rows) > maxHistoryRecords {
			return nil, fail(503, "history_limit_reached")
		}
		items := make([]Operation, 0, len(rows))
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id, _ := row.Get("id")
			value, _ := row.Get("result")
			data, ok := value.(string)
			var op Operation
			if !ok || json.Unmarshal([]byte(data), &op) != nil || id != op.ID || op.Status != "committed" || !storedID.MatchString(op.ID) || op.Finished.IsZero() {
				return nil, fail(503, "invalid_operation_record")
			}
			items = append(items, op)
		}
		return items, nil
	})
	if err != nil {
		return h, err
	}
	items := raw.([]Operation)
	slices.SortFunc(items, direction)
	if cursor != "" {
		index := slices.IndexFunc(items, func(op Operation) bool { return op.ID == cursor })
		if index < 0 {
			return h, fail(422, "invalid_pagination")
		}
		items = items[index+1:]
	}
	if len(items) > limit {
		items = items[:limit]
		h.NextCursor = items[limit-1].ID
	}
	h.Items = items
	return h, nil
}

func (s *Service) Detail(ctx context.Context, id string) (Operation, error) {
	if !storedID.MatchString(id) {
		return Operation{}, fail(422, "invalid_operation_id")
	}
	result, err := s.repo.read(ctx, func(tx runner) (any, error) {
		if _, err := s.repo.epoch(ctx, tx, false); err != nil {
			return nil, err
		}
		op, ok, err := getOperation(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fail(404, "operation_not_observed")
		}
		return op, nil
	})
	if err != nil {
		return Operation{}, err
	}
	return result.(Operation), nil
}
