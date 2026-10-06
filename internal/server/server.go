// Package server exposes the planner over HTTP.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/planner"
	"github.com/example/busscheduler/internal/results"
	"github.com/example/busscheduler/internal/service"
	"github.com/example/busscheduler/internal/stops"
	"github.com/example/busscheduler/internal/store"
)

type Server struct {
	Svc         *service.Service
	Store       store.Store
	Results     *results.Store
	Log         *slog.Logger
	PlanTimeout time.Duration // default 15m
	MaxBodyByte int64         // default 10 MiB

	sem chan struct{} // limits concurrent plans (VROOM is CPU-heavy)
}

func New(svc *service.Service, resStore *results.Store, log *slog.Logger, maxConcurrent int) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 2
	}
	var st store.Store
	if svc != nil {
		if s, ok := svc.Stops.(store.Store); ok {
			st = s
		} else if mem, ok := svc.Stops.(stops.Memory); ok {
			m := store.NewMemory()
			for id, pt := range mem {
				_ = m.AddStop(context.Background(), model.Stop{
					StopID:    id,
					Name:      id,
					Latitude:  pt.Lat,
					Longitude: pt.Lon,
				})
			}
			st = m
			svc.Stops = m
		}
	}
	if st == nil {
		st = store.NewMemory()
	}
	return &Server{
		Svc:     svc,
		Store:   st,
		Results: resStore,
		Log:     log,
		sem:     make(chan struct{}, maxConcurrent),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Plans
	mux.HandleFunc("POST /v1/plans", s.createPlan)
	mux.HandleFunc("POST /plans", s.createPlan)
	mux.HandleFunc("POST /v1/plan", s.createPlan)
	mux.HandleFunc("POST /plan", s.createPlan)

	mux.HandleFunc("POST /v1/plan-from-db", s.createPlanFromDB)
	mux.HandleFunc("POST /plan-from-db", s.createPlanFromDB)
	mux.HandleFunc("POST /v1/plans-from-db", s.createPlanFromDB)
	mux.HandleFunc("POST /plans-from-db", s.createPlanFromDB)
	mux.HandleFunc("POST /v1/plan_from_db", s.createPlanFromDB)
	mux.HandleFunc("POST /plan_from_db", s.createPlanFromDB)

	mux.HandleFunc("PUT /v1/plans/{id}", s.updatePlan)
	mux.HandleFunc("PUT /plans/{id}", s.updatePlan)
	mux.HandleFunc("PUT /v1/plan/{id}", s.updatePlan)
	mux.HandleFunc("PUT /plan/{id}", s.updatePlan)

	mux.HandleFunc("POST /v1/rollback/{id}", s.rollbackPlan)
	mux.HandleFunc("POST /rollback/{id}", s.rollbackPlan)
	mux.HandleFunc("POST /v1/rollback", s.rollbackPlan)
	mux.HandleFunc("POST /rollback", s.rollbackPlan)

	mux.HandleFunc("DELETE /v1/plans", s.deletePlans)
	mux.HandleFunc("DELETE /plans", s.deletePlans)
	mux.HandleFunc("DELETE /v1/plan", s.deletePlans)
	mux.HandleFunc("DELETE /plan", s.deletePlans)

	mux.HandleFunc("GET /v1/plans/{id}", s.getPlan)
	mux.HandleFunc("GET /plans/{id}", s.getPlan)
	mux.HandleFunc("GET /v1/plan/{id}", s.getPlan)
	mux.HandleFunc("GET /plan/{id}", s.getPlan)
	mux.HandleFunc("GET /v1/plans", s.getPlan)
	mux.HandleFunc("GET /plans", s.getPlan)
	mux.HandleFunc("GET /v1/plan", s.getPlan)
	mux.HandleFunc("GET /plan", s.getPlan)

	// Buses
	mux.HandleFunc("PUT /v1/add-bus", s.addBus)
	mux.HandleFunc("PUT /add-bus", s.addBus)
	mux.HandleFunc("PUT /v1/add_bus", s.addBus)
	mux.HandleFunc("PUT /add_bus", s.addBus)
	mux.HandleFunc("PUT /v1/bus", s.addBus)
	mux.HandleFunc("PUT /bus", s.addBus)
	mux.HandleFunc("PUT /v1/buses", s.addBus)
	mux.HandleFunc("PUT /buses", s.addBus)
	mux.HandleFunc("POST /add-bus", s.addBus)

	mux.HandleFunc("PUT /v1/bus_from_csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /bus_from_csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /v1/bus-from-csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /bus-from-csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /v1/buses_from_csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /buses_from_csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /v1/buses-from-csv", s.addBusFromCSV)
	mux.HandleFunc("PUT /buses-from-csv", s.addBusFromCSV)
	mux.HandleFunc("POST /bus_from_csv", s.addBusFromCSV)

	mux.HandleFunc("DELETE /v1/bus/{id}", s.deleteBus)
	mux.HandleFunc("DELETE /bus/{id}", s.deleteBus)
	mux.HandleFunc("DELETE /v1/buses/{id}", s.deleteBus)
	mux.HandleFunc("DELETE /buses/{id}", s.deleteBus)
	mux.HandleFunc("DELETE /v1/bus", s.deleteBus)
	mux.HandleFunc("DELETE /bus", s.deleteBus)
	mux.HandleFunc("DELETE /v1/buses", s.deleteBus)
	mux.HandleFunc("DELETE /buses", s.deleteBus)

	// Students
	mux.HandleFunc("PUT /v1/add-student", s.addStudent)
	mux.HandleFunc("PUT /add-student", s.addStudent)
	mux.HandleFunc("PUT /v1/add_student", s.addStudent)
	mux.HandleFunc("PUT /add_student", s.addStudent)
	mux.HandleFunc("PUT /v1/student", s.addStudent)
	mux.HandleFunc("PUT /student", s.addStudent)
	mux.HandleFunc("PUT /v1/students", s.addStudent)
	mux.HandleFunc("PUT /students", s.addStudent)
	mux.HandleFunc("POST /add-student", s.addStudent)

	mux.HandleFunc("PUT /v1/student_from_csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /student_from_csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /v1/student-from-csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /student-from-csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /v1/students_from_csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /students_from_csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /v1/students-from-csv", s.addStudentFromCSV)
	mux.HandleFunc("PUT /students-from-csv", s.addStudentFromCSV)
	mux.HandleFunc("POST /student_from_csv", s.addStudentFromCSV)

	mux.HandleFunc("DELETE /v1/student/{id}", s.deleteStudent)
	mux.HandleFunc("DELETE /student/{id}", s.deleteStudent)
	mux.HandleFunc("DELETE /v1/students/{id}", s.deleteStudent)
	mux.HandleFunc("DELETE /students/{id}", s.deleteStudent)
	mux.HandleFunc("DELETE /v1/student", s.deleteStudent)
	mux.HandleFunc("DELETE /student", s.deleteStudent)
	mux.HandleFunc("DELETE /v1/students", s.deleteStudent)
	mux.HandleFunc("DELETE /students", s.deleteStudent)

	// Stops
	mux.HandleFunc("PUT /v1/add-stop", s.addStop)
	mux.HandleFunc("PUT /add-stop", s.addStop)
	mux.HandleFunc("PUT /v1/add_stop", s.addStop)
	mux.HandleFunc("PUT /add_stop", s.addStop)
	mux.HandleFunc("PUT /v1/stop", s.addStop)
	mux.HandleFunc("PUT /stop", s.addStop)
	mux.HandleFunc("PUT /v1/stops", s.addStop)
	mux.HandleFunc("PUT /stops", s.addStop)
	mux.HandleFunc("POST /add-stop", s.addStop)

	mux.HandleFunc("PUT /v1/stop_from_csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /stop_from_csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /v1/stop-from-csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /stop-from-csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /v1/stops_from_csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /stops_from_csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /v1/stops-from-csv", s.addStopFromCSV)
	mux.HandleFunc("PUT /stops-from-csv", s.addStopFromCSV)
	mux.HandleFunc("POST /stop_from_csv", s.addStopFromCSV)

	mux.HandleFunc("DELETE /v1/stop/{id}", s.deleteStop)
	mux.HandleFunc("DELETE /stop/{id}", s.deleteStop)
	mux.HandleFunc("DELETE /v1/stops/{id}", s.deleteStop)
	mux.HandleFunc("DELETE /stops/{id}", s.deleteStop)
	mux.HandleFunc("DELETE /v1/stop", s.deleteStop)
	mux.HandleFunc("DELETE /stop", s.deleteStop)
	mux.HandleFunc("DELETE /v1/stops", s.deleteStop)
	mux.HandleFunc("DELETE /stops", s.deleteStop)

	// List (read) endpoints for the frontend management dashboard.
	mux.HandleFunc("GET /v1/buses", s.listBuses)
	mux.HandleFunc("GET /buses", s.listBuses)
	mux.HandleFunc("GET /v1/stops", s.listStops)
	mux.HandleFunc("GET /stops", s.listStops)
	mux.HandleFunc("GET /v1/students", s.listStudents)
	mux.HandleFunc("GET /students", s.listStudents)

	// Health
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return jwtMiddleware(mux)
}

// ---------------- Plan Handlers ----------------

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	limit := s.MaxBodyByte
	if limit <= 0 {
		limit = 10 << 20
	}
	var req model.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return
	}

	id := results.NewID()
	s.Results.Reserve(id)

	if r.URL.Query().Get("async") == "true" {
		go func() {
			res, err := s.execute(context.Background(), id, req)
			if err == nil && res != nil && s.Store != nil {
				_ = s.Store.SavePlan(context.Background(), res)
			}
		}()
		w.Header().Set("Location", "/v1/plans/"+id)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"result_id": id, "plan_id": id, "status": string(results.Pending), "status_url": "/v1/plans/" + id,
		})
		return
	}

	res, err := s.execute(r.Context(), id, req)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if s.Store != nil {
		if err := s.Store.SavePlan(r.Context(), res); err != nil {
			s.Log.Error("saving plan to store failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) createPlanFromDB(w http.ResponseWriter, r *http.Request) {
	limit := s.MaxBodyByte
	if limit <= 0 {
		limit = 10 << 20
	}
	var req model.Request
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req)
	}

	if s.Store == nil {
		writeJSON(w, http.StatusBadRequest, errBody("database store not configured"))
		return
	}

	buses, err := s.Store.GetAllBuses(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if len(buses) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("no buses found in database"))
		return
	}
	req.Buses = buses

	students, err := s.Store.GetAllStudents(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if len(students) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("no students found in database"))
		return
	}
	req.Students = students

	if strings.TrimSpace(req.College.StopID) == "" {
		allStops, err := s.Store.GetAllStops(r.Context())
		if err != nil || len(allStops) == 0 {
			writeJSON(w, http.StatusBadRequest, errBody("no college stop configured in database"))
			return
		}
		found := false
		for _, st := range allStops {
			if strings.Contains(strings.ToLower(st.Name), "college") || strings.Contains(strings.ToLower(st.StopID), "college") || st.StopID == "st20" {
				req.College.StopID = st.StopID
				req.College.CollegeID = "college-1"
				found = true
				break
			}
		}
		if !found {
			req.College.StopID = allStops[0].StopID
			req.College.CollegeID = "college-1"
		}
	}

	if req.TimeWindow.EarliestDeparture.IsZero() || req.TimeWindow.ArrivalDeadline.IsZero() {
		now := time.Now()
		loc := time.Local
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		req.TimeWindow.EarliestDeparture = today.Add(6*time.Hour + 30*time.Minute)
		req.TimeWindow.ArrivalDeadline = today.Add(8*time.Hour + 45*time.Minute)
	}

	id := results.NewID()
	s.Results.Reserve(id)

	if r.URL.Query().Get("async") == "true" {
		go func() {
			res, err := s.execute(context.Background(), id, req)
			if err == nil && res != nil && s.Store != nil {
				_ = s.Store.SavePlan(context.Background(), res)
			}
		}()
		w.Header().Set("Location", "/v1/plans/"+id)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"result_id": id, "plan_id": id, "status": string(results.Pending), "status_url": "/v1/plans/" + id,
		})
		return
	}

	res, err := s.execute(r.Context(), id, req)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if s.Store != nil {
		if err := s.Store.SavePlan(r.Context(), res); err != nil {
			s.Log.Error("saving plan to store failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errBody("missing plan id"))
		return
	}

	limit := s.MaxBodyByte
	if limit <= 0 {
		limit = 10 << 20
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	// Try unmarshaling as model.Result first
	var result model.Result
	if err := json.Unmarshal(data, &result); err == nil && len(result.Buses) > 0 && len(result.Students) > 0 && result.Students[0].BusID != "" {
		result.ResultID = id
		result.PlanID = id
		if s.Store != nil {
			if err := s.Store.UpdatePlan(r.Context(), &result); err != nil {
				s.writeError(w, err)
				return
			}
		}
		s.Results.Complete(id, &result)
		writeJSON(w, http.StatusOK, &result)
		return
	}

	// Try unmarshaling as model.Request
	var req model.Request
	if err := json.Unmarshal(data, &req); err == nil && (len(req.Buses) > 0 || len(req.Students) > 0) {
		res, err := s.execute(r.Context(), id, req)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if s.Store != nil {
			if err := s.Store.UpdatePlan(r.Context(), res); err != nil {
				s.writeError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
}

func (s *Server) rollbackPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" && r.Body != nil {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if v, ok := b["id"]; ok {
			id = v
		} else if v, ok := b["plan_id"]; ok {
			id = v
		}
	}

	if s.Store == nil {
		writeJSON(w, http.StatusBadRequest, errBody("database store not configured"))
		return
	}

	res, err := s.Store.Rollback(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errBody("plan not found"))
			return
		}
		if errors.Is(err, store.ErrNoPreviousPlan) {
			writeJSON(w, http.StatusBadRequest, errBody("no previous version to rollback to"))
			return
		}
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) deletePlans(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "deleted": 0})
		return
	}
	deleted, err := s.Store.DeleteOldPlans(r.Context(), 3)
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "deleted": deleted})
}

func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}

	if id != "" {
		if s.Store != nil {
			if plan, err := s.Store.GetPlan(r.Context(), id); err == nil {
				writeJSON(w, http.StatusOK, plan)
				return
			}
		}

		if e, ok := s.Results.Get(id); ok {
			switch e.Status {
			case results.Pending:
				writeJSON(w, http.StatusAccepted, map[string]string{"result_id": e.ID, "status": string(e.Status)})
				return
			case results.Completed:
				if s.Store == nil {
					writeJSON(w, http.StatusOK, e.Result)
					return
				}
			}
		}
		writeJSON(w, http.StatusNotFound, errBody("unknown or expired result_id"))
		return
	}

	// No ID provided -> return current active plan
	if s.Store != nil {
		if plan, err := s.Store.GetCurrentPlan(r.Context()); err == nil {
			writeJSON(w, http.StatusOK, plan)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errBody("no active plan found"))
}

// ---------------- Bus Handlers ----------------

func (s *Server) addBus(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return
	}
	var busID string
	var capacity int

	for k, v := range raw {
		kl := strings.ToLower(k)
		switch kl {
		case "busid", "bus_id", "id":
			if str, ok := v.(string); ok {
				busID = str
			}
		case "capacity", "cap":
			if num, ok := v.(float64); ok {
				capacity = int(num)
			}
		}
	}
	if busID == "" {
		writeJSON(w, http.StatusBadRequest, errBody("bus_id is required"))
		return
	}
	if capacity <= 0 {
		capacity = 1
	}

	if err := s.Store.AddBus(r.Context(), model.Bus{ID: busID, Capacity: capacity}); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "bus_id": busID, "capacity": capacity})
}

func (s *Server) addBusFromCSV(w http.ResponseWriter, r *http.Request) {
	count, err := s.Store.AddBusesFromCSV(r.Context(), r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": count})
}

func (s *Server) deleteBus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		id = r.URL.Query().Get("bus_id")
	}
	if id == "" && r.Body != nil {
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if v, ok := raw["bus_id"]; ok {
			id = v
		} else if v, ok := raw["id"]; ok {
			id = v
		} else if v, ok := raw["busid"]; ok {
			id = v
		}
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errBody("bus_id is required"))
		return
	}
	if err := s.Store.DeleteBus(r.Context(), id); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "bus deleted", "bus_id": id})
}

// ---------------- Student Handlers ----------------

func (s *Server) addStudent(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return
	}
	var studentID string
	var stopID string

	for k, v := range raw {
		kl := strings.ToLower(k)
		switch kl {
		case "student_id", "studentid", "id":
			if str, ok := v.(string); ok {
				studentID = str
			}
		case "stop_id", "stopid":
			if str, ok := v.(string); ok {
				stopID = str
			}
		}
	}
	if studentID == "" {
		writeJSON(w, http.StatusBadRequest, errBody("student_id is required"))
		return
	}
	if stopID == "" {
		writeJSON(w, http.StatusBadRequest, errBody("stop_id is required"))
		return
	}

	if err := s.Store.AddStudent(r.Context(), model.StudentRef{ID: studentID, StopID: stopID}); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "student_id": studentID, "stop_id": stopID})
}

func (s *Server) addStudentFromCSV(w http.ResponseWriter, r *http.Request) {
	count, err := s.Store.AddStudentsFromCSV(r.Context(), r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": count})
}

func (s *Server) deleteStudent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		id = r.URL.Query().Get("student_id")
	}
	if id == "" && r.Body != nil {
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if v, ok := raw["student_id"]; ok {
			id = v
		} else if v, ok := raw["id"]; ok {
			id = v
		} else if v, ok := raw["studentid"]; ok {
			id = v
		}
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errBody("student_id is required"))
		return
	}
	if err := s.Store.DeleteStudent(r.Context(), id); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "student deleted", "student_id": id})
}

// ---------------- Stop Handlers ----------------

func (s *Server) addStop(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid JSON body: "+err.Error()))
		return
	}
	var stopID string
	var name string
	var lat, lon float64
	var hasLat, hasLon bool

	for k, v := range raw {
		kl := strings.ToLower(k)
		switch kl {
		case "stop_id", "stop-id", "stopid", "id":
			if str, ok := v.(string); ok {
				stopID = str
			}
		case "name":
			if str, ok := v.(string); ok {
				name = str
			}
		case "latitude", "lattitude", "lat":
			if num, ok := v.(float64); ok {
				lat = num
				hasLat = true
			}
		case "longitude", "long", "lon", "lng":
			if num, ok := v.(float64); ok {
				lon = num
				hasLon = true
			}
		}
	}
	if stopID == "" {
		writeJSON(w, http.StatusBadRequest, errBody("stop_id is required"))
		return
	}
	if !hasLat || !hasLon {
		writeJSON(w, http.StatusBadRequest, errBody("latitude and longitude are required"))
		return
	}
	if name == "" {
		name = stopID
	}

	stop := model.Stop{
		StopID:    stopID,
		Name:      name,
		Latitude:  lat,
		Longitude: lon,
	}
	if err := s.Store.AddStop(r.Context(), stop); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"stop_id":   stopID,
		"name":      name,
		"latitude":  lat,
		"longitude": lon,
	})
}

func (s *Server) addStopFromCSV(w http.ResponseWriter, r *http.Request) {
	count, err := s.Store.AddStopsFromCSV(r.Context(), r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": count})
}

func (s *Server) deleteStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		id = r.URL.Query().Get("stop_id")
	}
	if id == "" && r.Body != nil {
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if v, ok := raw["stop_id"]; ok {
			id = v
		} else if v, ok := raw["id"]; ok {
			id = v
		} else if v, ok := raw["stopid"]; ok {
			id = v
		}
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errBody("stop_id is required"))
		return
	}
	if err := s.Store.DeleteStop(r.Context(), id); err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "stop deleted", "stop_id": id})
}

// ---------------- List Handlers ----------------

func (s *Server) listBuses(w http.ResponseWriter, r *http.Request) {
	buses, err := s.Store.GetAllBuses(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if buses == nil {
		buses = []model.Bus{}
	}
	writeJSON(w, http.StatusOK, buses)
}

func (s *Server) listStops(w http.ResponseWriter, r *http.Request) {
	stps, err := s.Store.GetAllStops(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if stps == nil {
		stps = []model.Stop{}
	}
	writeJSON(w, http.StatusOK, stps)
}

func (s *Server) listStudents(w http.ResponseWriter, r *http.Request) {
	students, err := s.Store.GetAllStudents(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if students == nil {
		students = []model.StudentRef{}
	}
	writeJSON(w, http.StatusOK, students)
}

// ---------------- Helper execution ----------------

// execute waits for a free slot, runs the plan with a timeout and records the
// outcome under id.
func (s *Server) execute(ctx context.Context, id string, req model.Request) (*model.Result, error) {
	timeout := s.PlanTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.Results.Fail(id, ctx.Err())
		return nil, ctx.Err()
	}

	res, err := s.Svc.Plan(ctx, req, id)
	if err != nil {
		s.Log.Error("plan failed", "result_id", id, "err", err)
		s.Results.Fail(id, publicError(err))
		return nil, err
	}
	s.Results.Complete(id, res)
	return res, nil
}

// publicError maps an error to a message that is safe to show callers.
func publicError(err error) error {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve), errors.Is(err, planner.ErrInfeasible):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("planning timed out")
	case errors.Is(err, context.Canceled):
		return errors.New("request cancelled")
	default:
		return errors.New("internal error")
	}
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	var ve *service.ValidationError
	code := http.StatusInternalServerError
	switch {
	case errors.As(err, &ve):
		code = http.StatusBadRequest
	case errors.Is(err, planner.ErrInfeasible):
		code = http.StatusUnprocessableEntity
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		code = 499
	}
	writeJSON(w, code, errBody(publicError(err).Error()))
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
