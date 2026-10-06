package planner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/vroom"
)

var deadline = time.Date(2026, 10, 1, 8, 45, 0, 0, time.FixedZone("IST", 19800))

func base(n int, buses ...model.Bus) *Planner {
	p := &Planner{
		Buses:          buses,
		Matrix:         [][]int{{0, 60}, {60, 0}}, // 0 = college, 1 = the only stop
		CollegeStopID:  "st20",
		Window:         model.TimeWindow{EarliestDeparture: deadline.Add(-time.Hour), ArrivalDeadline: deadline},
		ServiceSeconds: 10,
		StaggerSeconds: 30,
		Solver:         vroom.GreedySolver{},
	}
	for i := 0; i < n; i++ {
		p.Students = append(p.Students, model.StudentRef{ID: string(rune('a' + i)), StopID: "st1"})
		p.StudentLoc = append(p.StudentLoc, 1)
	}
	return p
}

func TestBufferRelaxedAndArrivalsStaggered(t *testing.T) {
	p := base(12, model.Bus{ID: "A", Capacity: 10}, model.Bus{ID: "B", Capacity: 10})
	p.DesiredEmpty = 5 // 12 students cannot fit with 5 spare seats per bus -> relax to 4
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Buffer != 4 {
		t.Fatalf("buffer=%d want 4", res.Buffer)
	}
	out := p.BuildResult(res)

	total := 0
	var arrivals []time.Time
	for _, b := range out.Buses {
		total += b.StudentCount
		if b.StudentCount == 0 {
			continue
		}
		if len(b.Stops) != 1 || len(b.Stops[0].StudentIDs) != b.StudentCount {
			t.Fatalf("students at the same stop must form ONE route stop: %+v", b.Stops)
		}
		arrivals = append(arrivals, *b.CollegeArrivalTime)
		if !b.StartTime.Equal(b.CollegeArrivalTime.Add(-time.Duration(b.TotalTravelSeconds) * time.Second)) {
			t.Fatalf("bad start time for %s", b.BusID)
		}
	}
	if total != 12 {
		t.Fatalf("assigned %d of 12", total)
	}
	if len(arrivals) != 2 {
		t.Fatalf("want 2 active buses, got %d", len(arrivals))
	}
	if d := arrivals[0].Sub(arrivals[1]); d != 30*time.Second && d != -30*time.Second {
		t.Fatalf("arrivals should differ by the 30s stagger, got %v", d)
	}
	for _, s := range out.Students {
		if s.BusID == "" || s.PickupStopID != "st1" || s.PickupTime.IsZero() {
			t.Fatalf("incomplete student assignment: %+v", s)
		}
	}
}

func TestInfeasible(t *testing.T) {
	p := base(5, model.Bus{ID: "A", Capacity: 2})
	if _, err := p.Run(context.Background()); !errors.Is(err, ErrInfeasible) {
		t.Fatalf("want ErrInfeasible, got %v", err)
	}
}

func TestBinarySearchShrinksBusTime(t *testing.T) {
	// Two stops far apart; a second bus can take the far stop, so tuning the
	// long bus must end with limits no larger than the initial window.
	p := base(0, model.Bus{ID: "A", Capacity: 4}, model.Bus{ID: "B", Capacity: 4})
	p.Matrix = [][]int{{0, 600, 60}, {600, 0, 640}, {60, 640, 0}}
	for i, loc := range []int{1, 1, 2, 2} {
		p.Students = append(p.Students, model.StudentRef{ID: string(rune('a' + i)), StopID: "s"})
		p.StudentLoc = append(p.StudentLoc, loc)
	}
	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, lim := range res.Limits {
		if lim > p.usableSeconds() {
			t.Fatalf("limit %d for bus %d exceeds window", lim, v)
		}
	}
	if res.Runs < 3 {
		t.Fatalf("expected binary search iterations, ran solver %d times", res.Runs)
	}
}

func TestGlobalCapTuningBalancesBuses(t *testing.T) {
	p := base(0, model.Bus{ID: "A", Capacity: 10}, model.Bus{ID: "B", Capacity: 10}, model.Bus{ID: "C", Capacity: 10})
	p.Matrix = [][]int{
		{0, 100, 200, 300},
		{100, 0, 150, 250},
		{200, 150, 0, 150},
		{300, 250, 150, 0},
	}
	for i, loc := range []int{1, 1, 2, 2, 3, 3} {
		p.Students = append(p.Students, model.StudentRef{ID: string(rune('a' + i)), StopID: "s"})
		p.StudentLoc = append(p.StudentLoc, loc)
	}

	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i < len(res.Limits); i++ {
		if res.Limits[i] != res.Limits[0] {
			t.Fatalf("expected all limits to be uniform, got %v", res.Limits)
		}
	}
	if res.Limits[0] >= p.usableSeconds() {
		t.Fatalf("expected tuned limit %d to be strictly less than usable window %d", res.Limits[0], p.usableSeconds())
	}
}
