package endpoint

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresCatalog struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

func NewPostgresCatalog(pool *pgxpool.Pool) *PostgresCatalog {
	return &PostgresCatalog{pool: pool, timeout: 3 * time.Second}
}

func (r *PostgresCatalog) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.timeout)
}

func (r *PostgresCatalog) ExistsByHostname(ctx context.Context, hostname string) (bool, error) {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM endpoints WHERE hostname=$1)`, hostname).Scan(&exists)
	return exists, err
}

func (r *PostgresCatalog) Ping(ctx context.Context) error {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	return r.pool.Ping(ctx)
}

func (r *PostgresCatalog) Close() { r.pool.Close() }
