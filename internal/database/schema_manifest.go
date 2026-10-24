package database

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// The golden manifest is generated from 000001_initial.up.sql by
// TestInitialSchemaManifestGolden. Baseline compares the live catalogs against
// it; it is not a parsed SQL comparison.
//
//go:embed initial_schema.json
var initialSchemaFS embed.FS

type schemaManifest struct {
	Tables    map[string]tableShape `json:"tables"`
	Functions []string              `json:"functions"`
}

type tableShape struct {
	Columns     []columnShape `json:"columns"`
	Constraints []objectShape `json:"constraints"`
	Indexes     []objectShape `json:"indexes"`
	Triggers    []string      `json:"triggers"`
}

type columnShape struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"not_null"`
	Default string `json:"default"`
}

type objectShape struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Definition string `json:"definition"`
}

var (
	expectedOnce sync.Once
	expected     schemaManifest
	expectedErr  error
)

func loadExpectedManifest() (schemaManifest, error) {
	expectedOnce.Do(func() {
		raw, err := initialSchemaFS.ReadFile("initial_schema.json")
		if err != nil {
			expectedErr = err
			return
		}
		if err := json.Unmarshal(raw, &expected); err != nil {
			expectedErr = fmt.Errorf("decode schema manifest: %w", err)
		}
	})
	return expected, expectedErr
}

// captureManifest reads the public schema through stable PostgreSQL catalogs.
func captureManifest(ctx context.Context, q Querier) (schemaManifest, error) {
	m := schemaManifest{Tables: map[string]tableShape{}, Functions: []string{}}

	tableRows, err := q.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY 1`)
	if err != nil {
		return m, err
	}
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			tableRows.Close()
			return m, err
		}
		m.Tables[name] = tableShape{Columns: []columnShape{}, Constraints: []objectShape{}, Indexes: []objectShape{}, Triggers: []string{}}
	}
	tableRows.Close()
	if err := tableRows.Err(); err != nil {
		return m, err
	}

	for name, shape := range m.Tables {
		cols, err := q.Query(ctx, `
			SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
			       coalesce(pg_get_expr(d.adbin, d.adrelid), '')
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE n.nspname = 'public' AND c.relname = $1 AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`, name)
		if err != nil {
			return m, err
		}
		for cols.Next() {
			var col columnShape
			if err := cols.Scan(&col.Name, &col.Type, &col.NotNull, &col.Default); err != nil {
				cols.Close()
				return m, err
			}
			shape.Columns = append(shape.Columns, col)
		}
		cols.Close()

		cons, err := q.Query(ctx, `
			SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid)
			FROM pg_constraint con
			JOIN pg_class c ON c.oid = con.conrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1`, name)
		if err != nil {
			return m, err
		}
		for cons.Next() {
			var obj objectShape
			if err := cons.Scan(&obj.Name, &obj.Kind, &obj.Definition); err != nil {
				cons.Close()
				return m, err
			}
			shape.Constraints = append(shape.Constraints, obj)
		}
		cons.Close()

		idxs, err := q.Query(ctx, `
			SELECT ic.relname, pg_get_indexdef(i.indexrelid)
			FROM pg_index i
			JOIN pg_class ic ON ic.oid = i.indexrelid
			JOIN pg_class tc ON tc.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = tc.relnamespace
			WHERE n.nspname = 'public' AND tc.relname = $1`, name)
		if err != nil {
			return m, err
		}
		for idxs.Next() {
			var obj objectShape
			if err := idxs.Scan(&obj.Name, &obj.Definition); err != nil {
				idxs.Close()
				return m, err
			}
			shape.Indexes = append(shape.Indexes, obj)
		}
		idxs.Close()

		trigs, err := q.Query(ctx, `
			SELECT t.tgname
			FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1 AND NOT t.tgisinternal
			ORDER BY t.tgname`, name)
		if err != nil {
			return m, err
		}
		for trigs.Next() {
			var trigger string
			if err := trigs.Scan(&trigger); err != nil {
				trigs.Close()
				return m, err
			}
			shape.Triggers = append(shape.Triggers, trigger)
		}
		trigs.Close()

		sort.Slice(shape.Columns, func(i, j int) bool { return shape.Columns[i].Name < shape.Columns[j].Name })
		sort.Slice(shape.Constraints, func(i, j int) bool { return shape.Constraints[i].Name < shape.Constraints[j].Name })
		sort.Slice(shape.Indexes, func(i, j int) bool { return shape.Indexes[i].Name < shape.Indexes[j].Name })
		sort.Strings(shape.Triggers)
		m.Tables[name] = shape
	}

	fnRows, err := q.Query(ctx, `SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname='public'`)
	if err != nil {
		return m, err
	}
	for fnRows.Next() {
		var fn string
		if err := fnRows.Scan(&fn); err != nil {
			fnRows.Close()
			return m, err
		}
		m.Functions = append(m.Functions, fn)
	}
	fnRows.Close()
	sort.Strings(m.Functions)
	return m, fnRows.Err()
}

func compareManifests(expected, actual schemaManifest) []string {
	var diffs []string
	for name := range expected.Tables {
		if _, ok := actual.Tables[name]; !ok {
			diffs = append(diffs, "missing table "+name)
		}
	}
	for name := range actual.Tables {
		if _, ok := expected.Tables[name]; !ok {
			diffs = append(diffs, "unexpected table "+name)
		}
	}

	for name, want := range expected.Tables {
		got, ok := actual.Tables[name]
		if !ok {
			continue
		}
		diffs = append(diffs, compareColumns(name, want.Columns, got.Columns)...)
		diffs = append(diffs, compareObjects(name, "constraint", want.Constraints, got.Constraints)...)
		diffs = append(diffs, compareObjects(name, "index", want.Indexes, got.Indexes)...)
		if !reflect.DeepEqual(want.Triggers, got.Triggers) {
			diffs = append(diffs, fmt.Sprintf("table %s: triggers %v, expected %v", name, got.Triggers, want.Triggers))
		}
	}
	if !reflect.DeepEqual(expected.Functions, actual.Functions) {
		diffs = append(diffs, fmt.Sprintf("public functions %v, expected %v", actual.Functions, expected.Functions))
	}
	return diffs
}

func compareColumns(table string, want, got []columnShape) []string {
	wantMap := make(map[string]columnShape, len(want))
	for _, col := range want {
		wantMap[col.Name] = col
	}
	gotMap := make(map[string]columnShape, len(got))
	for _, col := range got {
		gotMap[col.Name] = col
	}
	var diffs []string
	for name := range wantMap {
		if _, ok := gotMap[name]; !ok {
			diffs = append(diffs, fmt.Sprintf("table %s: missing column %s", table, name))
		}
	}
	for name, col := range gotMap {
		wantCol, ok := wantMap[name]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("table %s: unexpected column %s", table, name))
			continue
		}
		if wantCol != col {
			diffs = append(diffs, fmt.Sprintf("table %s column %s: got %s not_null=%t default=%q, expected %s not_null=%t default=%q",
				table, name, col.Type, col.NotNull, col.Default, wantCol.Type, wantCol.NotNull, wantCol.Default))
		}
	}
	return diffs
}

func compareObjects(table, kind string, want, got []objectShape) []string {
	wantMap := make(map[string]objectShape, len(want))
	for _, obj := range want {
		wantMap[obj.Name] = obj
	}
	gotMap := make(map[string]objectShape, len(got))
	for _, obj := range got {
		gotMap[obj.Name] = obj
	}
	var diffs []string
	for name := range wantMap {
		if _, ok := gotMap[name]; !ok {
			diffs = append(diffs, fmt.Sprintf("table %s: missing %s %s", table, kind, name))
		}
	}
	for name, obj := range gotMap {
		wantObj, ok := wantMap[name]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("table %s: unexpected %s %s", table, kind, name))
			continue
		}
		if wantObj != obj {
			diffs = append(diffs, fmt.Sprintf("table %s %s %s differs: got %q, expected %q", table, kind, name, obj.Definition, wantObj.Definition))
		}
	}
	return diffs
}

func summarize(diffs []string) string {
	const max = 15
	if len(diffs) > max {
		return strings.Join(diffs[:max], "; ") + fmt.Sprintf("; and %d more", len(diffs)-max)
	}
	return strings.Join(diffs, "; ")
}

// CaptureSchemaManifestJSON returns the indented manifest for the live public
// schema. It is exported for golden-generation tests and diagnostics.
func CaptureSchemaManifestJSON(ctx context.Context, q Querier) ([]byte, error) {
	m, err := captureManifest(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}
