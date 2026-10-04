package updatestatus

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRecord_TracksFailureRunAndResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	t0 := time.Date(2026, 10, 4, 17, 45, 0, 0, time.UTC)

	if _, ok := Read(path); ok {
		t.Fatal("Read of a missing file reported ok")
	}
	Record(path, "runner-a", errors.New("Access is denied"), t0)
	Record(path, "runner-a", errors.New("Access is denied"), t0.Add(10*time.Minute))
	st, ok := Read(path)
	if !ok {
		t.Fatal("Read after Record failed")
	}
	if st.Failures != 2 || st.FailingSince != "2026-10-04T17:45:00Z" || st.LastError != "Access is denied" {
		t.Errorf("after 2 failures = %+v, want 2 failures since 17:45", st)
	}
	if st.LastCheckAt != "2026-10-04T17:55:00Z" || st.KeeperVersion != "runner-a" {
		t.Errorf("lastCheckAt/version = %q/%q", st.LastCheckAt, st.KeeperVersion)
	}

	Record(path, "runner-b", nil, t0.Add(20*time.Minute))
	st, _ = Read(path)
	if st.Failures != 0 || st.FailingSince != "" || st.LastError != "" || st.KeeperVersion != "runner-b" {
		t.Errorf("after a working check = %+v, want the failure run cleared", st)
	}
}
