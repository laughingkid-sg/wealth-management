package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	job          *Job
	retried      bool
	completed    bool
	retryAt      time.Time
	renewed      chan struct{}
	renewErr     error
	renewStarted chan struct{}
}

func (s *memoryStore) Claim(context.Context, string, time.Time) (*Job, error) { return s.job, nil }
func (s *memoryStore) RenewLease(ctx context.Context, _ uuid.UUID, _ string, _ time.Time) error {
	if s.renewed != nil {
		select {
		case s.renewed <- struct{}{}:
		default:
		}
	}
	if s.renewStarted != nil {
		close(s.renewStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	return s.renewErr
}
func (s *memoryStore) Complete(context.Context, uuid.UUID, string) error {
	s.completed = true
	return nil
}
func (s *memoryStore) Retry(_ context.Context, _ uuid.UUID, _ string, _ int, at time.Time, _ string) error {
	s.retried = true
	s.retryAt = at
	return nil
}

func TestWorkerRenewsLeaseWhileHandlerIsRunning(t *testing.T) {
	renewed := make(chan struct{}, 4)
	store := &memoryStore{job: &Job{ID: uuid.New(), Attempts: 1}, renewed: renewed}
	handlerDone := make(chan struct{})
	worker := Worker{
		Store: store, WorkerID: "worker-1", LeaseHeartbeatInterval: 5 * time.Millisecond,
		Handler: handlerFunc(func(ctx context.Context, _ Job) error {
			for range 2 {
				select {
				case <-renewed:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			close(handlerDone)
			return nil
		}),
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %t, %v", processed, err)
	}
	select {
	case <-handlerDone:
	default:
		t.Fatal("handler did not observe lease renewals")
	}
	if !store.completed || store.retried {
		t.Fatalf("completed=%t retried=%t", store.completed, store.retried)
	}
}

func TestWorkerCancelsHandlerAndDoesNotFinalizeAfterLostLease(t *testing.T) {
	store := &memoryStore{job: &Job{ID: uuid.New(), Attempts: 1}, renewErr: ErrLeaseLost}
	handlerCancelled := make(chan struct{})
	worker := Worker{
		Store: store, WorkerID: "worker-1", LeaseHeartbeatInterval: time.Millisecond,
		Handler: handlerFunc(func(ctx context.Context, _ Job) error {
			<-ctx.Done()
			close(handlerCancelled)
			return ctx.Err()
		}),
	}
	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("ProcessOne() = %t, %v", processed, err)
	}
	select {
	case <-handlerCancelled:
	default:
		t.Fatal("lost lease did not cancel handler")
	}
	if store.completed || store.retried {
		t.Fatalf("stale worker finalized job: completed=%t retried=%t", store.completed, store.retried)
	}
}

func TestWorkerCompletesWhenHandlerCancelsInflightRenewal(t *testing.T) {
	renewStarted := make(chan struct{})
	store := &memoryStore{job: &Job{ID: uuid.New(), Attempts: 1}, renewStarted: renewStarted}
	worker := Worker{
		Store: store, WorkerID: "worker-1", LeaseHeartbeatInterval: time.Millisecond,
		Handler: handlerFunc(func(ctx context.Context, _ Job) error {
			select {
			case <-renewStarted:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
	}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %t, %v", processed, err)
	}
	if !store.completed || store.retried {
		t.Fatalf("completed=%t retried=%t", store.completed, store.retried)
	}
}

type handlerFunc func(context.Context, Job) error

func (f handlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }

type scopedMemoryStore struct {
	*memoryStore
	kinds []Kind
}

func (s *scopedMemoryStore) ClaimKinds(_ context.Context, _ string, _ time.Time, kinds []Kind) (*Job, error) {
	s.kinds = append([]Kind(nil), kinds...)
	return s.job, nil
}

func TestWorkerClaimsOnlyConfiguredKinds(t *testing.T) {
	store := &scopedMemoryStore{memoryStore: &memoryStore{}}
	allowed := []Kind{KindGmailIngest, KindSourceParse}
	processed, err := (Worker{Store: store, WorkerID: "worker-1", Handler: Router{}, AllowedKinds: allowed}).ProcessOne(context.Background())
	if err != nil || processed || len(store.kinds) != len(allowed) || store.kinds[0] != allowed[0] || store.kinds[1] != allowed[1] {
		t.Fatalf("processed=%t err=%v kinds=%v", processed, err, store.kinds)
	}
}

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
