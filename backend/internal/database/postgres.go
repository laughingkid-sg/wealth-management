// Package database configures PostgreSQL for Supabase transaction pooling.
package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenTransactionPooler disables prepared statements and session-dependent behavior.
// Supabase transaction poolers may route each query to a different backend connection.
func OpenTransactionPooler(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, fmt.Errorf("parse transaction-pooler URL: %w", err)
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MinConns = 0
	config.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open transaction-pooler: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping transaction-pooler: %w", err)
	}
	return pool, nil
}
