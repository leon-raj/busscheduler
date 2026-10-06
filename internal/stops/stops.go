// Package stops resolves stop IDs to coordinates.
package stops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/example/busscheduler/internal/model"
)

// Store looks up stops by ID. IDs that do not exist are simply absent from the
// returned map; the caller decides how to report them.
type Store interface {
	Lookup(ctx context.Context, ids []string) (map[string]model.Point, error)
}

// Memory is an in-memory Store (tests, local runs).
type Memory map[string]model.Point

func (m Memory) Lookup(_ context.Context, ids []string) (map[string]model.Point, error) {
	out := make(map[string]model.Point, len(ids))
	for _, id := range ids {
		if p, ok := m[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

// Postgres reads the `stops` table (stop_id PK, latitude, longitude). It works
// with Neon: use the connection string from the Neon console (sslmode=require).
type Postgres struct {
	DB *sql.DB
}

func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	if dsn == "" {
		return nil, fmt.Errorf("stops: DATABASE_URL is empty")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	// Neon closes idle connections when a compute scales to zero; recycle early.
	db.SetConnMaxIdleTime(4 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	pctx, cancel := context.WithTimeout(ctx, 30*time.Second) // allows for a cold start
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("stops: cannot reach database: %w", err)
	}
	return &Postgres{DB: db}, nil
}

func (p *Postgres) Close() error { return p.DB.Close() }

func (p *Postgres) Lookup(ctx context.Context, ids []string) (map[string]model.Point, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		out, err := p.lookupOnce(ctx, ids)
		if err == nil {
			return out, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, fmt.Errorf("stops: lookup failed: %w", lastErr)
}

func (p *Postgres) lookupOnce(ctx context.Context, ids []string) (map[string]model.Point, error) {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT stop_id, latitude, longitude FROM stops WHERE stop_id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]model.Point, len(ids))
	for rows.Next() {
		var id string
		var pt model.Point
		if err := rows.Scan(&id, &pt.Lat, &pt.Lon); err != nil {
			return nil, err
		}
		out[id] = pt
	}
	return out, rows.Err()
}
