// Package tomtom is a small client for the TomTom Matrix Routing v2
// *asynchronous* API: submit -> poll status -> download result.
//
// The standard plan limits an async job to 2500 cells (max 1000 origins x 1000
// destinations), so DurationMatrix tiles the full NxN matrix into blocks and
// submits each block as its own job.
package tomtom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/example/busscheduler/internal/model"
)

const MaxCellsStandardPlan = 2500

type Client struct {
	APIKey       string
	BaseURL      string        // default https://api.tomtom.com
	HTTP         *http.Client  // default: 2 min timeout
	PollInterval time.Duration // default 2s
	JobTimeout   time.Duration // per-block, default 10 min
	BlockSize    int           // origins/destinations per block side, default 50 (50x50=2500)
	Concurrency  int           // parallel jobs, default 2
	// Unreachable is the value used for cells TomTom could not route. It should
	// be large enough that any route through it violates the time limits.
	Unreachable int // default 7 days
	Log         *slog.Logger
}

type apiPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type apiLocation struct {
	Point apiPoint `json:"point"`
}
type apiOptions struct {
	ArriveAt string `json:"arriveAt,omitempty"`
}
type matrixRequest struct {
	Origins      []apiLocation `json:"origins"`
	Destinations []apiLocation `json:"destinations"`
	Options      *apiOptions   `json:"options,omitempty"`
}
type jobResponse struct {
	JobID string `json:"jobId"`
	State string `json:"state"`
}
type resultCell struct {
	OriginIndex      int `json:"originIndex"`
	DestinationIndex int `json:"destinationIndex"`
	RouteSummary     *struct {
		LengthInMeters      int `json:"lengthInMeters"`
		TravelTimeInSeconds int `json:"travelTimeInSeconds"`
	} `json:"routeSummary"`
	DetailedError json.RawMessage `json:"detailedError"`
}
type resultResponse struct {
	Data []resultCell `json:"data"`
}

func (c *Client) defaults() {
	if c.Unreachable <= 0 {
		c.Unreachable = 7 * 24 * 3600
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.tomtom.com"
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 2 * time.Minute}
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.JobTimeout <= 0 {
		c.JobTimeout = 10 * time.Minute
	}
	if c.BlockSize <= 0 {
		c.BlockSize = 50
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 2
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// Matrix returns travel times in seconds between every pair of pts (it
// implements routing.Provider). If arriveAt is set, times are traffic-aware for
// that arrival time (must be in the future); note that sending any option lifts
// the "unlimited bounding box" allowance, so points must sit in a 400x400 km box.
// Cells TomTom could not route are filled with c.Unreachable.
func (c *Client) Matrix(ctx context.Context, pts []model.Point, arriveAt *time.Time) ([][]int, error) {
	unreachable := c.Unreachable
	c.defaults()
	unreachable = c.Unreachable
	if c.APIKey == "" {
		return nil, fmt.Errorf("tomtom: API key is empty")
	}
	if c.BlockSize*c.BlockSize > MaxCellsStandardPlan {
		c.Log.Warn("block size exceeds the 2500-cell standard-plan limit; requires an enterprise plan",
			"block", c.BlockSize)
	}
	n := len(pts)
	m := make([][]int, n)
	filled := make([][]bool, n)
	for i := range m {
		m[i] = make([]int, n)
		filled[i] = make([]bool, n)
	}

	type block struct{ o0, o1, d0, d1 int }
	var blocks []block
	for o := 0; o < n; o += c.BlockSize {
		for d := 0; d < n; d += c.BlockSize {
			blocks = append(blocks, block{o, min(o+c.BlockSize, n), d, min(d+c.BlockSize, n)})
		}
	}
	c.Log.Info("requesting duration matrix", "points", n, "jobs", len(blocks))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, c.Concurrency)
	)
	for _, b := range blocks {
		wg.Add(1)
		sem <- struct{}{}
		go func(b block) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			cells, err := c.runBlock(ctx, pts[b.o0:b.o1], pts[b.d0:b.d1], arriveAt)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("matrix block o[%d:%d] d[%d:%d]: %w", b.o0, b.o1, b.d0, b.d1, err)
					cancel()
				}
				mu.Unlock()
				return
			}
			for _, cell := range cells {
				if cell.RouteSummary == nil {
					continue
				}
				oi, di := b.o0+cell.OriginIndex, b.d0+cell.DestinationIndex
				if oi < n && di < n {
					m[oi][di] = cell.RouteSummary.TravelTimeInSeconds
					filled[oi][di] = true
				}
			}
		}(b)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	missing := 0
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			switch {
			case i == j:
				m[i][j] = 0
			case !filled[i][j]:
				m[i][j] = unreachable
				missing++
			}
		}
	}
	if missing > 0 {
		c.Log.Warn("some matrix cells could not be routed and were marked unreachable", "cells", missing)
	}
	return m, nil
}

func (c *Client) runBlock(ctx context.Context, origins, dests []model.Point, arriveAt *time.Time) ([]resultCell, error) {
	ctx, cancel := context.WithTimeout(ctx, c.JobTimeout)
	defer cancel()

	req := matrixRequest{Origins: toLocs(origins), Destinations: toLocs(dests)}
	if arriveAt != nil {
		req.Options = &apiOptions{ArriveAt: arriveAt.Format("2006-01-02T15:04:05-07:00")}
	}
	body, _ := json.Marshal(req)

	// 1. submit
	status, data, err := c.do(ctx, http.MethodPost, c.endpoint("/routing/matrix/2/async"), body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusAccepted && status != http.StatusOK {
		return nil, fmt.Errorf("submit: HTTP %d: %s", status, trim(data))
	}
	var job jobResponse
	if err := json.Unmarshal(data, &job); err != nil || job.JobID == "" {
		return nil, fmt.Errorf("submit: bad response: %s", trim(data))
	}

	// 2. poll status
	statusURL := c.endpoint("/routing/matrix/2/async/" + url.PathEscape(job.JobID))
	for {
		status, data, err = c.do(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("status: HTTP %d: %s", status, trim(data))
		}
		var st jobResponse
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("status: bad response: %s", trim(data))
		}
		if st.State == "Completed" {
			break
		}
		if st.State == "Failed" {
			return nil, fmt.Errorf("job %s failed: %s", job.JobID, trim(data))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for job %s: %w", job.JobID, ctx.Err())
		case <-time.After(c.PollInterval):
		}
	}

	// 3. download (Go's transport sends Accept-Encoding: gzip itself and
	// follows the 302 used for very large results).
	downloadURL := c.endpoint("/routing/matrix/2/async/" + url.PathEscape(job.JobID) + "/result")
	status, data, err = c.do(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d: %s", status, trim(data))
	}
	var res resultResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("download: bad JSON: %w", err)
	}
	return res.Data, nil
}

func (c *Client) endpoint(path string) string {
	q := url.Values{"key": {c.APIKey}}
	return c.BaseURL + path + "?" + q.Encode()
}

// do performs a request, retrying on network errors, 429 and 5xx with
// exponential backoff. URLs (which contain the API key) are never logged.
func (c *Client) do(ctx context.Context, method, u string, body []byte) (int, []byte, error) {
	const attempts = 5
	backoff := time.Second
	var lastErr error
	for i := 0; i < attempts; i++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return 0, nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err == nil {
			data, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil && resp.StatusCode != 429 && resp.StatusCode < 500 {
				return resp.StatusCode, data, nil
			}
			if rerr != nil {
				lastErr = rerr
			} else {
				lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, trim(data))
			}
		} else {
			// net/http errors embed the full URL, which contains the API key.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			lastErr = err
		}
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return 0, nil, fmt.Errorf("giving up after %d attempts: %w", attempts, lastErr)
}

func toLocs(pts []model.Point) []apiLocation {
	out := make([]apiLocation, len(pts))
	for i, p := range pts {
		out[i] = apiLocation{apiPoint{Latitude: p.Lat, Longitude: p.Lon}}
	}
	return out
}

func trim(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}
