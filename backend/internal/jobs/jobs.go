// Package jobs defines durable, claimable job contracts. PostgreSQL storage is added with its migration.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrLeaseLost = errors.New("job lease lost")

const DefaultLeaseHeartbeatInterval = time.Minute

type Kind string

const (
	KindGmailIngest             Kind = "gmail_ingestion"
	KindSourceParse             Kind = "source_parsing"
	KindReconcile               Kind = "reconciliation"
	KindSourceAttachmentCleanup Kind = "source_attachment_cleanup"
)

type Job struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	SyncRunID  *uuid.UUID
	Kind       Kind
	Payload    []byte
	Attempts   int
	Available  time.Time
	LeaseUntil *time.Time
}

// SourceAttachmentCleanupPayload is persisted in the durable job row before
// its source is deleted. The row's UserID is the owner; SourceID plus these
// exact paths are retained only until Storage cleanup succeeds.
type SourceAttachmentCleanupPayload struct {
	SourceID    string   `json:"source_id"`
	ObjectPaths []string `json:"object_paths"`
}

// Store implementations must claim jobs atomically in a short SQL transaction using row locks.
// They must not use LISTEN/NOTIFY, temp tables, advisory locks, or session state.
type Store interface {
	Claim(context.Context, string, time.Time) (*Job, error)
	RenewLease(context.Context, uuid.UUID, string, time.Time) error
	Complete(context.Context, uuid.UUID, string) error
	Retry(context.Context, uuid.UUID, string, int, time.Time, string) error
}

type Handler interface {
	Handle(context.Context, Job) error
}

// Router dispatches durable job kinds to independently configured handlers.
// It keeps each handler focused while making unsupported queue values fail
// safely and retry through Worker.
type Router map[Kind]Handler

func (r Router) Handle(ctx context.Context, job Job) error {
	handler, ok := r[job.Kind]
	if !ok || handler == nil {
		return errors.New("no handler is configured for job kind " + string(job.Kind))
	}
	return handler.Handle(ctx, job)
}

type Worker struct {
	Store    Store
	WorkerID string
	Handler  Handler
	Now      func() time.Time
	// LeaseHeartbeatInterval is injectable for deterministic tests. Production
	// workers default to renewing once per minute, well within the five-minute
	// database lease.
	LeaseHeartbeatInterval time.Duration
}

func (w Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.Store == nil || w.Handler == nil || w.WorkerID == "" {
		return false, errors.New("worker requires store, worker ID, and handler")
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	job, err := w.Store.Claim(ctx, w.WorkerID, now())
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	heartbeatInterval := w.LeaseHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultLeaseHeartbeatInterval
	}
	handlerContext, cancelHandler := context.WithCancel(ctx)
	heartbeatContext, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHandler()
	defer cancelHeartbeat()
	heartbeatResult := make(chan error, 1)
	go w.renewLeaseUntilDone(heartbeatContext, cancelHandler, *job, now, heartbeatInterval, heartbeatResult)

	handlerErr := w.Handler.Handle(handlerContext, *job)
	cancelHeartbeat()
	renewalErr := <-heartbeatResult
	cancelHandler()
	if renewalErr != nil {
		// Once ownership cannot be proven, a stale worker must never complete or
		// retry the job. The database will make the expired lease claimable again.
		return true, fmt.Errorf("renew job lease: %w", renewalErr)
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if handlerErr != nil {
		return true, w.Store.Retry(ctx, job.ID, w.WorkerID, job.Attempts, now().Add(Backoff(job.Attempts)), "handler failed")
	}
	return true, w.Store.Complete(ctx, job.ID, w.WorkerID)
}

func (w Worker) renewLeaseUntilDone(
	ctx context.Context,
	cancelHandler context.CancelFunc,
	job Job,
	now func() time.Time,
	interval time.Duration,
	result chan<- error,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			if err := w.Store.RenewLease(ctx, job.ID, w.WorkerID, now()); err != nil {
				// Completing the handler cancels an in-flight renewal query. That
				// cancellation is not ownership loss; any other renewal error is.
				if contextErr := ctx.Err(); contextErr != nil && errors.Is(err, contextErr) {
					result <- nil
					return
				}
				cancelHandler()
				result <- err
				return
			}
		}
	}
}

func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Second * time.Duration(1<<(attempt-1))
}
