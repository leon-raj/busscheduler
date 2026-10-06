-- Runs once when the Postgres volume is first created.
-- Creates the scheduler service's database alongside the main project's 'transit' DB.
-- The 'transit' database itself is created automatically via POSTGRES_DB=transit.
CREATE DATABASE busdb OWNER transit;
