// Package jobs defines durable, claimable job contracts. PostgreSQL storage is added with its migration.
package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	KindGmailIngest Kind = "gmail_ingestion"
	KindSourceParse Kind = "source_parsing"
	KindReconcile   Kind = "reconciliation"
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

// Store implementations must claim jobs atomically in a short SQL transaction using row locks.
// They must not use LISTEN/NOTIFY, temp tables, advisory locks, or session state.
type Store interface {
	Claim(context.Context, string, time.Time) (*Job, error)
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
	if err := w.Handler.Handle(ctx, *job); err != nil {
		return true, w.Store.Retry(ctx, job.ID, w.WorkerID, job.Attempts, now().Add(Backoff(job.Attempts)), "handler failed")
	}
	return true, w.Store.Complete(ctx, job.ID, w.WorkerID)
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
