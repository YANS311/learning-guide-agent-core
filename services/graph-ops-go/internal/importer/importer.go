package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"learning-guide/graph-ops/internal/graph"
)

func DecodeJSON(ctx context.Context, data []byte, dst any) *graph.Issue {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := jsonShape(ctx, decoder, "$", 0); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		var invalid *shapeError
		if errors.As(err, &invalid) {
			return &graph.Issue{Code: "invalid_json", Location: invalid.location, Message: invalid.message}
		}
		return &graph.Issue{Code: "invalid_json", Location: "$", Message: "malformed or empty JSON"}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return &graph.Issue{Code: "invalid_json", Location: "$", Message: "exactly one JSON object is required"}
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return &graph.Issue{Code: "invalid_type", Location: "$." + typeErr.Field, Message: "incorrect JSON field type or numeric range"}
		}
		return &graph.Issue{Code: "invalid_json", Location: "$", Message: "expected a JSON object with supported fields"}
	}
	return nil
}

type shapeError struct{ location, message string }

func (e *shapeError) Error() string { return e.message }

func jsonShape(ctx context.Context, decoder *json.Decoder, path string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 12 {
		return &shapeError{path, "JSON nesting limit exceeded"}
	}
	t, err := decoder.Token()
	if err != nil {
		return err
	}
	switch t {
	case nil:
		return &shapeError{path, "null is not permitted"}
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name := key.(string)
			// Field names are user input. Keep them out of diagnostics.
			normalized := strings.ToLower(name)
			if seen[normalized] {
				return &shapeError{path, "duplicate JSON field"}
			}
			seen[normalized] = true
			if err := jsonShape(ctx, decoder, path+".field", depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case json.Delim('['):
		for i := 0; decoder.More(); i++ {
			if err := jsonShape(ctx, decoder, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

const CSVHeader = "kind,id,name,category,semester,credits,hours,layer,description,type,source,target,reason,strength"

func DecodeCSV(ctx context.Context, data []byte) (graph.Data, *graph.Issue) {
	d := graph.Data{}
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	expected := strings.Split(CSVHeader, ",")
	reader.FieldsPerRecord = len(expected)
	header, err := reader.Read()
	if err != nil || !slices.Equal(header, expected) {
		return d, &graph.Issue{Code: "invalid_csv_header", Location: "line 1", Message: "expected documented CSV columns"}
	}
	for {
		if ctx.Err() != nil {
			return d, nil
		}
		row, err := reader.Read()
		if err == io.EOF {
			return d, nil
		}
		if err != nil {
			line := "CSV record"
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				line = fmt.Sprintf("line %d", parse.Line)
			}
			return d, &graph.Issue{Code: "invalid_csv", Location: line, Message: "malformed record or wrong column count"}
		}
		line, _ := reader.FieldPos(0)
		bad := func(code, column, message string) (graph.Data, *graph.Issue) {
			return d, &graph.Issue{Code: code, Location: fmt.Sprintf("line %d%s", line, column), Message: message}
		}
		switch row[0] {
		case "course":
			if anyValue(row[9:]) {
				return bad("invalid_csv", "", "course row contains relation fields")
			}
			c := graph.Course{ID: row[1], Name: row[2], Category: row[3], Semester: row[4], Layer: row[7], Description: row[8]}
			if row[5] != "" {
				c.Credits, err = strconv.ParseFloat(row[5], 64)
				if err != nil {
					return bad("invalid_type", ".credits", "credits must be a number")
				}
			}
			if row[6] != "" {
				c.Hours, err = strconv.Atoi(row[6])
				if err != nil {
					return bad("invalid_type", ".hours", "hours must be an integer")
				}
			}
			d.Courses = append(d.Courses, c)
		case "relation":
			if anyValue(row[1:9]) {
				return bad("invalid_csv", "", "relation row contains course fields")
			}
			d.Relations = append(d.Relations, graph.Relation{Type: row[9], Source: row[10], Target: row[11], Reason: row[12], Strength: row[13]})
		default:
			return bad("invalid_csv", ".kind", "kind must be course or relation")
		}
		if len(d.Courses) > graph.MaxCourses || len(d.Relations) > graph.MaxRelations {
			return bad("record_limit", "", "too many courses or relations")
		}
	}
}

func anyValue(fields []string) bool {
	for _, field := range fields {
		if field != "" {
			return true
		}
	}
	return false
}
