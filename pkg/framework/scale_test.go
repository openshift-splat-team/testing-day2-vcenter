package framework

import (
	"testing"
	"time"
)

func TestParseScaleLadderConfigDefaults(t *testing.T) {
	cfg, err := ParseScaleLadderConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Start != 50 || cfg.Stop != 300 || cfg.Step != 50 {
		t.Fatalf("unexpected range: %+v", cfg)
	}
	if cfg.Timeout != 90*time.Minute || cfg.ResultsDir != "reports/scale" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseScaleLadderConfigRejectsUnevenRange(t *testing.T) {
	env := map[string]string{"PERF_SCALE_START": "50", "PERF_SCALE_STOP": "275", "PERF_SCALE_STEP": "50"}
	if _, err := ParseScaleLadderConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatal("expected uneven range error")
	}
}
