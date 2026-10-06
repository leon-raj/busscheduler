// Package app holds configuration and wiring shared by both executables.
package app

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example/busscheduler/internal/routing"
	"github.com/example/busscheduler/internal/service"
	"github.com/example/busscheduler/internal/store"
	"github.com/example/busscheduler/internal/tomtom"
	"github.com/example/busscheduler/internal/vroom"
)

// Config is filled from environment variables (defaults) and overridden by
// flags. Secrets (DATABASE_URL, TOMTOM_API_KEY) are environment-only so they
// never appear in process listings.
type Config struct {
	// secrets (env only)
	DatabaseURL string // Neon: postgres://user:pass@ep-xxx.neon.tech/db?sslmode=require
	TomTomKey   string

	// tunables
	ServiceTimePerStudent time.Duration
	BusArrivalStagger     time.Duration
	Tolerance             time.Duration
	MaxTuned              int
	TrafficAware          bool

	VroomPath string
	VroomArgs string
	Solver    string // "vroom" (real) or "greedy" (testing only)

	MatrixCacheDir    string
	TomTomBlock       int
	TomTomConcurrency int
	RoutingProvider   string // "tomtom", "haversine", or "osrm"
	OSRMURL           string // e.g. "http://localhost:5000"

	// server only
	Addr               string
	ResultTTL          time.Duration
	MaxConcurrentPlans int
	PlanTimeout        time.Duration
}

func (c *Config) Bind(fs *flag.FlagSet) {
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	c.TomTomKey = os.Getenv("TOMTOM_API_KEY")

	fs.DurationVar(&c.ServiceTimePerStudent, "service-time-per-student", envDur("SERVICE_TIME_PER_STUDENT", 20*time.Second), "boarding time per student (env SERVICE_TIME_PER_STUDENT)")
	fs.DurationVar(&c.BusArrivalStagger, "bus-arrival-stagger", envDur("BUS_ARRIVAL_STAGGER", 30*time.Second), "gap between consecutive buses' college arrivals (env BUS_ARRIVAL_STAGGER)")
	fs.DurationVar(&c.Tolerance, "tolerance", envDur("TOLERANCE", 15*time.Second), "binary-search precision per bus")
	fs.IntVar(&c.MaxTuned, "max-tuned", envInt("MAX_TUNED", 0), "max buses to tune (0 = all active)")
	fs.BoolVar(&c.TrafficAware, "traffic-aware", envBool("TRAFFIC_AWARE", true), "send arriveAt=deadline to TomTom (future date, 400 km box)")

	fs.StringVar(&c.VroomPath, "vroom", envStr("VROOM_PATH", "vroom"), "path to the vroom binary")
	fs.StringVar(&c.VroomArgs, "vroom-args", envStr("VROOM_ARGS", "-x 5"), "extra vroom flags, e.g. \"-x 5 -t 4\"")
	fs.StringVar(&c.Solver, "solver", envStr("SOLVER", "vroom"), "vroom | greedy (greedy is a TEST-ONLY stand-in)")

	fs.StringVar(&c.MatrixCacheDir, "matrix-cache-dir", envStr("MATRIX_CACHE_DIR", ".matrix_cache"), "on-disk cache for routing matrices (\"\" = memory only)")
	fs.IntVar(&c.TomTomBlock, "tomtom-block", envInt("TOMTOM_BLOCK", 50), "matrix block side; 50x50=2500 cells is the standard-plan max")
	fs.IntVar(&c.TomTomConcurrency, "tomtom-concurrency", envInt("TOMTOM_CONCURRENCY", 2), "parallel TomTom jobs")
	fs.StringVar(&c.RoutingProvider, "routing", envStr("ROUTING_PROVIDER", "tomtom"), "routing provider: tomtom | haversine | osrm")
	fs.StringVar(&c.OSRMURL, "osrm-url", envStr("OSRM_URL", "http://localhost:5000"), "OSRM backend URL for osrm routing provider")
}

func (c *Config) BindServer(fs *flag.FlagSet) {
	fs.StringVar(&c.Addr, "addr", envStr("ADDR", ":8080"), "listen address")
	fs.DurationVar(&c.ResultTTL, "result-ttl", envDur("RESULT_TTL", 24*time.Hour), "how long results stay retrievable by result_id")
	fs.IntVar(&c.MaxConcurrentPlans, "max-concurrent-plans", envInt("MAX_CONCURRENT_PLANS", 2), "plans solved in parallel")
	fs.DurationVar(&c.PlanTimeout, "plan-timeout", envDur("PLAN_TIMEOUT", 15*time.Minute), "per-plan time limit")
}

// Build wires the service. The returned func releases resources.
func (c *Config) Build(ctx context.Context, log *slog.Logger) (*service.Service, func(), error) {
	pg, err := store.NewPostgres(ctx, c.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}

	var inner routing.Provider
	provider := strings.ToLower(strings.TrimSpace(c.RoutingProvider))
	if provider == "" {
		provider = "tomtom"
	}

	tag := provider
	switch provider {
	case "haversine":
		inner = routing.Haversine{}
		tag = "haversine"
		log.Warn("using OFFLINE haversine routing; travel times are approximate")

	case "osrm":
		inner = &routing.OSRM{
			BaseURL: c.OSRMURL,
		}
		tag = "osrm"
		log.Info("using OSRM routing", "url", c.OSRMURL)

	case "tomtom":
		if c.TomTomKey == "" {
			pg.Close()
			return nil, nil, fmt.Errorf("TOMTOM_API_KEY is not set (or use -routing=haversine or -routing=osrm)")
		}
		inner = &tomtom.Client{
			APIKey: c.TomTomKey, BlockSize: c.TomTomBlock, Concurrency: c.TomTomConcurrency, Log: log,
		}
		tag = "tomtom"

	default:
		pg.Close()
		return nil, nil, fmt.Errorf("unknown -routing provider %q (expected: tomtom, haversine, osrm)", c.RoutingProvider)
	}

	var solver vroom.Solver
	switch c.Solver {
	case "vroom":
		solver = vroom.CLI{Path: c.VroomPath, Args: strings.Fields(c.VroomArgs)}
	case "greedy":
		solver = vroom.GreedySolver{}
		log.Warn("using the TEST-ONLY greedy solver instead of VROOM")
	default:
		pg.Close()
		return nil, nil, fmt.Errorf("unknown -solver %q", c.Solver)
	}

	svc := &service.Service{
		Stops:   pg,
		Routing: &routing.Cached{Inner: inner, Tag: tag, Dir: c.MatrixCacheDir},
		Solver:  solver,
		Cfg: service.Config{
			ServiceTimePerStudent: c.ServiceTimePerStudent,
			BusArrivalStagger:     c.BusArrivalStagger,
			TolerancePerBus:       c.Tolerance,
			MaxTunedBuses:         c.MaxTuned,
			TrafficAware:          c.TrafficAware,
		},
		Log: log,
	}
	return svc, func() { pg.Close() }, nil
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
