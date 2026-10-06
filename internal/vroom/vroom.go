// Package vroom wraps the VROOM command-line solver
// (https://github.com/VROOM-Project/vroom) using a custom duration matrix.
package vroom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

const Profile = "car"

// ---- Input ----

type Vehicle struct {
	ID       int    `json:"id"`
	Profile  string `json:"profile"`
	EndIndex int    `json:"end_index"` // college
	Capacity []int  `json:"capacity"`
	// No start_index: VROOM then starts the route at the first job, which is
	// exactly how each bus's start point gets chosen by the solver.
	MaxTravelTime *int `json:"max_travel_time,omitempty"`
	// TimeWindow [0, W] bounds the whole route (travel + service), which
	// max_travel_time alone does not.
	TimeWindow []int `json:"time_window,omitempty"`
}

type Job struct {
	ID            int   `json:"id"`
	LocationIndex int   `json:"location_index"`
	Service       int   `json:"service,omitempty"`
	Pickup        []int `json:"pickup"` // seat taken until the shared drop-off
}

type Matrix struct {
	Durations [][]int `json:"durations"`
}

type Problem struct {
	Vehicles []Vehicle         `json:"vehicles"`
	Jobs     []Job             `json:"jobs"`
	Matrices map[string]Matrix `json:"matrices"`
}

// ---- Output ----

type Step struct {
	Type     string `json:"type"` // start | job | end
	Job      int    `json:"job"`
	Arrival  int    `json:"arrival"`
	Duration int    `json:"duration"`
}

type Route struct {
	Vehicle  int    `json:"vehicle"`
	Duration int    `json:"duration"` // travel time only (what max_travel_time limits)
	Steps    []Step `json:"steps"`
}

type Unassigned struct {
	ID int `json:"id"`
}

type Output struct {
	Code       int          `json:"code"`
	Error      string       `json:"error"`
	Routes     []Route      `json:"routes"`
	Unassigned []Unassigned `json:"unassigned"`
}

// Solver abstracts VROOM so the planner can be tested without the binary.
type Solver interface {
	Solve(ctx context.Context, p Problem) (*Output, error)
}

// CLI runs the `vroom` executable.
type CLI struct {
	Path string   // default "vroom"
	Args []string // extra flags, e.g. ["-x", "5", "-t", "4"]
}

func (s CLI) Solve(ctx context.Context, p Problem) (*Output, error) {
	path := s.Path
	if path == "" {
		path = "vroom"
	}
	f, err := os.CreateTemp("", "vroom-in-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(p); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	args := append(append([]string{}, s.Args...), "-i", f.Name())
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// VROOM still prints a JSON error object on stdout; prefer that.
		var out Output
		if json.Unmarshal(stdout.Bytes(), &out) == nil && out.Error != "" {
			return nil, fmt.Errorf("vroom: %s", out.Error)
		}
		return nil, fmt.Errorf("vroom: %w: %s", err, stderr.String())
	}
	var out Output
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("vroom: parsing output: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("vroom: code %d: %s", out.Code, out.Error)
	}
	return &out, nil
}
