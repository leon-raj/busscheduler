-- Database schema for bus scheduler

-- Stops table
CREATE TABLE IF NOT EXISTS stops (
    stop_id   TEXT             PRIMARY KEY,
    name      TEXT,
    latitude  DOUBLE PRECISION NOT NULL CHECK (latitude  BETWEEN  -90 AND  90),
    longitude DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180)
);

-- Buses table
CREATE TABLE IF NOT EXISTS buses (
    bus_id   TEXT PRIMARY KEY,
    capacity INT  NOT NULL CHECK (capacity >= 1)
);

-- Students table
CREATE TABLE IF NOT EXISTS students (
    student_id TEXT PRIMARY KEY,
    stop_id    TEXT NOT NULL
);

-- Entire bus plan and versions
CREATE TABLE IF NOT EXISTS plans (
    plan_id         TEXT        PRIMARY KEY,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    college_id      TEXT,
    college_stop_id TEXT,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    plan_data       JSONB       NOT NULL
);

-- Assigned route of buses for each plan version
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

-- Assigned bus for students for each plan version
CREATE TABLE IF NOT EXISTS student_assignments (
    id             SERIAL PRIMARY KEY,
    plan_id        TEXT        NOT NULL REFERENCES plans(plan_id) ON DELETE CASCADE,
    student_id     TEXT        NOT NULL,
    bus_id         TEXT        NOT NULL,
    pickup_stop_id TEXT        NOT NULL,
    pickup_time    TIMESTAMPTZ NOT NULL
);
