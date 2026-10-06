# busscheduler

Assigns students to buses and plans each bus's route to one college stop.
Stops come from Postgres (Neon), travel times from TomTom Matrix Routing v2 (async), optimisation by VROOM.

Two executables share the same core:

| | |
|---|---|
| `cmd/busplan` | single-run CLI: request JSON in -> plan JSON out |
| `cmd/busplan-server` | HTTP service (results cached by `result_id`) |

## Setup
    export DATABASE_URL='postgres://USER:PASS@ep-xxx.neon.tech/neondb?sslmode=require'   # Neon
    export TOMTOM_API_KEY=...
    # apply db/schema.sql (stops: stop_id PK, latitude, longitude) and load your stops
    go build -o busplan ./cmd/busplan && go build -o busplan-server ./cmd/busplan-server
    # install VROOM (https://github.com/VROOM-Project/vroom) and put `vroom` on PATH (or -vroom /path)

### CLI
    ./busplan -input examples/request.json -output plan.json
    cat request.json | ./busplan > plan.json         # logs go to stderr

### Server
    ./busplan-server -addr :8080
    curl -X POST localhost:8080/v1/plans -d @examples/request.json          # sync: 200 + result
    curl -X POST 'localhost:8080/v1/plans?async=true' -d @examples/request.json   # 202 + result_id
    curl localhost:8080/v1/plans/{result_id}          # 200 result | 202 pending | 404 unknown/expired

Errors: 400 bad input / unknown stop_id, 422 cannot seat everyone in the window, 504 timeout.

## Config (env var or flag)
| flag | env | default |
|---|---|---|
| `-service-time-per-student` | `SERVICE_TIME_PER_STUDENT` | `20s` (VROOM job `service`, per student) |
| `-bus-arrival-stagger` | `BUS_ARRIVAL_STAGGER` | `30s` |
| `-tolerance` | `TOLERANCE` | `15s` binary-search precision |
| `-traffic-aware` | `TRAFFIC_AWARE` | `true` (send arrival deadline to TomTom for historical traffic data) |
| `-vroom`, `-vroom-args` | `VROOM_PATH`, `VROOM_ARGS` | `vroom`, `-x 5` |
| `-routing` | `ROUTING_PROVIDER` | `tomtom` (`tomtom` \| `haversine` \| `osrm`) |
| `-osrm-url` | `OSRM_URL` | `http://localhost:5000` (for OSRM routing) |
| `-matrix-cache-dir` | `MATRIX_CACHE_DIR` | `.matrix_cache` |
| `-result-ttl` | `RESULT_TTL` | `24h` (server) |
| `-max-concurrent-plans` | `MAX_CONCURRENT_PLANS` | `2` (server) |

`DATABASE_URL` and `TOMTOM_API_KEY` are env-only (secrets).

## Local testing without Neon / TomTom / VROOM
    docker compose up -d                                   # dummy Postgres (seeded) + OSRM service
    export DATABASE_URL='postgres://bus:bus@localhost:5433/busdb?sslmode=disable'
    ./busplan -input examples/request.json -routing haversine -solver greedy
    # or with OSRM backend:
    ./busplan -input examples/request.json -routing osrm -osrm-url http://localhost:5000 -solver greedy

`-routing haversine` and `-solver greedy` are TEST-ONLY stand-ins. Unit tests use in-memory
stops; `TEST_DATABASE_URL=... go test ./...` also runs the Postgres test.

## How it works
1. Distinct `stop_id`s (and identical coordinates) are looked up once; **only unique points go to TomTom**
   (tiled into <=2500-cell async jobs; matrices cached in memory + disk).
2. Every student is its own VROOM job (same-stop students share a location; VROOM handles it).
3. Baseline: all buses at MAX_TIME = usable window. Soft seat buffer relaxed if needed.
4. Buses are tuned one at a time (longest first): binary-search the smallest `max_travel_time` for that bus
   while the others keep their limits; then freeze it.
5. Vehicles have no start: each bus starts at its first pickup.
6. Times are computed backwards from `arrival_deadline`: the longest-trip bus arrives at the deadline, each
   next bus `bus_arrival_stagger` earlier; pickups are as late as possible. Same-stop students share a time.

`total_travel_time_seconds` = start -> college arrival, including boarding time.
