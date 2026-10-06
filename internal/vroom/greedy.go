package vroom

import "context"

// GreedySolver is a tiny nearest-neighbour stand-in for VROOM. It exists ONLY
// for unit tests and offline smoke runs; production must use CLI (real VROOM).
// It honours capacity, max_travel_time and the vehicle time window, and emits
// VROOM-shaped output (no start step, jobs, then an end step).
type GreedySolver struct{}

func (GreedySolver) Solve(_ context.Context, p Problem) (*Output, error) {
	m := p.Matrices[Profile].Durations
	remaining := make([]int, len(p.Jobs))
	for i := range remaining {
		remaining[i] = i
	}

	// evaluate returns (travel, elapsed) of visiting jobs in order then the college.
	evaluate := func(route []int) (int, int) {
		travel, service := 0, 0
		for i, j := range route {
			service += p.Jobs[j].Service
			if i > 0 {
				travel += m[p.Jobs[route[i-1]].LocationIndex][p.Jobs[j].LocationIndex]
			}
		}
		last := p.Jobs[route[len(route)-1]].LocationIndex
		travel += m[last][p.Vehicles[0].EndIndex]
		return travel, travel + service
	}

	out := &Output{}
	for _, v := range p.Vehicles {
		if len(remaining) == 0 {
			break
		}
		feasible := func(route []int) bool {
			if len(route) > v.Capacity[0] {
				return false
			}
			travel, elapsed := evaluate(route)
			if v.MaxTravelTime != nil && travel > *v.MaxTravelTime {
				return false
			}
			if len(v.TimeWindow) == 2 && elapsed > v.TimeWindow[1] {
				return false
			}
			return true
		}

		var route []int
		for {
			best, bestKey := -1, 0
			for ri, j := range remaining {
				cand := append(append([]int{}, route...), j)
				if !feasible(cand) {
					continue
				}
				var key int
				if len(route) == 0 {
					// Start with the job farthest from the college.
					key = -m[p.Jobs[j].LocationIndex][v.EndIndex]
				} else {
					key = m[p.Jobs[route[len(route)-1]].LocationIndex][p.Jobs[j].LocationIndex]
				}
				if best == -1 || key < bestKey {
					best, bestKey = ri, key
				}
			}
			if best == -1 {
				break
			}
			route = append(route, remaining[best])
			remaining = append(remaining[:best], remaining[best+1:]...)
		}
		if len(route) == 0 {
			continue
		}

		travel, _ := evaluate(route)
		r := Route{Vehicle: v.ID, Duration: travel}
		arrival, cum := 0, 0
		for i, j := range route {
			if i > 0 {
				d := m[p.Jobs[route[i-1]].LocationIndex][p.Jobs[j].LocationIndex]
				arrival += p.Jobs[route[i-1]].Service + d
				cum += d
			}
			r.Steps = append(r.Steps, Step{Type: "job", Job: p.Jobs[j].ID, Arrival: arrival, Duration: cum})
		}
		lastJob := p.Jobs[route[len(route)-1]]
		d := m[lastJob.LocationIndex][v.EndIndex]
		r.Steps = append(r.Steps, Step{Type: "end", Arrival: arrival + lastJob.Service + d, Duration: cum + d})
		out.Routes = append(out.Routes, r)
	}
	for _, j := range remaining {
		out.Unassigned = append(out.Unassigned, Unassigned{ID: p.Jobs[j].ID})
	}
	return out, nil
}
