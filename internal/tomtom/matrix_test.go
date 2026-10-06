package tomtom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/busscheduler/internal/model"
)

func TestClientMatrixFlow(t *testing.T) {
	apiKey := "test-api-key"
	jobID := "job-123"

	var submitCalled, pollCalled, resultCalled bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("key"); got != apiKey {
			t.Errorf("expected key %q, got %q (URL: %s)", apiKey, got, r.URL.String())
		}

		switch r.URL.Path {
		case "/routing/matrix/2/async":
			submitCalled = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(jobResponse{JobID: jobID, State: "Submitted"})
		case "/routing/matrix/2/async/" + jobID:
			pollCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jobResponse{JobID: jobID, State: "Completed"})
		case "/routing/matrix/2/async/" + jobID + "/result":
			resultCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resultResponse{
				Data: []resultCell{
					{
						OriginIndex:      0,
						DestinationIndex: 1,
						RouteSummary: &struct {
							LengthInMeters      int `json:"lengthInMeters"`
							TravelTimeInSeconds int `json:"travelTimeInSeconds"`
						}{LengthInMeters: 1000, TravelTimeInSeconds: 120},
					},
				},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := &Client{
		APIKey:       apiKey,
		BaseURL:      ts.URL,
		HTTP:         ts.Client(),
		PollInterval: 10 * time.Millisecond,
		JobTimeout:   2 * time.Second,
	}

	pts := []model.Point{
		{Lat: 10.0, Lon: 20.0},
		{Lat: 10.1, Lon: 20.1},
	}

	matrix, err := client.Matrix(context.Background(), pts, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !submitCalled || !pollCalled || !resultCalled {
		t.Fatalf("expected all endpoints to be called; submit=%v, poll=%v, result=%v", submitCalled, pollCalled, resultCalled)
	}

	if matrix[0][1] != 120 {
		t.Errorf("expected matrix[0][1] == 120, got %d", matrix[0][1])
	}
}
