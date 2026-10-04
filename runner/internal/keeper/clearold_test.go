package keeper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClearOldRemovesOldAndStaleAsides(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "keeperd.exe.old")
	aside := old + "-123"
	for _, p := range []string{old, aside} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clearOld(old)
	for _, p := range []string{old, aside} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", filepath.Base(p))
		}
	}
}
