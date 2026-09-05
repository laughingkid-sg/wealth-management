package scriptstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

func TestChecksumIsStableSha256Hex(t *testing.T) {
	// sha256("") — the well-known empty-string digest, in the DB's required
	// lowercase-hex form.
	if got := Checksum(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("Checksum(\"\") = %q", got)
	}
	if len(Checksum("output := input")) != 64 {
		t.Fatalf("checksum must be 64 hex chars")
	}
}

func TestValidateKeyMatchesContract(t *testing.T) {
	for _, ok := range []string{"email_pre_process", "transaction_post_process", "a1"} {
		if !ValidateKey(ok) {
			t.Fatalf("key %q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "A", "1abc", "has-dash", "x", "White Space"} {
		if ValidateKey(bad) {
			t.Fatalf("key %q should be invalid", bad)
		}
	}
}

func TestScriptStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID := uuid.New()
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`, userID, "scriptstore-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	key := "zz_test_" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from private.script_definitions where script_key = $1`, key)
		_, _ = pool.Exec(context.Background(), `delete from auth.users where id = $1`, userID)
	})

	store := New(pool)

	// No active script yet.
	if _, err := store.LoadActiveScript(ctx, key); !errors.Is(err, ErrNoActiveScript) {
		t.Fatalf("LoadActiveScript before create = %v, want ErrNoActiveScript", err)
	}

	v1, err := store.CreateVersion(ctx, key, "output := input", "first", userID)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v1.IsActive {
		t.Fatalf("v1 = %+v, want version 1 inactive", v1)
	}
	v2, err := store.CreateVersion(ctx, key, "output := {a: input}", "second", userID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 {
		t.Fatalf("v2.Version = %d, want 2", v2.Version)
	}

	// Activate v2, then verify LoadActiveScript returns it.
	if err := store.Activate(ctx, key, 2); err != nil {
		t.Fatal(err)
	}
	active, err := store.LoadActiveScript(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 2 || active.Source != "output := {a: input}" {
		t.Fatalf("active = %+v, want version 2 source", active)
	}

	// Rollback to v1; only one active version at a time.
	if err := store.Activate(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	active, err = store.LoadActiveScript(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 1 {
		t.Fatalf("after rollback active.Version = %d, want 1", active.Version)
	}

	versions, err := store.ListVersions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("ListVersions returned %d, want 2", len(versions))
	}

	if _, err := store.GetVersion(ctx, key, 99); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("GetVersion(99) = %v, want ErrScriptNotFound", err)
	}
	if err := store.Activate(ctx, key, 99); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("Activate(99) = %v, want ErrScriptNotFound", err)
	}
}
