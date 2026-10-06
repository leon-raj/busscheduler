// Package routing provides travel-time matrices between points.
package routing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/example/busscheduler/internal/model"
)

// Provider returns a square matrix of travel times in seconds between pts.
// arriveAt, when non-nil, asks for traffic-aware times for that arrival time.
type Provider interface {
	Matrix(ctx context.Context, pts []model.Point, arriveAt *time.Time) ([][]int, error)
}

// ---------------- Cache ----------------

// Cached wraps a Provider with an in-memory cache and, if Dir is set, an
// on-disk cache, so identical point sets never hit the paid API twice.
type Cached struct {
	Inner Provider
	Tag   string // distinguishes providers (e.g. "tomtom" vs "fake") in the key
	Dir   string
	Max   int // max in-memory entries (default 64)

	mu  sync.Mutex
	mem map[string][][]int
}

func (c *Cached) Matrix(ctx context.Context, pts []model.Point, arriveAt *time.Time) ([][]int, error) {
	key := cacheKey(c.Tag, pts, arriveAt)

	c.mu.Lock()
	if m, ok := c.mem[key]; ok {
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	if c.Dir != "" {
		if b, err := os.ReadFile(filepath.Join(c.Dir, key+".json")); err == nil {
			var m [][]int
			if json.Unmarshal(b, &m) == nil && len(m) == len(pts) {
				c.remember(key, m)
				return m, nil
			}
		}
	}

	m, err := c.Inner.Matrix(ctx, pts, arriveAt)
	if err != nil {
		return nil, err
	}
	c.remember(key, m)
	if c.Dir != "" {
		if err := os.MkdirAll(c.Dir, 0o755); err == nil {
			if b, err := json.Marshal(m); err == nil {
				_ = os.WriteFile(filepath.Join(c.Dir, key+".json"), b, 0o644)
			}
		}
	}
	return m, nil
}

func (c *Cached) remember(key string, m [][]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		c.mem = map[string][][]int{}
	}
	limit := c.Max
	if limit <= 0 {
		limit = 64
	}
	if len(c.mem) >= limit {
		for k := range c.mem { // evict an arbitrary entry
			delete(c.mem, k)
			break
		}
	}
	c.mem[key] = m
}

func cacheKey(tag string, pts []model.Point, arriveAt *time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|", tag)
	for _, p := range pts {
		fmt.Fprintf(h, "%.6f,%.6f;", p.Lat, p.Lon)
	}
	if arriveAt != nil {
		fmt.Fprintf(h, "|%s", arriveAt.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

// ---------------- Offline provider ----------------

// Haversine is an offline stand-in for TomTom: straight-line distance scaled
// by a detour factor at a constant speed. For local testing only.
type Haversine struct {
	SpeedKmh float64 // default 25
	Detour   float64 // default 1.3
}

func (h Haversine) Matrix(_ context.Context, pts []model.Point, _ *time.Time) ([][]int, error) {
	speed, detour := h.SpeedKmh, h.Detour
	if speed <= 0 {
		speed = 25
	}
	if detour <= 0 {
		detour = 1.3
	}
	m := make([][]int, len(pts))
	for i := range m {
		m[i] = make([]int, len(pts))
		for j := range m[i] {
			if i != j {
				km := haversineKm(pts[i], pts[j]) * detour
				m[i][j] = int(math.Round(km / speed * 3600))
			}
		}
	}
	return m, nil
}

func haversineKm(a, b model.Point) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (b.Lat-a.Lat)*rad, (b.Lon-a.Lon)*rad
	x := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(x))
}
