package endpoint

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool, timeout: 3 * time.Second}
}

func (r *PostgresRepository) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.timeout)
}

func scanEndpoint(row pgx.Row) (Endpoint, error) {
	var value Endpoint
	err := row.Scan(&value.ID, &value.Name, &value.Hostname, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	return value, err
}

func (r *PostgresRepository) CreateEndpoint(ctx context.Context, value Endpoint) (Endpoint, error) {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	created, err := scanEndpoint(r.pool.QueryRow(ctx, `
		INSERT INTO endpoints (id, name, hostname, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text, name, hostname, created_at, updated_at`,
		value.ID, value.Name, value.Hostname, value.CreatedAt.UTC(), value.UpdatedAt.UTC()))
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return Endpoint{}, ErrConflict
	}
	return created, err
}

func (r *PostgresRepository) GetEndpointByName(ctx context.Context, name string) (Endpoint, error) {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	return scanEndpoint(r.pool.QueryRow(ctx, `SELECT id::text, name, hostname, created_at, updated_at FROM endpoints WHERE name=$1`, name))
}

func (r *PostgresRepository) GetEndpointByHostname(ctx context.Context, hostname string) (Endpoint, error) {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	return scanEndpoint(r.pool.QueryRow(ctx, `SELECT id::text, name, hostname, created_at, updated_at FROM endpoints WHERE hostname=$1`, hostname))
}

func (r *PostgresRepository) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT id::text, name, hostname, created_at, updated_at FROM endpoints ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Endpoint
	for rows.Next() {
		var value Endpoint
		if err := rows.Scan(&value.ID, &value.Name, &value.Hostname, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *PostgresRepository) DeleteEndpoint(ctx context.Context, name string) error {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	_, err := r.pool.Exec(ctx, `DELETE FROM endpoints WHERE name=$1`, name)
	return err
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	ctx, cancel := r.deadline(ctx)
	defer cancel()
	return r.pool.Ping(ctx)
}

func (r *PostgresRepository) Close() { r.pool.Close() }
