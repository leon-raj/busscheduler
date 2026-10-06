package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/planner"
	"github.com/example/busscheduler/internal/routing"
	"github.com/example/busscheduler/internal/stops"
	"github.com/example/busscheduler/internal/vroom"
)

// countingProvider records how many points reached the (paid) routing API.
type countingProvider struct {
	inner routing.Provider
	sizes []int
}

func (c *countingProvider) Matrix(ctx context.Context, pts []model.Point, a *time.Time) ([][]int, error) {
	c.sizes = append(c.sizes, len(pts))
	return c.inner.Matrix(ctx, pts, a)
}

func newSvc() (*Service, *countingProvider) {
	cp := &countingProvider{inner: routing.Haversine{}}
	return &Service{
		Stops: stops.Memory{
			"st20": {Lat: 13.0067, Lon: 80.2206}, // college
			"st1":  {Lat: 12.9716, Lon: 80.2209},
			"st2":  {Lat: 12.9800, Lon: 80.2180},
			"st3":  {Lat: 13.0500, Lon: 80.2100},
			"st4":  {Lat: 12.9716, Lon: 80.2209}, // same coordinates as st1
		},
		Routing: cp,
		Solver:  vroom.GreedySolver{},
		Cfg: Config{ServiceTimePerStudent: 20 * time.Second, BusArrivalStagger: 30 * time.Second,
			TolerancePerBus: 15 * time.Second},
	}, cp
}

func req() model.Request {
	dl := time.Date(2026, 10, 1, 8, 45, 0, 0, time.FixedZone("IST", 19800))
	return model.Request{
		College:           model.CollegeRef{CollegeID: "c1", StopID: "st20"},
		TimeWindow:        model.TimeWindow{EarliestDeparture: dl.Add(-2 * time.Hour), ArrivalDeadline: dl},
		DesiredEmptySeats: 1,
		Buses:             []model.Bus{{ID: "BUS-1", Capacity: 4}, {ID: "BUS-2", Capacity: 4}},
		Students: []model.StudentRef{
			{ID: "S01", StopID: "st1"}, {ID: "S02", StopID: "st1"}, {ID: "S03", StopID: "st2"},
			{ID: "S04", StopID: "st3"}, {ID: "S05", StopID: "st4"}, {ID: "S06", StopID: "st1"},
		},
	}
}

func TestPlanEndToEnd(t *testing.T) {
	svc, cp := newSvc()
	res, err := svc.Plan(context.Background(), req(), "res_test")
	if err != nil {
		t.Fatal(err)
	}
	// college + st1/st4 (identical coords) + st2 + st3 = 4 unique points, not 7.
	if len(cp.sizes) != 1 || cp.sizes[0] != 4 {
		t.Fatalf("routing saw %v points, want exactly one call with 4", cp.sizes)
	}
	if res.ResultID != "res_test" || res.CollegeStopID != "st20" || len(res.Students) != 6 {
		t.Fatalf("bad result header: %+v", res)
	}
	inStops := map[string]int{}
	for _, b := range res.Buses {
		if b.DropStopID != "st20" {
			t.Fatalf("drop stop = %q", b.DropStopID)
		}
		for _, s := range b.Stops {
			for _, id := range s.StudentIDs {
				inStops[id]++
			}
		}
	}
	for _, s := range res.Students {
		if s.BusID == "" || inStops[s.StudentID] != 1 {
			t.Fatalf("student %s not assigned exactly once: %+v", s.StudentID, s)
		}
	}
}

func TestPlanValidation(t *testing.T) {
	svc, _ := newSvc()
	r := req()
	r.Students[0].StopID = "nope"
	_, err := svc.Plan(context.Background(), r, "x")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError for unknown stop, got %v", err)
	}
	r = req()
	r.Buses = r.Buses[:1]
	r.Buses[0].Capacity = 2
	if _, err = svc.Plan(context.Background(), r, "x"); !errors.Is(err, planner.ErrInfeasible) {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
}
