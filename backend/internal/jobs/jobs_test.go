package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	job     *Job
	retried bool
	retryAt time.Time
}

func (s *memoryStore) Claim(context.Context, string, time.Time) (*Job, error) { return s.job, nil }
func (s *memoryStore) Complete(context.Context, uuid.UUID, string) error      { return nil }
func (s *memoryStore) Retry(_ context.Context, _ uuid.UUID, _ string, _ int, at time.Time, _ string) error {
	s.retried = true
	s.retryAt = at
	return nil
}

type handlerFunc func(context.Context, Job) error

func (f handlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }

func TestWorkerRetriesWithBoundedBackoff(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	store := &memoryStore{job: &Job{ID: uuid.New(), Attempts: 3}}
	worker := Worker{Store: store, WorkerID: "worker-1", Now: func() time.Time { return now }, Handler: handlerFunc(func(context.Context, Job) error { return errors.New("provider unavailable") })}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed || !store.retried {
		t.Fatalf("ProcessOne() = %t, %v, retried %t", processed, err, store.retried)
	}
	if want := now.Add(4 * time.Second); !store.retryAt.Equal(want) {
		t.Fatalf("retry at %s, want %s", store.retryAt, want)
	}
}

func TestBackoffIsBounded(t *testing.T) {
	if got, want := Backoff(100), 64*time.Second; got != want {
		t.Fatalf("Backoff(100) = %s, want %s", got, want)
	}
}
