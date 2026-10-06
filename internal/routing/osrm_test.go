package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/example/busscheduler/internal/model"
)

func TestOSRM_Matrix(t *testing.T) {
	d00, d01, d10, d11 := 0.0, 125.6, 124.2, 0.0
	var d02 *float64 // null -> unreachable
	d20 := -1.0      // negative -> unreachable

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("annotations") {
			t.Errorf("expected annotations query param")
		}
		res := osrmTableResponse{
			Code: "Ok",
			Durations: [][]*float64{
				{&d00, &d01, d02},
				{&d10, &d11, &d00},
				{&d20, &d01, &d00},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer server.Close()

	osrm := &OSRM{
		BaseURL:     server.URL,
		Unreachable: 99999,
	}

	pts := []model.Point{
		{Lat: 12.9716, Lon: 77.5946},
		{Lat: 12.9352, Lon: 77.6245},
		{Lat: 12.9279, Lon: 77.6271},
	}

	ctx := context.Background()
	m, err := osrm.Matrix(ctx, pts, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(m) != 3 || len(m[0]) != 3 {
		t.Fatalf("unexpected matrix dimensions: %dx%d", len(m), len(m[0]))
	}

	if m[0][1] != 126 { // 125.6 rounded
		t.Errorf("m[0][1] = %d, expected 126", m[0][1])
	}
	if m[0][2] != 99999 { // null -> unreachable
		t.Errorf("m[0][2] = %d, expected 99999", m[0][2])
	}
	if m[2][0] != 99999 { // negative -> unreachable
		t.Errorf("m[2][0] = %d, expected 99999", m[2][0])
	}
}

func TestOSRM_EmptyAndErrors(t *testing.T) {
	osrm := &OSRM{}
	m, err := osrm.Matrix(context.Background(), nil, nil)
	if err != nil || len(m) != 0 {
		t.Fatalf("expected empty matrix for empty points, got %v, err %v", m, err)
	}

	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(osrmTableResponse{Code: "InvalidQuery", Message: "Too many coordinates"})
	}))
	defer errServer.Close()

	osrm.BaseURL = errServer.URL
	_, err = osrm.Matrix(context.Background(), []model.Point{{Lat: 1.0, Lon: 1.0}}, nil)
	if err == nil {
		t.Fatal("expected error on HTTP 400 response, got nil")
	}
}
