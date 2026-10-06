// Package store manages database persistence for stops, buses, students, and plan versions.
package store

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/stops"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrNoPreviousPlan = errors.New("no previous version to rollback to")
)

type Store interface {
	stops.Store

	// Stops
	AddStop(ctx context.Context, stop model.Stop) error
	AddStopsFromCSV(ctx context.Context, r io.Reader) (int, error)
	DeleteStop(ctx context.Context, stopID string) error
	GetAllStops(ctx context.Context) ([]model.Stop, error)

	// Buses
	AddBus(ctx context.Context, bus model.Bus) error
	AddBusesFromCSV(ctx context.Context, r io.Reader) (int, error)
	DeleteBus(ctx context.Context, busID string) error
	GetAllBuses(ctx context.Context) ([]model.Bus, error)

	// Students
	AddStudent(ctx context.Context, student model.StudentRef) error
	AddStudentsFromCSV(ctx context.Context, r io.Reader) (int, error)
	DeleteStudent(ctx context.Context, studentID string) error
	GetAllStudents(ctx context.Context) ([]model.StudentRef, error)

	// Plans & Versions
	SavePlan(ctx context.Context, plan *model.Result) error
	UpdatePlan(ctx context.Context, plan *model.Result) error
	GetPlan(ctx context.Context, planID string) (*model.Result, error)
	GetCurrentPlan(ctx context.Context) (*model.Result, error)
	Rollback(ctx context.Context, planID string) (*model.Result, error)
	DeleteOldPlans(ctx context.Context, keepCount int) (int, error)
	GetAllPlans(ctx context.Context) ([]*model.Result, error)
}

// ---------------- CSV Helpers ----------------

func parseBusesCSV(r io.Reader) ([]model.Bus, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading csv: %w", err)
	}
	var buses []model.Bus
	for i, record := range records {
		if len(record) < 2 {
			continue
		}
		id := strings.TrimSpace(record[0])
		capStr := strings.TrimSpace(record[1])
		if id == "" {
			continue
		}
		// Skip header if present
		if i == 0 && (strings.Contains(strings.ToLower(id), "bus") || strings.Contains(strings.ToLower(id), "id") || strings.ToLower(capStr) == "capacity") {
			continue
		}
		capacity, err := strconv.Atoi(capStr)
		if err != nil {
			return nil, fmt.Errorf("invalid capacity %q for bus %q: %w", capStr, id, err)
		}
		buses = append(buses, model.Bus{ID: id, Capacity: capacity})
	}
	return buses, nil
}

func parseStudentsCSV(r io.Reader) ([]model.StudentRef, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading csv: %w", err)
	}
	var students []model.StudentRef
	for i, record := range records {
		if len(record) < 2 {
			continue
		}
		id := strings.TrimSpace(record[0])
		stopID := strings.TrimSpace(record[1])
		if id == "" {
			continue
		}
		// Skip header if present
		if i == 0 && (strings.Contains(strings.ToLower(id), "student") || strings.Contains(strings.ToLower(id), "id") || strings.Contains(strings.ToLower(stopID), "stop")) {
			continue
		}
		students = append(students, model.StudentRef{ID: id, StopID: stopID})
	}
	return students, nil
}

func parseStopsCSV(r io.Reader) ([]model.Stop, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading csv: %w", err)
	}
	var stopsList []model.Stop
	for i, record := range records {
		if len(record) < 3 {
			continue
		}
		id := strings.TrimSpace(record[0])
		if id == "" {
			continue
		}
		// Check for header
		if i == 0 && (strings.Contains(strings.ToLower(id), "stop") || strings.Contains(strings.ToLower(id), "id")) {
			continue
		}
		var name string
		var latStr, lonStr string
		if len(record) >= 4 {
			name = strings.TrimSpace(record[1])
			latStr = strings.TrimSpace(record[2])
			lonStr = strings.TrimSpace(record[3])
		} else {
			name = id
			latStr = strings.TrimSpace(record[1])
			lonStr = strings.TrimSpace(record[2])
		}
		lat, err := strconv.ParseFloat(latStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid latitude %q for stop %q: %w", latStr, id, err)
		}
		lon, err := strconv.ParseFloat(lonStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid longitude %q for stop %q: %w", lonStr, id, err)
		}
		stopsList = append(stopsList, model.Stop{
			StopID:    id,
			Name:      name,
			Latitude:  lat,
			Longitude: lon,
		})
	}
	return stopsList, nil
}

// ---------------- In-Memory Store ----------------

type MemoryStore struct {
	mu                 sync.RWMutex
	stops              map[string]model.Stop
	buses              map[string]model.Bus
	students           map[string]model.StudentRef
	plans              []*model.Result
	planMap            map[string]*model.Result
	activePlanID       string
	busRoutes          map[string][]model.BusRoute
	studentAssignments map[string][]model.StudentAssignment
}

func NewMemory() *MemoryStore {
	return &MemoryStore{
		stops:              make(map[string]model.Stop),
		buses:              make(map[string]model.Bus),
		students:           make(map[string]model.StudentRef),
		plans:              make([]*model.Result, 0),
		planMap:            make(map[string]*model.Result),
		busRoutes:          make(map[string][]model.BusRoute),
		studentAssignments: make(map[string][]model.StudentAssignment),
	}
}

func (m *MemoryStore) Lookup(_ context.Context, ids []string) (map[string]model.Point, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]model.Point, len(ids))
	for _, id := range ids {
		if s, ok := m.stops[id]; ok {
			out[id] = model.Point{Lat: s.Latitude, Lon: s.Longitude}
		}
	}
	return out, nil
}

func (m *MemoryStore) AddStop(_ context.Context, stop model.Stop) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops[stop.StopID] = stop
	return nil
}

func (m *MemoryStore) AddStopsFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseStopsCSV(r)
	if err != nil {
		return 0, err
	}
	for _, s := range list {
		if err := m.AddStop(ctx, s); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (m *MemoryStore) DeleteStop(_ context.Context, stopID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stops, stopID)
	return nil
}

func (m *MemoryStore) GetAllStops(_ context.Context) ([]model.Stop, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Stop, 0, len(m.stops))
	for _, s := range m.stops {
		out = append(out, s)
	}
	return out, nil
}

func (m *MemoryStore) AddBus(_ context.Context, bus model.Bus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buses[bus.ID] = bus
	return nil
}

func (m *MemoryStore) AddBusesFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseBusesCSV(r)
	if err != nil {
		return 0, err
	}
	for _, b := range list {
		if err := m.AddBus(ctx, b); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (m *MemoryStore) DeleteBus(_ context.Context, busID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.buses, busID)
	return nil
}

func (m *MemoryStore) GetAllBuses(_ context.Context) ([]model.Bus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Bus, 0, len(m.buses))
	for _, b := range m.buses {
		out = append(out, b)
	}
	return out, nil
}

func (m *MemoryStore) AddStudent(_ context.Context, student model.StudentRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.students[student.ID] = student
	return nil
}

func (m *MemoryStore) AddStudentsFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseStudentsCSV(r)
	if err != nil {
		return 0, err
	}
	for _, st := range list {
		if err := m.AddStudent(ctx, st); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

func (m *MemoryStore) DeleteStudent(_ context.Context, studentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.students, studentID)
	return nil
}

func (m *MemoryStore) GetAllStudents(_ context.Context) ([]model.StudentRef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.StudentRef, 0, len(m.students))
	for _, st := range m.students {
		out = append(out, st)
	}
	return out, nil
}

func cloneResult(r *model.Result) *model.Result {
	if r == nil {
		return nil
	}
	data, _ := json.Marshal(r)
	var cp model.Result
	_ = json.Unmarshal(data, &cp)
	return &cp
}

func (m *MemoryStore) SavePlan(_ context.Context, plan *model.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := plan.ResultID
	if id == "" {
		id = plan.PlanID
	}
	if id == "" {
		id = fmt.Sprintf("plan_%d", time.Now().UnixNano())
		plan.ResultID = id
	}
	plan.PlanID = id

	copied := cloneResult(plan)
	// Check if already in slice
	idx := -1
	for i, p := range m.plans {
		if p.ResultID == id {
			idx = i
			break
		}
	}
	if idx >= 0 {
		m.plans[idx] = copied
	} else {
		m.plans = append(m.plans, copied)
	}
	m.planMap[id] = copied
	m.activePlanID = id
	m.busRoutes[id] = plan.Buses
	m.studentAssignments[id] = plan.Students
	return nil
}

func (m *MemoryStore) UpdatePlan(ctx context.Context, plan *model.Result) error {
	return m.SavePlan(ctx, plan)
}

func (m *MemoryStore) GetPlan(_ context.Context, planID string) (*model.Result, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.planMap[planID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneResult(p), nil
}

func (m *MemoryStore) GetCurrentPlan(_ context.Context) (*model.Result, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activePlanID != "" {
		if p, ok := m.planMap[m.activePlanID]; ok {
			return cloneResult(p), nil
		}
	}
	if len(m.plans) > 0 {
		return cloneResult(m.plans[len(m.plans)-1]), nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) Rollback(_ context.Context, planID string) (*model.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if planID != "" {
		p, ok := m.planMap[planID]
		if !ok {
			return nil, ErrNotFound
		}
		m.activePlanID = planID
		return cloneResult(p), nil
	}

	// Rollback to previous version
	if len(m.plans) < 2 {
		return nil, ErrNoPreviousPlan
	}

	// Find index of current active plan
	currentIdx := -1
	for i, p := range m.plans {
		if p.ResultID == m.activePlanID {
			currentIdx = i
			break
		}
	}
	targetIdx := len(m.plans) - 2
	if currentIdx > 0 {
		targetIdx = currentIdx - 1
	} else if currentIdx == 0 {
		return nil, ErrNoPreviousPlan
	}

	target := m.plans[targetIdx]
	m.activePlanID = target.ResultID
	return cloneResult(target), nil
}

func (m *MemoryStore) DeleteOldPlans(_ context.Context, keepCount int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if keepCount <= 0 {
		keepCount = 3
	}
	if len(m.plans) <= keepCount {
		return 0, nil
	}
	deletedCount := len(m.plans) - keepCount
	toDelete := m.plans[:deletedCount]
	m.plans = m.plans[deletedCount:]
	for _, p := range toDelete {
		delete(m.planMap, p.ResultID)
		delete(m.busRoutes, p.ResultID)
		delete(m.studentAssignments, p.ResultID)
	}
	return deletedCount, nil
}

func (m *MemoryStore) GetAllPlans(_ context.Context) ([]*model.Result, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*model.Result, len(m.plans))
	for i, p := range m.plans {
		out[i] = cloneResult(p)
	}
	return out, nil
}

// ---------------- Postgres Store ----------------

type PostgresStore struct {
	DB *sql.DB
}

func NewPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("store: DATABASE_URL is empty")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(4 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: cannot reach database: %w", err)
	}

	s := &PostgresStore{DB: db}
	if err := s.InitSchema(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: initializing schema: %w", err)
	}
	return s, nil
}

func (p *PostgresStore) Close() error {
	return p.DB.Close()
}

func (p *PostgresStore) InitSchema(ctx context.Context) error {
	schema := `
CREATE TABLE IF NOT EXISTS stops (
    stop_id   TEXT             PRIMARY KEY,
    name      TEXT,
    latitude  DOUBLE PRECISION NOT NULL CHECK (latitude  BETWEEN  -90 AND  90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180)
);

CREATE TABLE IF NOT EXISTS buses (
    bus_id   TEXT PRIMARY KEY,
    capacity INT  NOT NULL CHECK (capacity >= 1)
);

CREATE TABLE IF NOT EXISTS students (
    student_id TEXT PRIMARY KEY,
    stop_id    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plans (
    plan_id         TEXT        PRIMARY KEY,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    college_id      TEXT,
    college_stop_id TEXT,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    plan_data       JSONB       NOT NULL
);

CREATE TABLE IF NOT EXISTS bus_routes (
    id                        SERIAL PRIMARY KEY,
    plan_id                   TEXT   NOT NULL REFERENCES plans(plan_id) ON DELETE CASCADE,
    bus_id                    TEXT   NOT NULL,
    capacity                  INT    NOT NULL,
    student_count             INT    NOT NULL,
    empty_seats               INT    NOT NULL,
    start_stop_id             TEXT,
    drop_stop_id              TEXT,
    start_time                TIMESTAMPTZ,
    college_arrival_time      TIMESTAMPTZ,
    total_travel_time_seconds INT    NOT NULL,
    route_stops               JSONB  NOT NULL
);

CREATE TABLE IF NOT EXISTS student_assignments (
    id             SERIAL PRIMARY KEY,
    plan_id        TEXT        NOT NULL REFERENCES plans(plan_id) ON DELETE CASCADE,
    student_id     TEXT        NOT NULL,
    bus_id         TEXT        NOT NULL,
    pickup_stop_id TEXT        NOT NULL,
    pickup_time    TIMESTAMPTZ NOT NULL
);
`
	_, err := p.DB.ExecContext(ctx, schema)
	return err
}

func (p *PostgresStore) Lookup(ctx context.Context, ids []string) (map[string]model.Point, error) {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT stop_id, latitude, longitude FROM stops WHERE stop_id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]model.Point, len(ids))
	for rows.Next() {
		var id string
		var pt model.Point
		if err := rows.Scan(&id, &pt.Lat, &pt.Lon); err != nil {
			return nil, err
		}
		out[id] = pt
	}
	return out, rows.Err()
}

func (p *PostgresStore) AddStop(ctx context.Context, stop model.Stop) error {
	_, err := p.DB.ExecContext(ctx,
		`INSERT INTO stops (stop_id, name, latitude, longitude)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (stop_id) DO UPDATE SET
		   name = EXCLUDED.name,
		   latitude = EXCLUDED.latitude,
		   longitude = EXCLUDED.longitude`,
		stop.StopID, stop.Name, stop.Latitude, stop.Longitude)
	return err
}

func (p *PostgresStore) AddStopsFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseStopsCSV(r)
	if err != nil {
		return 0, err
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO stops (stop_id, name, latitude, longitude)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (stop_id) DO UPDATE SET
		   name = EXCLUDED.name,
		   latitude = EXCLUDED.latitude,
		   longitude = EXCLUDED.longitude`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, s := range list {
		if _, err := stmt.ExecContext(ctx, s.StopID, s.Name, s.Latitude, s.Longitude); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(list), nil
}

func (p *PostgresStore) DeleteStop(ctx context.Context, stopID string) error {
	_, err := p.DB.ExecContext(ctx, `DELETE FROM stops WHERE stop_id = $1`, stopID)
	return err
}

func (p *PostgresStore) GetAllStops(ctx context.Context) ([]model.Stop, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT stop_id, COALESCE(name, ''), latitude, longitude FROM stops ORDER BY stop_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Stop
	for rows.Next() {
		var s model.Stop
		if err := rows.Scan(&s.StopID, &s.Name, &s.Latitude, &s.Longitude); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PostgresStore) AddBus(ctx context.Context, bus model.Bus) error {
	_, err := p.DB.ExecContext(ctx,
		`INSERT INTO buses (bus_id, capacity)
		 VALUES ($1, $2)
		 ON CONFLICT (bus_id) DO UPDATE SET capacity = EXCLUDED.capacity`,
		bus.ID, bus.Capacity)
	return err
}

func (p *PostgresStore) AddBusesFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseBusesCSV(r)
	if err != nil {
		return 0, err
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO buses (bus_id, capacity)
		 VALUES ($1, $2)
		 ON CONFLICT (bus_id) DO UPDATE SET capacity = EXCLUDED.capacity`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, b := range list {
		if _, err := stmt.ExecContext(ctx, b.ID, b.Capacity); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(list), nil
}

func (p *PostgresStore) DeleteBus(ctx context.Context, busID string) error {
	_, err := p.DB.ExecContext(ctx, `DELETE FROM buses WHERE bus_id = $1`, busID)
	return err
}

func (p *PostgresStore) GetAllBuses(ctx context.Context) ([]model.Bus, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT bus_id, capacity FROM buses ORDER BY bus_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Bus
	for rows.Next() {
		var b model.Bus
		if err := rows.Scan(&b.ID, &b.Capacity); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (p *PostgresStore) AddStudent(ctx context.Context, student model.StudentRef) error {
	_, err := p.DB.ExecContext(ctx,
		`INSERT INTO students (student_id, stop_id)
		 VALUES ($1, $2)
		 ON CONFLICT (student_id) DO UPDATE SET stop_id = EXCLUDED.stop_id`,
		student.ID, student.StopID)
	return err
}

func (p *PostgresStore) AddStudentsFromCSV(ctx context.Context, r io.Reader) (int, error) {
	list, err := parseStudentsCSV(r)
	if err != nil {
		return 0, err
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO students (student_id, stop_id)
		 VALUES ($1, $2)
		 ON CONFLICT (student_id) DO UPDATE SET stop_id = EXCLUDED.stop_id`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, st := range list {
		if _, err := stmt.ExecContext(ctx, st.ID, st.StopID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(list), nil
}

func (p *PostgresStore) DeleteStudent(ctx context.Context, studentID string) error {
	_, err := p.DB.ExecContext(ctx, `DELETE FROM students WHERE student_id = $1`, studentID)
	return err
}

func (p *PostgresStore) GetAllStudents(ctx context.Context) ([]model.StudentRef, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT student_id, stop_id FROM students ORDER BY student_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.StudentRef
	for rows.Next() {
		var st model.StudentRef
		if err := rows.Scan(&st.ID, &st.StopID); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (p *PostgresStore) SavePlan(ctx context.Context, plan *model.Result) error {
	id := plan.ResultID
	if id == "" {
		id = plan.PlanID
	}
	if id == "" {
		id = fmt.Sprintf("plan_%d", time.Now().UnixNano())
		plan.ResultID = id
	}
	plan.PlanID = id

	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshaling plan: %w", err)
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Set all others inactive
	if _, err := tx.ExecContext(ctx, `UPDATE plans SET is_active = FALSE`); err != nil {
		return err
	}

	// Insert or update plan
	createdAt := plan.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO plans (plan_id, created_at, college_id, college_stop_id, is_active, plan_data)
		 VALUES ($1, $2, $3, $4, TRUE, $5)
		 ON CONFLICT (plan_id) DO UPDATE SET
		   created_at = EXCLUDED.created_at,
		   college_id = EXCLUDED.college_id,
		   college_stop_id = EXCLUDED.college_stop_id,
		   is_active = TRUE,
		   plan_data = EXCLUDED.plan_data`,
		id, createdAt, plan.CollegeID, plan.CollegeStopID, data)
	if err != nil {
		return fmt.Errorf("inserting plan: %w", err)
	}

	// Clean existing routes and assignments if updating
	if _, err := tx.ExecContext(ctx, `DELETE FROM bus_routes WHERE plan_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM student_assignments WHERE plan_id = $1`, id); err != nil {
		return err
	}

	// Insert bus routes
	routeStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO bus_routes (plan_id, bus_id, capacity, student_count, empty_seats, start_stop_id, drop_stop_id, start_time, college_arrival_time, total_travel_time_seconds, route_stops)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`)
	if err != nil {
		return err
	}
	defer routeStmt.Close()

	for _, b := range plan.Buses {
		stopsJSON, err := json.Marshal(b.Stops)
		if err != nil {
			return err
		}
		var startStopID, dropStopID *string
		if b.StartStopID != "" {
			s := b.StartStopID
			startStopID = &s
		}
		if b.DropStopID != "" {
			d := b.DropStopID
			dropStopID = &d
		}
		if _, err := routeStmt.ExecContext(ctx, id, b.BusID, b.Capacity, b.StudentCount, b.EmptySeats, startStopID, dropStopID, b.StartTime, b.CollegeArrivalTime, b.TotalTravelSeconds, stopsJSON); err != nil {
			return fmt.Errorf("inserting bus route: %w", err)
		}
	}

	// Insert student assignments
	stuStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO student_assignments (plan_id, student_id, bus_id, pickup_stop_id, pickup_time)
		 VALUES ($1, $2, $3, $4, $5)`)
	if err != nil {
		return err
	}
	defer stuStmt.Close()

	for _, st := range plan.Students {
		if _, err := stuStmt.ExecContext(ctx, id, st.StudentID, st.BusID, st.PickupStopID, st.PickupTime); err != nil {
			return fmt.Errorf("inserting student assignment: %w", err)
		}
	}

	return tx.Commit()
}

func (p *PostgresStore) UpdatePlan(ctx context.Context, plan *model.Result) error {
	return p.SavePlan(ctx, plan)
}

func (p *PostgresStore) GetPlan(ctx context.Context, planID string) (*model.Result, error) {
	var data []byte
	err := p.DB.QueryRowContext(ctx, `SELECT plan_data FROM plans WHERE plan_id = $1`, planID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var res model.Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("unmarshaling plan: %w", err)
	}
	return &res, nil
}

func (p *PostgresStore) GetCurrentPlan(ctx context.Context) (*model.Result, error) {
	var data []byte
	err := p.DB.QueryRowContext(ctx, `SELECT plan_data FROM plans WHERE is_active = TRUE ORDER BY created_at DESC LIMIT 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		// Fallback to latest
		err = p.DB.QueryRowContext(ctx, `SELECT plan_data FROM plans ORDER BY created_at DESC LIMIT 1`).Scan(&data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var res model.Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("unmarshaling plan: %w", err)
	}
	return &res, nil
}

func (p *PostgresStore) Rollback(ctx context.Context, planID string) (*model.Result, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var targetID string
	var data []byte

	if planID != "" {
		err := tx.QueryRowContext(ctx, `SELECT plan_id, plan_data FROM plans WHERE plan_id = $1`, planID).Scan(&targetID, &data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
	} else {
		// Find active plan
		var activeID string
		var activeCreatedAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT plan_id, created_at FROM plans WHERE is_active = TRUE ORDER BY created_at DESC LIMIT 1`).Scan(&activeID, &activeCreatedAt)
		if err == nil {
			// Find previous plan by created_at
			err = tx.QueryRowContext(ctx, `SELECT plan_id, plan_data FROM plans WHERE created_at < $1 ORDER BY created_at DESC LIMIT 1`, activeCreatedAt).Scan(&targetID, &data)
			if errors.Is(err, sql.ErrNoRows) {
				// Try finding by different plan_id
				err = tx.QueryRowContext(ctx, `SELECT plan_id, plan_data FROM plans WHERE plan_id != $1 ORDER BY created_at DESC LIMIT 1`, activeID).Scan(&targetID, &data)
			}
		} else {
			// Fallback to second latest
			err = tx.QueryRowContext(ctx, `SELECT plan_id, plan_data FROM plans ORDER BY created_at DESC OFFSET 1 LIMIT 1`).Scan(&targetID, &data)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoPreviousPlan
		}
		if err != nil {
			return nil, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE plans SET is_active = FALSE`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plans SET is_active = TRUE WHERE plan_id = $1`, targetID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	var res model.Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (p *PostgresStore) DeleteOldPlans(ctx context.Context, keepCount int) (int, error) {
	if keepCount <= 0 {
		keepCount = 3
	}
	res, err := p.DB.ExecContext(ctx, `
		DELETE FROM plans
		WHERE plan_id NOT IN (
			SELECT plan_id FROM plans ORDER BY created_at DESC LIMIT $1
		)
	`, keepCount)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (p *PostgresStore) GetAllPlans(ctx context.Context) ([]*model.Result, error) {
	rows, err := p.DB.QueryContext(ctx, `SELECT plan_data FROM plans ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Result
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var res model.Result
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		out = append(out, &res)
	}
	return out, rows.Err()
}
