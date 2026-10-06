package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example/busscheduler/internal/model"
)

func TestMemoryStoreStops(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	// Add stop
	err := m.AddStop(ctx, model.Stop{
		StopID:    "st1",
		Name:      "Stop 1",
		Latitude:  12.98,
		Longitude: 80.14,
	})
	if err != nil {
		t.Fatalf("AddStop failed: %v", err)
	}

	// Repeated AddStop (update)
	err = m.AddStop(ctx, model.Stop{
		StopID:    "st1",
		Name:      "Updated Stop 1",
		Latitude:  12.99,
		Longitude: 80.15,
	})
	if err != nil {
		t.Fatalf("AddStop update failed: %v", err)
	}

	stops, err := m.GetAllStops(ctx)
	if err != nil || len(stops) != 1 || stops[0].Name != "Updated Stop 1" {
		t.Fatalf("expected 1 updated stop, got %v", stops)
	}

	// Add through CSV
	csvData := `stop_id,name,latitude,longitude
st2,Stop 2,13.04,80.13
st3,Stop 3,13.02,80.18`
	count, err := m.AddStopsFromCSV(ctx, strings.NewReader(csvData))
	if err != nil || count != 2 {
		t.Fatalf("AddStopsFromCSV failed: %v, count=%d", err, count)
	}

	stops, err = m.GetAllStops(ctx)
	if err != nil || len(stops) != 3 {
		t.Fatalf("expected 3 stops, got %d", len(stops))
	}

	// Delete stop
	err = m.DeleteStop(ctx, "st2")
	if err != nil {
		t.Fatalf("DeleteStop failed: %v", err)
	}

	lookup, err := m.Lookup(ctx, []string{"st1", "st2", "st3"})
	if err != nil || len(lookup) != 2 {
		t.Fatalf("expected 2 stops in lookup, got %v", lookup)
	}
}

func TestMemoryStoreBusesAndStudents(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	// Add bus
	err := m.AddBus(ctx, model.Bus{ID: "B1", Capacity: 10})
	if err != nil {
		t.Fatalf("AddBus failed: %v", err)
	}

	// Repeat AddBus (update)
	err = m.AddBus(ctx, model.Bus{ID: "B1", Capacity: 15})
	if err != nil {
		t.Fatalf("AddBus update failed: %v", err)
	}

	// CSV buses
	csvBuses := `bus_id,capacity
B2,20
B3,30`
	cnt, err := m.AddBusesFromCSV(ctx, strings.NewReader(csvBuses))
	if err != nil || cnt != 2 {
		t.Fatalf("AddBusesFromCSV failed: %v, count=%d", err, cnt)
	}

	buses, err := m.GetAllBuses(ctx)
	if err != nil || len(buses) != 3 {
		t.Fatalf("expected 3 buses, got %d", len(buses))
	}

	// Delete bus
	err = m.DeleteBus(ctx, "B2")
	if err != nil {
		t.Fatalf("DeleteBus failed: %v", err)
	}
	buses, _ = m.GetAllBuses(ctx)
	if len(buses) != 2 {
		t.Fatalf("expected 2 buses, got %d", len(buses))
	}

	// Add student
	err = m.AddStudent(ctx, model.StudentRef{ID: "S1", StopID: "st1"})
	if err != nil {
		t.Fatalf("AddStudent failed: %v", err)
	}

	// Repeat AddStudent (update)
	err = m.AddStudent(ctx, model.StudentRef{ID: "S1", StopID: "st2"})
	if err != nil {
		t.Fatalf("AddStudent update failed: %v", err)
	}

	// CSV students
	csvStudents := `student_id,stop_id
S2,st2
S3,st3`
	cnt, err = m.AddStudentsFromCSV(ctx, strings.NewReader(csvStudents))
	if err != nil || cnt != 2 {
		t.Fatalf("AddStudentsFromCSV failed: %v, count=%d", err, cnt)
	}

	students, err := m.GetAllStudents(ctx)
	if err != nil || len(students) != 3 {
		t.Fatalf("expected 3 students, got %d", len(students))
	}

	// Delete student
	err = m.DeleteStudent(ctx, "S2")
	if err != nil {
		t.Fatalf("DeleteStudent failed: %v", err)
	}
	students, _ = m.GetAllStudents(ctx)
	if len(students) != 2 {
		t.Fatalf("expected 2 students, got %d", len(students))
	}
}

func TestMemoryStorePlanVersionsAndRollback(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	// Save Plan 1
	p1 := &model.Result{
		ResultID:  "p1",
		PlanID:    "p1",
		CreatedAt: time.Now().Add(-10 * time.Minute),
		CollegeID: "c1",
	}
	_ = m.SavePlan(ctx, p1)

	// Save Plan 2
	p2 := &model.Result{
		ResultID:  "p2",
		PlanID:    "p2",
		CreatedAt: time.Now().Add(-5 * time.Minute),
		CollegeID: "c1",
	}
	_ = m.SavePlan(ctx, p2)

	// Save Plan 3
	p3 := &model.Result{
		ResultID:  "p3",
		PlanID:    "p3",
		CreatedAt: time.Now(),
		CollegeID: "c1",
	}
	_ = m.SavePlan(ctx, p3)

	cur, err := m.GetCurrentPlan(ctx)
	if err != nil || cur.ResultID != "p3" {
		t.Fatalf("expected current plan p3, got %v, err=%v", cur, err)
	}

	// Rollback with empty ID (should rollback to previous version: p2)
	rb, err := m.Rollback(ctx, "")
	if err != nil || rb.ResultID != "p2" {
		t.Fatalf("expected rollback to p2, got %v, err=%v", rb, err)
	}

	cur, _ = m.GetCurrentPlan(ctx)
	if cur.ResultID != "p2" {
		t.Fatalf("expected current plan p2 after rollback, got %v", cur)
	}

	// Rollback to specific ID: p1
	rb, err = m.Rollback(ctx, "p1")
	if err != nil || rb.ResultID != "p1" {
		t.Fatalf("expected rollback to p1, got %v, err=%v", rb, err)
	}

	cur, _ = m.GetCurrentPlan(ctx)
	if cur.ResultID != "p1" {
		t.Fatalf("expected current plan p1, got %v", cur)
	}

	// Save 2 more plans: p4, p5
	_ = m.SavePlan(ctx, &model.Result{ResultID: "p4", PlanID: "p4"})
	_ = m.SavePlan(ctx, &model.Result{ResultID: "p5", PlanID: "p5"})

	plans, _ := m.GetAllPlans(ctx)
	if len(plans) != 5 {
		t.Fatalf("expected 5 plans, got %d", len(plans))
	}

	// Delete old plans, keeping last 3
	deleted, err := m.DeleteOldPlans(ctx, 3)
	if err != nil || deleted != 2 {
		t.Fatalf("expected 2 deleted plans, got %d, err=%v", deleted, err)
	}

	plans, _ = m.GetAllPlans(ctx)
	if len(plans) != 3 {
		t.Fatalf("expected 3 remaining plans, got %d", len(plans))
	}
	if plans[0].ResultID != "p3" || plans[1].ResultID != "p4" || plans[2].ResultID != "p5" {
		t.Fatalf("unexpected remaining plans: %v", plans)
	}
}
