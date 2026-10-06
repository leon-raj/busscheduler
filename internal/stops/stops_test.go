package stops

import (
	"context"
	"os"
	"testing"
)

// Runs only when TEST_DATABASE_URL points at a database that has db/schema.sql + db/seed.sql applied.
func TestPostgresLookup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pg, err := NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	got, err := pg.Lookup(context.Background(), []string{"st20", "st1", "does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["st20"].Lat == 0 {
		t.Fatalf("unexpected lookup result: %+v", got)
	}
}

func TestMemoryLookup(t *testing.T) {
	m := Memory{"a": {Lat: 1, Lon: 2}}
	got, _ := m.Lookup(context.Background(), []string{"a", "b"})
	if len(got) != 1 || got["a"].Lon != 2 {
		t.Fatalf("got %+v", got)
	}
}
