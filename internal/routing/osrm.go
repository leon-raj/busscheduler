package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/example/busscheduler/internal/model"
)

// OSRM computes travel-time matrices via an OSRM backend's Table API (/table/v1/driving).
type OSRM struct {
	BaseURL     string       // default: "http://localhost:5000"
	HTTP        *http.Client // default: http.Client with 30s timeout
	Unreachable int          // travel time in seconds for unreachable pairs (default: 7 days)
}

type osrmTableResponse struct {
	Code      string       `json:"code"`
	Durations [][]*float64 `json:"durations"`
	Message   string       `json:"message,omitempty"`
}

func (o *OSRM) defaults() (string, *http.Client, int) {
	baseURL := strings.TrimRight(o.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}
	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	unreachable := o.Unreachable
	if unreachable <= 0 {
		unreachable = 7 * 24 * 3600
	}
	return baseURL, client, unreachable
}

func (o *OSRM) Matrix(ctx context.Context, pts []model.Point, _ *time.Time) ([][]int, error) {
	if len(pts) == 0 {
		return [][]int{}, nil
	}
	baseURL, client, unreachable := o.defaults()

	coords := make([]string, len(pts))
	for i, p := range pts {
		coords[i] = fmt.Sprintf("%.6f,%.6f", p.Lon, p.Lat)
	}

	url := fmt.Sprintf("%s/table/v1/driving/%s?annotations=duration", baseURL, strings.Join(coords, ";"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("osrm request build: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("osrm table request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osrm returned HTTP status %d", resp.StatusCode)
	}

	var res osrmTableResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("osrm decoding: %w", err)
	}
	if res.Code != "Ok" {
		return nil, fmt.Errorf("osrm error: %s (%s)", res.Code, res.Message)
	}
	if len(res.Durations) != len(pts) {
		return nil, fmt.Errorf("osrm returned %d rows, expected %d", len(res.Durations), len(pts))
	}

	matrix := make([][]int, len(res.Durations))
	for i, row := range res.Durations {
		if len(row) != len(pts) {
			return nil, fmt.Errorf("osrm row %d returned %d cols, expected %d", i, len(row), len(pts))
		}
		matrix[i] = make([]int, len(row))
		for j, sec := range row {
			if sec == nil || *sec < 0 {
				matrix[i][j] = unreachable
			} else {
				matrix[i][j] = int(math.Round(*sec))
			}
		}
	}

	return matrix, nil
}
