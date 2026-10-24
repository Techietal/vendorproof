package database_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"vendorproof/db/migrations"
	"vendorproof/internal/database"
	"vendorproof/internal/testutil"
)

func TestInitialSchemaManifestGolden(t *testing.T) {
	_, pool := testutil.NewRawDB(t)
	initial, err := migrations.FS.ReadFile("000001_initial.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(initial)); err != nil {
		t.Fatal(err)
	}
	got, err := database.CaptureSchemaManifestJSON(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile("initial_schema.json", append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden manifest rewritten")
		return
	}

	raw, err := os.ReadFile("initial_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, actual any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, actual) {
		t.Fatal("embedded manifest is stale; run UPDATE_GOLDEN=1 go test ./internal/database -run TestInitialSchemaManifestGolden")
	}
}
