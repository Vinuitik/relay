//go:build linux

package collectors

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestRealSandboxEndToEnd: ActivityWatch snapshot → real bwrap → python
// collector → value, through Engine.Test and Engine.Run. Skips without
// bwrap/python.
func TestRealSandboxEndToEnd(t *testing.T) {
	if err := OSSandbox.Available(); err != nil {
		t.Skipf("sandbox unavailable: %v", err)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	e := newTestEngine(t, OSSandbox, fakeAW(t).URL)
	e.Python = func() (string, error) { return py, nil }
	collector := `
import argparse, json, os
p = argparse.ArgumentParser()
p.add_argument("--inputs"); p.add_argument("--from", dest="f"); p.add_argument("--to")
a = p.parse_args()
evs = json.load(open(os.path.join(a.inputs, "aw", "window.json")))
mins = sum(e["duration"] for e in evs) / 60
print(json.dumps({"value": mins, "unit": "min", "evidence": [{"label": "events", "value": len(evs)}, {"label": "window", "value": a.f + ".." + a.to}]}))
`
	info := install(t, e, "real", dailyManifest, map[string]string{
		"collector.py":                         collector,
		"fixtures/empty/expected.json":         `{"value": 0}`,
		"fixtures/empty/inputs/aw/window.json": `[]`,
		"fixtures/two/expected.json":           `{"value": 1.5}`,
		"fixtures/two/inputs/aw/window.json":   `[{"duration": 60}, {"duration": 30}]`,
	})
	ctx := context.Background()
	res, err := e.Test(ctx, "real")
	if err != nil || !res.Passed || res.Sandbox != "bwrap" {
		t.Fatalf("Test = %+v, %v", res, err)
	}
	if err := e.Approve("real", info.ContractHash); err != nil {
		t.Fatal(err)
	}
	out, err := e.Run(ctx, "real", time.Now().Add(-24*time.Hour), time.Now())
	if err != nil || out.Value != 95.0/60 || out.Sandbox != "bwrap" || len(out.Evidence) != 2 {
		t.Fatalf("Run = %+v, %v", out, err)
	}
}
