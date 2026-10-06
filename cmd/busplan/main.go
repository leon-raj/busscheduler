// busplan is the single-run command-line utility: read a request JSON, write
// the plan JSON.
//
//	busplan -input request.json -output plan.json
//	cat request.json | busplan > plan.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/example/busscheduler/internal/app"
	"github.com/example/busscheduler/internal/model"
	"github.com/example/busscheduler/internal/results"
)

func main() {
	var cfg app.Config
	in := flag.String("input", "-", "request JSON file (\"-\" = stdin)")
	out := flag.String("output", "-", "result JSON file (\"-\" = stdout)")
	cfg.Bind(flag.CommandLine)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil)) // logs never mix with JSON on stdout
	if err := run(log, cfg, *in, *out); err != nil {
		log.Error("failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg app.Config, inPath, outPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var raw []byte
	var err error
	if inPath == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(inPath)
	}
	if err != nil {
		return err
	}
	var req model.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("parsing request: %w", err)
	}

	svc, closeFn, err := cfg.Build(ctx, log)
	if err != nil {
		return err
	}
	defer closeFn()

	res, err := svc.Plan(ctx, req, results.NewID())
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	b = append(b, '\n')
	if outPath == "-" {
		_, err = os.Stdout.Write(b)
		return err
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return err
	}
	log.Info("plan written", "file", outPath, "result_id", res.ResultID)
	return nil
}
