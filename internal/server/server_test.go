package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/results"
	"github.com/example/busscheduler/internal/routing"
	"github.com/example/busscheduler/internal/service"
	"github.com/example/busscheduler/internal/stops"
	"github.com/example/busscheduler/internal/vroom"
)

func newTestServer() *httptest.Server {
	svc := &service.Service{
		Stops: stops.Memory{
			"st20": {Lat: 13.0067, Lon: 80.2206}, "st1": {Lat: 12.9716, Lon: 80.2209}, "st2": {Lat: 12.98, Lon: 80.218},
		},
		Routing: routing.Haversine{}, Solver: vroom.GreedySolver{},
		Cfg: service.Config{ServiceTimePerStudent: 20 * time.Second, BusArrivalStagger: 30 * time.Second},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(New(svc, &results.Store{}, log, 2).Handler())
}

const body = `{"college":{"college_id":"c1","stop_id":"st20"},
 "time_window":{"earliest_departure":"2026-10-01T06:30:00+05:30","arrival_deadline":"2026-10-01T08:45:00+05:30"},
 "desired_empty_seats":1,
 "buses":[{"id":"BUS-1","capacity":3},{"id":"BUS-2","capacity":3}],
 "students":[{"id":"S01","stop_id":"st1"},{"id":"S02","stop_id":"st1"},{"id":"S03","stop_id":"st2"}]}`

func post(t *testing.T, url, b string) (*http.Response, []byte) {
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func putJSON(t *testing.T, url, b string) (*http.Response, []byte) {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewBufferString(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func putCSV(t *testing.T, url, csv string) (*http.Response, []byte) {
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func deleteReq(t *testing.T, url string) (*http.Response, []byte) {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestSyncPlanThenFetchByID(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := post(t, ts.URL+"/v1/plans", body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var res model.Result
	if err := json.Unmarshal(data, &res); err != nil || res.ResultID == "" || len(res.Students) != 3 {
		t.Fatalf("bad result: %v %s", err, data)
	}

	got, err := http.Get(ts.URL + "/v1/plans/" + res.ResultID)
	if err != nil || got.StatusCode != 200 {
		t.Fatalf("GET by id: %v %v", err, got)
	}
	var again model.Result
	json.NewDecoder(got.Body).Decode(&again)
	if again.ResultID != res.ResultID {
		t.Fatalf("cached result differs")
	}

	// Fetch current plan with no ID
	cur, err := http.Get(ts.URL + "/plan")
	if err != nil || cur.StatusCode != 200 {
		t.Fatalf("GET /plan (no id): %v %v", err, cur)
	}
	var curRes model.Result
	json.NewDecoder(cur.Body).Decode(&curRes)
	if curRes.ResultID != res.ResultID {
		t.Fatalf("expected current plan %s, got %s", res.ResultID, curRes.ResultID)
	}

	nf, _ := http.Get(ts.URL + "/v1/plans/res_missing")
	if nf.StatusCode != 404 {
		t.Fatalf("unknown id -> %d", nf.StatusCode)
	}
}

func TestAsyncPlan(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp, data := post(t, ts.URL+"/v1/plans?async=true", body)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var acc map[string]string
	json.Unmarshal(data, &acc)
	for i := 0; i < 50; i++ {
		r, _ := http.Get(ts.URL + "/v1/plans/" + acc["result_id"])
		if r.StatusCode == 200 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("async result never completed")
}

func TestErrors(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	if r, _ := post(t, ts.URL+"/v1/plans", `{not json`); r.StatusCode != 400 {
		t.Fatalf("bad json -> %d", r.StatusCode)
	}
	bad := bytes.Replace([]byte(body), []byte(`"st2"`), []byte(`"ghost"`), 1)
	if r, _ := post(t, ts.URL+"/v1/plans", string(bad)); r.StatusCode != 400 {
		t.Fatalf("unknown stop -> %d", r.StatusCode)
	}
	tiny := bytes.Replace([]byte(body), []byte(`"capacity":3},{"id":"BUS-2","capacity":3`), []byte(`"capacity":1},{"id":"BUS-2","capacity":1`), 1)
	if r, _ := post(t, ts.URL+"/v1/plans", string(tiny)); r.StatusCode != 422 {
		t.Fatalf("infeasible -> %d", r.StatusCode)
	}
}

func TestBusesCRUD(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// PUT add-bus
	resp, data := putJSON(t, ts.URL+"/add-bus", `{"busid":"B1","capacity":25}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-bus failed: %d %s", resp.StatusCode, data)
	}

	// Repeated PUT add-bus (update)
	resp, data = putJSON(t, ts.URL+"/add-bus", `{"bus_id":"B1","capacity":30}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-bus repeated update failed: %d %s", resp.StatusCode, data)
	}

	// PUT bus_from_csv
	csvData := `bus_id,capacity
B2,40
B3,50`
	resp, data = putCSV(t, ts.URL+"/bus_from_csv", csvData)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /bus_from_csv failed: %d %s", resp.StatusCode, data)
	}

	// DELETE bus
	resp, data = deleteReq(t, ts.URL+"/bus/B3")
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE /bus/B3 failed: %d %s", resp.StatusCode, data)
	}
}

func TestStudentsCRUD(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// PUT add-student
	resp, data := putJSON(t, ts.URL+"/add-student", `{"student_id":"S10","stop_id":"st1"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-student failed: %d %s", resp.StatusCode, data)
	}

	// Repeated PUT add-student (update)
	resp, data = putJSON(t, ts.URL+"/add-student", `{"student_id":"S10","stop_id":"st2"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-student repeated update failed: %d %s", resp.StatusCode, data)
	}

	// PUT student_from_csv
	csvData := `student_id,stop_id
S20,st1
S30,st2`
	resp, data = putCSV(t, ts.URL+"/student_from_csv", csvData)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /student_from_csv failed: %d %s", resp.StatusCode, data)
	}

	// DELETE student
	resp, data = deleteReq(t, ts.URL+"/student/S30")
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE /student/S30 failed: %d %s", resp.StatusCode, data)
	}
}

func TestStopsCRUD(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// PUT add-stop with stop-id and lattitude spelling
	resp, data := putJSON(t, ts.URL+"/add-stop", `{"stop-id":"st100","name":"Stop 100","lattitude":12.95,"longitude":80.20}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-stop failed: %d %s", resp.StatusCode, data)
	}

	// Repeated PUT add-stop (update)
	resp, data = putJSON(t, ts.URL+"/add-stop", `{"stop_id":"st100","name":"Updated Stop 100","latitude":12.96,"longitude":80.21}`)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /add-stop repeated update failed: %d %s", resp.StatusCode, data)
	}

	// PUT stop_from_csv
	csvData := `stop_id,name,latitude,longitude
st101,Stop 101,12.94,80.19
st102,Stop 102,12.93,80.18`
	resp, data = putCSV(t, ts.URL+"/stop_from_csv", csvData)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /stop_from_csv failed: %d %s", resp.StatusCode, data)
	}

	// DELETE stop
	resp, data = deleteReq(t, ts.URL+"/stop/st102")
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE /stop/st102 failed: %d %s", resp.StatusCode, data)
	}
}

func TestPlanFromDBAndPlanUpdateAndRollbackAndDeletePlans(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// Populate DB with buses, students, stops
	putJSON(t, ts.URL+"/add-stop", `{"stop_id":"st20","name":"College Main Gate","latitude":13.0067,"longitude":80.2206}`)
	putJSON(t, ts.URL+"/add-stop", `{"stop_id":"st1","name":"Stop 1","latitude":12.9716,"longitude":80.2209}`)
	putJSON(t, ts.URL+"/add-stop", `{"stop_id":"st2","name":"Stop 2","latitude":12.98,"longitude":80.218}`)

	putJSON(t, ts.URL+"/add-bus", `{"bus_id":"BUS-1","capacity":4}`)
	putJSON(t, ts.URL+"/add-bus", `{"bus_id":"BUS-2","capacity":4}`)

	putJSON(t, ts.URL+"/add-student", `{"student_id":"S01","stop_id":"st1"}`)
	putJSON(t, ts.URL+"/add-student", `{"student_id":"S02","stop_id":"st1"}`)
	putJSON(t, ts.URL+"/add-student", `{"student_id":"S03","stop_id":"st2"}`)

	// Plan 1: via POST /plan
	resp1, data1 := post(t, ts.URL+"/plan", body)
	if resp1.StatusCode != 200 {
		t.Fatalf("POST /plan failed: %d %s", resp1.StatusCode, data1)
	}
	var p1 model.Result
	_ = json.Unmarshal(data1, &p1)

	// Plan 2: via POST /plan-from-db
	reqFromDB := `{"college":{"college_id":"c1","stop_id":"st20"},
 "time_window":{"earliest_departure":"2026-10-01T06:30:00+05:30","arrival_deadline":"2026-10-01T08:45:00+05:30"},
 "desired_empty_seats":1}`
	resp2, data2 := post(t, ts.URL+"/plan-from-db", reqFromDB)
	if resp2.StatusCode != 200 {
		t.Fatalf("POST /plan-from-db failed: %d %s", resp2.StatusCode, data2)
	}
	var p2 model.Result
	_ = json.Unmarshal(data2, &p2)

	// GET current plan (should be p2)
	curResp, err := http.Get(ts.URL + "/plan")
	if err != nil || curResp.StatusCode != 200 {
		t.Fatalf("GET /plan failed: %v", err)
	}
	defer curResp.Body.Close()
	curData, _ := io.ReadAll(curResp.Body)
	var curPlan model.Result
	_ = json.Unmarshal(curData, &curPlan)
	if curPlan.ResultID != p2.ResultID {
		t.Fatalf("expected current plan %s, got %s", p2.ResultID, curPlan.ResultID)
	}

	// PUT plan/{id} update
	p2.CollegeID = "college-updated"
	p2JSON, _ := json.Marshal(p2)
	putResp, putData := putJSON(t, ts.URL+"/plan/"+p2.ResultID, string(p2JSON))
	if putResp.StatusCode != 200 {
		t.Fatalf("PUT /plan/{id} failed: %d %s", putResp.StatusCode, putData)
	}
	var putResult model.Result
	_ = json.Unmarshal(putData, &putResult)
	if putResult.CollegeID != "college-updated" {
		t.Fatalf("expected updated college id, got %s", putResult.CollegeID)
	}

	// Plan 3: create another plan
	resp3, data3 := post(t, ts.URL+"/plan", body)
	if resp3.StatusCode != 200 {
		t.Fatalf("POST /plan 3 failed: %d %s", resp3.StatusCode, data3)
	}
	var p3 model.Result
	_ = json.Unmarshal(data3, &p3)

	// POST rollback (empty id -> rollback to previous version: p2)
	rbResp, rbData := post(t, ts.URL+"/rollback", `{}`)
	if rbResp.StatusCode != 200 {
		t.Fatalf("POST /rollback failed: %d %s", rbResp.StatusCode, rbData)
	}
	var rbPlan model.Result
	_ = json.Unmarshal(rbData, &rbPlan)
	if rbPlan.ResultID != p2.ResultID {
		t.Fatalf("expected rollback to p2 (%s), got %s", p2.ResultID, rbPlan.ResultID)
	}

	// POST rollback/{id} to p1
	rbResp2, rbData2 := post(t, ts.URL+"/rollback/"+p1.ResultID, `{}`)
	if rbResp2.StatusCode != 200 {
		t.Fatalf("POST /rollback/%s failed: %d %s", p1.ResultID, rbResp2.StatusCode, rbData2)
	}
	var rbPlan2 model.Result
	_ = json.Unmarshal(rbData2, &rbPlan2)
	if rbPlan2.ResultID != p1.ResultID {
		t.Fatalf("expected rollback to p1 (%s), got %s", p1.ResultID, rbPlan2.ResultID)
	}

	// Create 2 more plans: p4, p5
	_, data4 := post(t, ts.URL+"/plan", body)
	var p4 model.Result
	_ = json.Unmarshal(data4, &p4)

	_, data5 := post(t, ts.URL+"/plan", body)
	var p5 model.Result
	_ = json.Unmarshal(data5, &p5)

	// DELETE plans (deletes all except last 3 plans)
	delResp, delData := deleteReq(t, ts.URL+"/plans")
	if delResp.StatusCode != 200 {
		t.Fatalf("DELETE /plans failed: %d %s", delResp.StatusCode, delData)
	}

	// Oldest plans p1, p2 should be deleted (404)
	rP1, _ := http.Get(ts.URL + "/plan/" + p1.ResultID)
	if rP1.StatusCode != 404 {
		t.Fatalf("expected p1 to be deleted, got %d", rP1.StatusCode)
	}
	rP2, _ := http.Get(ts.URL + "/plan/" + p2.ResultID)
	if rP2.StatusCode != 404 {
		t.Fatalf("expected p2 to be deleted, got %d", rP2.StatusCode)
	}

	// Last 3 plans (p3, p4, p5) should still exist (200)
	rP3, _ := http.Get(ts.URL + "/plan/" + p3.ResultID)
	if rP3.StatusCode != 200 {
		t.Fatalf("expected p3 to exist, got %d", rP3.StatusCode)
	}
	rP5, _ := http.Get(ts.URL + "/plan/" + p5.ResultID)
	if rP5.StatusCode != 200 {
		t.Fatalf("expected p5 to exist, got %d", rP5.StatusCode)
	}
}
