// Package planner turns "students + buses + duration matrix" into routes.
//
// VROOM minimises total cost but has no notion of "keep every bus's own
// travel time short". We steer it with the per-vehicle max_travel_time
// (MAX_TIME) constraint:
//
//  1. Solve with every bus at MAX_TIME = W (the usable window length). This
//     must seat everybody; the soft empty-seat buffer is relaxed if needed.
//  2. Binary-search the smallest uniform MAX_TIME ceiling applied to all buses
//     simultaneously. A trial is feasible iff VROOM leaves nobody unassigned.
//     This balances the load across all available buses and minimizes the maximum
//     transit duration for any bus.
//  3. No start is given to VROOM, so each bus starts at its first pickup.
//     The timeline is computed backwards from the arrival deadline, with each
//     bus arriving BusArrivalStagger earlier than the previous one, so every
//     student is picked up as late as possible.
package planner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/vroom"
)

// ErrInfeasible means the request cannot be satisfied (as opposed to a fault).
var ErrInfeasible = errors.New("infeasible")

type Planner struct {
	Buses         []model.Bus
	Students      []model.StudentRef
	StudentLoc    []int   // Matrix index of each student's stop
	Matrix        [][]int // seconds; index 0 is the college
	CollegeStopID string
	Window        model.TimeWindow
	DesiredEmpty  int // soft empty-seat buffer per bus

	ServiceSeconds int // service_time_per_student, applied to every student
	StaggerSeconds int // gap between consecutive buses' college arrivals

	Solver       vroom.Solver
	ToleranceSec int // stop bisecting when the bracket is this narrow (default 15)
	MaxTuned     int // max number of buses to tune; 0 = all active buses
	Log          *slog.Logger

	runs int
}

type solution struct {
	routes     map[int]vroom.Route // by bus index
	unassigned int
}

func (s *solution) duration(bus int) int {
	if r, ok := s.routes[bus]; ok {
		return r.Duration
	}
	return 0
}

type Result struct {
	sol    *solution
	Limits []int // final MAX_TIME per bus (seconds)
	Buffer int   // empty-seat buffer actually enforced
	Runs   int
}

// usableSeconds is the window minus the room reserved for staggering. Rank r
// arrives r*stagger before the deadline, so the worst case is (buses-1).
func (p *Planner) usableSeconds() int {
	return p.Window.Seconds() - p.StaggerSeconds*max(len(p.Buses)-1, 0)
}

func (p *Planner) Run(ctx context.Context) (*Result, error) {
	if p.Log == nil {
		p.Log = slog.Default()
	}
	if p.ToleranceSec <= 0 {
		p.ToleranceSec = 15
	}
	nb, ns := len(p.Buses), len(p.Students)
	W := p.usableSeconds()
	switch {
	case ns == 0:
		return nil, fmt.Errorf("%w: no students", ErrInfeasible)
	case nb == 0:
		return nil, fmt.Errorf("%w: no buses", ErrInfeasible)
	case W <= 0:
		return nil, fmt.Errorf("%w: time window (%ds) is too short for %d buses staggered %ds apart",
			ErrInfeasible, p.Window.Seconds(), nb, p.StaggerSeconds)
	}

	// ---- Step 1: feasibility with a soft seat buffer ----
	buf, err := p.startBuffer()
	if err != nil {
		return nil, err
	}
	limits := make([]int, nb)
	for i := range limits {
		limits[i] = W
	}

	var sol *solution
	for ; buf >= 0; buf-- {
		s, err := p.solve(ctx, W, buf, limits)
		if err != nil {
			return nil, err
		}
		if s.unassigned == 0 {
			sol = s
			break
		}
		p.Log.Warn("cannot seat everyone at this buffer, relaxing", "buffer", buf, "unassigned", s.unassigned)
	}
	if sol == nil {
		return nil, fmt.Errorf("%w: cannot serve every student within the %s window; widen it or add buses",
			ErrInfeasible, time.Duration(W)*time.Second)
	}
	if buf < p.DesiredEmpty {
		p.Log.Warn("empty-seat buffer reduced", "wanted", p.DesiredEmpty, "using", buf)
	}
	p.Log.Info("feasible baseline", "buffer", buf, "buses_used", len(sol.routes), "max_time_s", W)

	// ---- Step 2: Global minimax tuning across all buses ----
	lo := 0
	hi := W
	for hi-lo > p.ToleranceSec {
		mid := lo + (hi-lo)/2
		trial := make([]int, nb)
		for i := range trial {
			trial[i] = mid
		}

		s, err := p.solve(ctx, W, buf, trial)
		if err != nil {
			return nil, err
		}
		if s.unassigned == 0 {
			sol = s
			hi = mid
		} else {
			lo = mid
		}
	}

	for i := range limits {
		limits[i] = hi
	}
	p.Log.Info("tuned fleet global cap", "max_time_s", hi)

	return &Result{sol: sol, Limits: limits, Buffer: buf, Runs: p.runs}, nil
}

// startBuffer returns the largest buffer <= desired for which total seats
// still cover all students.
func (p *Planner) startBuffer() (int, error) {
	n := len(p.Students)
	for b := max(p.DesiredEmpty, 0); b >= 0; b-- {
		total := 0
		for _, bus := range p.Buses {
			total += max(bus.Capacity-b, 0)
		}
		if total >= n {
			return b, nil
		}
	}
	total := 0
	for _, b := range p.Buses {
		total += b.Capacity
	}
	return 0, fmt.Errorf("%w: %d students but the buses only have %d seats in total", ErrInfeasible, n, total)
}

// solve runs VROOM once. Every student is its own job (same-stop students
// share a location index; VROOM handles that natively), each with
// ServiceSeconds of service.
func (p *Planner) solve(ctx context.Context, W, buf int, limits []int) (*solution, error) {
	prob := vroom.Problem{
		Matrices: map[string]vroom.Matrix{vroom.Profile: {Durations: p.Matrix}},
	}
	for i, b := range p.Buses {
		lim := limits[i]
		prob.Vehicles = append(prob.Vehicles, vroom.Vehicle{
			ID:            i,
			Profile:       vroom.Profile,
			EndIndex:      0,
			Capacity:      []int{max(b.Capacity-buf, 0)},
			MaxTravelTime: &lim,
			TimeWindow:    []int{0, W}, // caps travel + boarding time
		})
	}
	for i := range p.Students {
		prob.Jobs = append(prob.Jobs, vroom.Job{
			ID:            i,
			LocationIndex: p.StudentLoc[i],
			Service:       p.ServiceSeconds,
			Pickup:        []int{1},
		})
	}
	out, err := p.Solver.Solve(ctx, prob)
	p.runs++
	if err != nil {
		return nil, err
	}
	s := &solution{routes: map[int]vroom.Route{}, unassigned: len(out.Unassigned)}
	for _, r := range out.Routes {
		s.routes[r.Vehicle] = r
	}
	return s, nil
}

// BuildResult converts the solution into the final plan (ResultID, CreatedAt
// and college IDs are filled in by the caller).
//
// Timeline, backwards from the deadline: the bus with the longest trip arrives
// at the deadline, the next one StaggerSeconds earlier, and so on. A stop is
// reached exactly (time from that stop to the college) before its bus's
// arrival time, i.e. as late as possible. Students at one stop share the
// stop's time.
func (p *Planner) BuildResult(r *Result) model.Result {
	stagger := time.Duration(p.StaggerSeconds) * time.Second

	elapsed := map[int]int{}
	var active []int
	for v, rt := range r.sol.routes {
		if len(rt.Steps) == 0 {
			continue
		}
		elapsed[v] = rt.Steps[len(rt.Steps)-1].Arrival - rt.Steps[0].Arrival
		active = append(active, v)
	}
	sort.Slice(active, func(i, j int) bool {
		a, b := active[i], active[j]
		if elapsed[a] != elapsed[b] {
			return elapsed[a] > elapsed[b]
		}
		return a < b
	})
	rank := map[int]int{}
	for k, v := range active {
		rank[v] = k
	}

	res := model.Result{
		Students: make([]model.StudentAssignment, len(p.Students)),
		Summary: model.Summary{
			BusesUsed:                len(active),
			EmptySeatBufferRequested: p.DesiredEmpty,
			EmptySeatBufferEnforced:  r.Buffer,
			SolverRuns:               r.Runs,
		},
	}

	for v, bus := range p.Buses {
		br := model.BusRoute{
			BusID: bus.ID, Capacity: bus.Capacity, EmptySeats: bus.Capacity,
			DropStopID: p.CollegeStopID, Stops: []model.RouteStop{},
		}
		rt, ok := r.sol.routes[v]
		if !ok || len(rt.Steps) == 0 {
			res.Buses = append(res.Buses, br)
			continue
		}

		endArr := rt.Steps[len(rt.Steps)-1].Arrival
		arrival := p.Window.ArrivalDeadline.Add(-time.Duration(rank[v]) * stagger)
		start := arrival.Add(-time.Duration(elapsed[v]) * time.Second)
		br.CollegeArrivalTime = &arrival
		br.StartTime = &start
		br.TotalTravelSeconds = elapsed[v]
		if start.Before(p.Window.EarliestDeparture) {
			p.Log.Warn("bus starts before earliest_departure", "bus", bus.ID,
				"start", start, "earliest", p.Window.EarliestDeparture)
		}

		for _, st := range rt.Steps {
			if st.Type != "job" {
				continue
			}
			stu := p.Students[st.Job]
			// Consecutive students at the same stop form one route stop.
			if n := len(br.Stops); n == 0 || br.Stops[n-1].StopID != stu.StopID {
				ride := time.Duration(endArr-st.Arrival) * time.Second
				br.Stops = append(br.Stops, model.RouteStop{
					Order: n + 1, StopID: stu.StopID, PickupTime: arrival.Add(-ride),
				})
			}
			cur := &br.Stops[len(br.Stops)-1]
			cur.StudentIDs = append(cur.StudentIDs, stu.ID)
			res.Students[st.Job] = model.StudentAssignment{
				StudentID: stu.ID, BusID: bus.ID, PickupStopID: stu.StopID, PickupTime: cur.PickupTime,
			}
			res.Summary.TotalStudentRideSeconds += int(arrival.Sub(cur.PickupTime).Seconds())
			br.StudentCount++
		}
		br.StartStopID = br.Stops[0].StopID
		br.EmptySeats = bus.Capacity - br.StudentCount
		res.Buses = append(res.Buses, br)
	}
	return res
}
