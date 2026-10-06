// Package service is the use-case layer shared by the CLI and the HTTP server:
// validate a request, resolve stops from the DB, fetch the travel matrix for
// the UNIQUE stops only, run the planner, and return the result.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/planner"
	"github.com/example/busscheduler/internal/routing"
	"github.com/example/busscheduler/internal/stops"
	"github.com/example/busscheduler/internal/vroom"
)

// ValidationError marks a problem with the caller's input (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

type Config struct {
	ServiceTimePerStudent time.Duration // boarding time per student
	BusArrivalStagger     time.Duration // gap between consecutive buses' college arrivals
	TolerancePerBus       time.Duration // binary-search precision
	MaxTunedBuses         int           // 0 = all
	TrafficAware          bool          // send arriveAt=deadline to TomTom
}

type Service struct {
	Stops   stops.Store
	Routing routing.Provider
	Solver  vroom.Solver
	Cfg     Config
	Log     *slog.Logger
}

func (s *Service) Plan(ctx context.Context, req model.Request, resultID string) (*model.Result, error) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if err := validate(req); err != nil {
		return nil, err
	}

	// ---- resolve stops (each distinct stop_id fetched once) ----
	ids := []string{req.College.StopID}
	seen := map[string]bool{req.College.StopID: true}
	for _, st := range req.Students {
		if !seen[st.StopID] {
			seen[st.StopID] = true
			ids = append(ids, st.StopID)
		}
	}
	found, err := s.Stops.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, invalid("unknown stop_id(s): %s", strings.Join(missing, ", "))
	}

	// ---- location table: 0 = college, then each distinct coordinate once ----
	// Students sharing a stop (or identical coordinates) map to the same row,
	// so TomTom only ever sees unique points.
	pts := []model.Point{found[req.College.StopID]}
	byCoord := map[model.Point]int{roundPoint(pts[0]): 0}
	stopLoc := map[string]int{req.College.StopID: 0}
	for _, id := range ids[1:] {
		pt := found[id]
		idx, ok := byCoord[roundPoint(pt)]
		if !ok {
			idx = len(pts)
			pts = append(pts, pt)
			byCoord[roundPoint(pt)] = idx
		}
		stopLoc[id] = idx
	}
	studentLoc := make([]int, len(req.Students))
	for i, st := range req.Students {
		studentLoc[i] = stopLoc[st.StopID]
	}
	log.Info("resolved stops", "students", len(req.Students), "unique_points", len(pts))

	var arriveAt *time.Time
	if s.Cfg.TrafficAware {
		arriveAt = &req.TimeWindow.ArrivalDeadline
	}
	matrix, err := s.Routing.Matrix(ctx, pts, arriveAt)
	if err != nil {
		return nil, fmt.Errorf("travel-time matrix: %w", err)
	}

	p := &planner.Planner{
		Buses: req.Buses, Students: req.Students, StudentLoc: studentLoc, Matrix: matrix,
		CollegeStopID: req.College.StopID, Window: req.TimeWindow, DesiredEmpty: req.DesiredEmptySeats,
		ServiceSeconds: int(s.Cfg.ServiceTimePerStudent.Seconds()),
		StaggerSeconds: int(s.Cfg.BusArrivalStagger.Seconds()),
		Solver:         s.Solver, ToleranceSec: int(s.Cfg.TolerancePerBus.Seconds()),
		MaxTuned: s.Cfg.MaxTunedBuses, Log: log,
	}
	run, err := p.Run(ctx)
	if err != nil {
		return nil, err
	}
	res := p.BuildResult(run)
	res.ResultID = resultID
	res.CreatedAt = time.Now().UTC()
	res.CollegeID = req.College.CollegeID
	res.CollegeStopID = req.College.StopID
	return &res, nil
}

// roundPoint snaps to ~0.1 m so float noise cannot defeat de-duplication.
func roundPoint(p model.Point) model.Point {
	r := func(v float64) float64 { return float64(int64(v*1e6+sign(v)*0.5)) / 1e6 }
	return model.Point{Lat: r(p.Lat), Lon: r(p.Lon)}
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func validate(r model.Request) error {
	if strings.TrimSpace(r.College.StopID) == "" {
		return invalid("college.stop_id is required")
	}
	if !r.TimeWindow.ArrivalDeadline.After(r.TimeWindow.EarliestDeparture) {
		return invalid("time_window.arrival_deadline must be after earliest_departure")
	}
	if r.DesiredEmptySeats < 0 {
		return invalid("desired_empty_seats cannot be negative")
	}
	if len(r.Buses) == 0 {
		return invalid("at least one bus is required")
	}
	if len(r.Students) == 0 {
		return invalid("at least one student is required")
	}
	busIDs := map[string]bool{}
	for _, b := range r.Buses {
		switch {
		case b.ID == "":
			return invalid("every bus needs an id")
		case busIDs[b.ID]:
			return invalid("duplicate bus id %q", b.ID)
		case b.Capacity < 1:
			return invalid("bus %q must have capacity >= 1", b.ID)
		}
		busIDs[b.ID] = true
	}
	stuIDs := map[string]bool{}
	for _, st := range r.Students {
		switch {
		case st.ID == "":
			return invalid("every student needs an id")
		case stuIDs[st.ID]:
			return invalid("duplicate student id %q", st.ID)
		case st.StopID == "":
			return invalid("student %q needs a stop_id", st.ID)
		}
		stuIDs[st.ID] = true
	}
	return nil
}
