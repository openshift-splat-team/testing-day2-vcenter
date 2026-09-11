package framework

import (
	"fmt"
	"strconv"
	"time"
)

// ScaleLadderConfig controls the cumulative worker scale benchmark.
type ScaleLadderConfig struct {
	Start      int
	Stop       int
	Step       int
	Timeout    time.Duration
	ResultsDir string
	CleanupTo  int
}

func ParseScaleLadderConfig(getenv func(string) string) (ScaleLadderConfig, error) {
	cfg := ScaleLadderConfig{Start: 50, Stop: 300, Step: 50, Timeout: 90 * time.Minute, ResultsDir: "reports/scale"}
	var err error
	if cfg.Start, err = positiveEnv(getenv, "PERF_SCALE_START", cfg.Start); err != nil {
		return cfg, err
	}
	if cfg.Stop, err = positiveEnv(getenv, "PERF_SCALE_STOP", cfg.Stop); err != nil {
		return cfg, err
	}
	if cfg.Step, err = positiveEnv(getenv, "PERF_SCALE_STEP", cfg.Step); err != nil {
		return cfg, err
	}
	if value := getenv("PERF_SCALE_TIMEOUT"); value != "" {
		cfg.Timeout, err = time.ParseDuration(value)
		if err != nil || cfg.Timeout <= 0 {
			return cfg, fmt.Errorf("PERF_SCALE_TIMEOUT must be a positive duration")
		}
	}
	if value := getenv("PERF_SCALE_RESULTS_DIR"); value != "" {
		cfg.ResultsDir = value
	}
	cfg.CleanupTo = cfg.Start
	if cfg.Stop < cfg.Start || (cfg.Stop-cfg.Start)%cfg.Step != 0 {
		return cfg, fmt.Errorf("scale range must be start <= stop and evenly divisible by step")
	}
	return cfg, nil
}

func positiveEnv(getenv func(string) string, name string, fallback int) (int, error) {
	value := getenv(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}
