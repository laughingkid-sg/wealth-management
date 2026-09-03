package transactionstore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zhengteck/wealth-builder/backend/internal/database"
)

func TestCleanupRetryCyclesWithoutTerminalAbandonment(t *testing.T) {
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

	userID, sourceID, jobID := uuid.New(), uuid.New(), uuid.New()
	workerID := "cleanup-retry-test"
	objectPath := userID.String() + "/" + sourceID.String() + "/receipt.pdf"
	payload, err := json.Marshal(map[string]any{
		"source_id": sourceID.String(), "object_paths": []string{objectPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`,
		userID, "cleanup-retry-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, `delete from auth.users where id = $1`, userID)
	}()
	if _, err = pool.Exec(ctx, `
		insert into private.transaction_jobs (
			id, user_id, job_type, payload, status, attempts, max_attempts,
			leased_at, lease_expires_at, leased_by
		) values (
			$1, $2, 'source_attachment_cleanup', $3::jsonb, 'running', 5, 5,
			now(), now() + interval '5 minutes', $4
		)`, jobID, userID, string(payload), workerID); err != nil {
		t.Fatal(err)
	}

	store := New(pool)
	for cycle := int64(1); cycle <= 2; cycle++ {
		retryAt := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
		if err = store.Retry(ctx, jobID, workerID, 5, retryAt, "provider body must not be stored"); err != nil {
			t.Fatal(err)
		}
		var status, lastError string
		var attempts int
		var failures int64
		var runAfter time.Time
		var completedAt, leasedAt, leaseExpiresAt *time.Time
		var leasedBy *string
		var retainedPayload []byte
		if err = pool.QueryRow(ctx, `
			select status, attempts, cleanup_failure_count, run_after,
				completed_at, leased_at, lease_expires_at, leased_by,
				last_error, payload
			from private.transaction_jobs where id = $1`, jobID).Scan(
			&status, &attempts, &failures, &runAfter,
			&completedAt, &leasedAt, &leaseExpiresAt, &leasedBy,
			&lastError, &retainedPayload,
		); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || attempts != 0 || failures != cycle {
			t.Fatalf("cycle %d: status=%q attempts=%d failures=%d", cycle, status, attempts, failures)
		}
		if delay := runAfter.Sub(retryAt); delay < sourceCleanupRetryCooldown-time.Millisecond ||
			delay > sourceCleanupRetryCooldown+time.Millisecond {
			t.Fatalf("cycle %d: recovery delay=%s, want %s", cycle, delay, sourceCleanupRetryCooldown)
		}
		if completedAt != nil || leasedAt != nil || leaseExpiresAt != nil || leasedBy != nil {
			t.Fatalf("cycle %d retained terminal/lease state", cycle)
		}
		if !strings.Contains(lastError, "remains queued") || strings.Contains(lastError, objectPath) {
			t.Fatalf("cycle %d unsafe or unclear diagnostic %q", cycle, lastError)
		}
		var retained struct {
			SourceID    string   `json:"source_id"`
			ObjectPaths []string `json:"object_paths"`
		}
		if err = json.Unmarshal(retainedPayload, &retained); err != nil {
			t.Fatal(err)
		}
		if retained.SourceID != sourceID.String() || len(retained.ObjectPaths) != 1 ||
			retained.ObjectPaths[0] != objectPath {
			t.Fatalf("cycle %d changed cleanup payload: %#v", cycle, retained)
		}
		if cycle < 2 {
			if _, err = pool.Exec(ctx, `
				update private.transaction_jobs
				set status = 'running', attempts = 5, leased_at = now(),
					lease_expires_at = now() + interval '5 minutes', leased_by = $2
				where id = $1`, jobID, workerID); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err = pool.Exec(ctx, `
		update private.transaction_jobs
		set status = 'running', attempts = 1, leased_at = now(),
			lease_expires_at = now() + interval '5 minutes', leased_by = $2
		where id = $1`, jobID, workerID); err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(ctx, jobID, workerID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = pool.QueryRow(ctx, `select count(*) from private.transaction_jobs where id = $1`, jobID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("successful Storage cleanup retained %d outbox rows", remaining)
	}
}

func TestExpiredFinalCleanupLeaseIsRequeued(t *testing.T) {
	databaseURL := os.Getenv("TRANSACTIONSTORE_TEST_DB_URL")
	if databaseURL == "" {
		t.Skip("TRANSACTIONSTORE_TEST_DB_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.OpenTransactionPooler(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	userID, jobID := uuid.New(), uuid.New()
	if _, err = tx.Exec(ctx, `insert into auth.users (id, email) values ($1, $2)`,
		userID, "cleanup-lease-"+userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		insert into private.transaction_jobs (
			id, user_id, job_type, payload, status, attempts, max_attempts,
			leased_at, lease_expires_at, leased_by
		) values (
			$1, $2, 'source_attachment_cleanup', '{}', 'running', 5, 5,
			now() - interval '6 minutes', now() - interval '1 minute', 'expired-worker'
		)`, jobID, userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err = reapExpiredFinalAttempts(ctx, tx, now); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	var attempts int
	var failures int64
	var runAfter time.Time
	var completedAt *time.Time
	if err = tx.QueryRow(ctx, `
		select status, attempts, cleanup_failure_count, run_after, completed_at, last_error
		from private.transaction_jobs where id = $1`, jobID).Scan(
		&status, &attempts, &failures, &runAfter, &completedAt, &lastError,
	); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || attempts != 0 || failures != 1 || completedAt != nil {
		t.Fatalf("status=%q attempts=%d failures=%d completed_at=%v", status, attempts, failures, completedAt)
	}
	if delay := runAfter.Sub(now); delay < sourceCleanupRetryCooldown-time.Millisecond ||
		delay > sourceCleanupRetryCooldown+time.Millisecond {
		t.Fatalf("recovery delay=%s, want %s", delay, sourceCleanupRetryCooldown)
	}
	if !strings.Contains(lastError, "lease expired") || !strings.Contains(lastError, "remains queued") {
		t.Fatalf("unexpected diagnostic %q", lastError)
	}
}
