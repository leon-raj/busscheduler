// Package model holds the request/response types shared across the scheduler.
package model

import "time"

// Point is a WGS84 coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Stop represents a bus stop.
type Stop struct {
	StopID    string  `json:"stop_id"`
	Name      string  `json:"name,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// ---------------- Request ----------------

type Request struct {
	College           CollegeRef   `json:"college"`
	TimeWindow        TimeWindow   `json:"time_window"`
	DesiredEmptySeats int          `json:"desired_empty_seats"`
	Buses             []Bus        `json:"buses"`
	Students          []StudentRef `json:"students"`
}

type CollegeRef struct {
	CollegeID string `json:"college_id"`
	StopID    string `json:"stop_id"`
}

// TimeWindow: buses must not leave before EarliestDeparture and must reach the
// college by ArrivalDeadline.
type TimeWindow struct {
	EarliestDeparture time.Time `json:"earliest_departure"`
	ArrivalDeadline   time.Time `json:"arrival_deadline"`
}

func (w TimeWindow) Seconds() int { return int(w.ArrivalDeadline.Sub(w.EarliestDeparture).Seconds()) }

type Bus struct {
	ID       string `json:"id"`
	Capacity int    `json:"capacity"`
}

type StudentRef struct {
	ID     string `json:"id"`
	StopID string `json:"stop_id"`
}

// ---------------- Response ----------------

type Result struct {
	ResultID      string              `json:"result_id"`
	PlanID        string              `json:"plan_id,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	CollegeID     string              `json:"college_id"`
	CollegeStopID string              `json:"college_stop_id"`
	Students      []StudentAssignment `json:"students"`
	Buses         []BusRoute          `json:"buses"`
	Summary       Summary             `json:"summary"`
}

type StudentAssignment struct {
	StudentID    string    `json:"student_id"`
	BusID        string    `json:"bus_id"`
	PickupStopID string    `json:"pickup_stop_id"`
	PickupTime   time.Time `json:"pickup_time"`
}

type BusRoute struct {
	BusID        string `json:"bus_id"`
	Capacity     int    `json:"capacity"`
	StudentCount int    `json:"student_count"`
	EmptySeats   int    `json:"empty_seats"`
	// StartStopID and the two times are omitted for a bus that carries nobody.
	StartStopID string     `json:"start_stop_id,omitempty"`
	DropStopID  string     `json:"drop_stop_id"`
	StartTime   *time.Time `json:"start_time,omitempty"`
	// CollegeArrivalTime is staggered per bus (see bus arrival stagger).
	CollegeArrivalTime *time.Time `json:"college_arrival_time,omitempty"`
	// TotalTravelSeconds = start -> college arrival, including boarding time.
	TotalTravelSeconds int         `json:"total_travel_time_seconds"`
	Stops              []RouteStop `json:"stops"`
}

type RouteStop struct {
	Order      int       `json:"order"`
	StopID     string    `json:"stop_id"`
	PickupTime time.Time `json:"pickup_time"`
	StudentIDs []string  `json:"student_ids"`
}

type Summary struct {
	BusesUsed                int `json:"buses_used"`
	EmptySeatBufferRequested int `json:"empty_seat_buffer_requested"`
	EmptySeatBufferEnforced  int `json:"empty_seat_buffer_enforced"`
	TotalStudentRideSeconds  int `json:"total_student_ride_seconds"`
	SolverRuns               int `json:"solver_runs"`
}
