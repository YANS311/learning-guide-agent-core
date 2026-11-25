package importer

import (
	"context"
	"os"
	"strings"
	"testing"

	"learning-guide/graph-ops/internal/graph"
)

func TestCSVFixtureAndDiagnostics(t *testing.T) {
	input, err := os.ReadFile("../../testdata/import.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, issue := DecodeCSV(context.Background(), append([]byte{0xef, 0xbb, 0xbf}, input...))
	if issue != nil || len(rows.Courses) != 2 || len(rows.Relations) != 1 || rows.Courses[0].Hours != 30 {
		t.Fatalf("%+v %+v", rows, issue)
	}
	for _, tc := range []struct{ row, code, location string }{
		{"course,X01,A,,,text,30,,,,,,,", "invalid_type", "line 2.credits"},
		{"course,X01,A,,,2,30.5,,,,,,,", "invalid_type", "line 2.hours"},
		{"course,X01,A,,,2,30,,,PREREQ,,,,", "invalid_csv", "line 2"},
		{"oops,,,,,,,,,,,,,", "invalid_csv", "line 2.kind"},
		{"course,X01,A", "invalid_csv", "line 2"},
	} {
		_, issue := DecodeCSV(context.Background(), []byte(CSVHeader+"\n"+tc.row+"\n"))
		if issue == nil || issue.Code != tc.code || issue.Location != tc.location {
			t.Fatalf("%q: %+v", tc.row, issue)
		}
	}
	_, issue = DecodeCSV(context.Background(), []byte(CSVHeader+"\n"+strings.Repeat("course,X01,A,,,2,30,,,,,,,\n", graph.MaxCourses+1)))
	if issue == nil || issue.Code != "record_limit" {
		t.Fatalf("%+v", issue)
	}
}

func TestCanceledParsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, issue := DecodeCSV(ctx, []byte(CSVHeader+"\ncourse,X01,A,,,2,30,,,,,,,\n"))
	if issue != nil || len(got.Courses) != 0 {
		t.Fatalf("%+v %+v", got, issue)
	}
	var dest graph.Data
	if issue := DecodeJSON(ctx, []byte(`{"courses":[{"id":"X01","name":"A"}]}`), &dest); issue != nil || len(dest.Courses) != 0 {
		t.Fatalf("%+v %+v", dest, issue)
	}
}
