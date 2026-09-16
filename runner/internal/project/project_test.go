package project

import (
	"path/filepath"
	"testing"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	root := t.TempDir()
	reg, err := NewRegistry(filepath.Join(root, "projects"), filepath.Join(root, "projects.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

func TestCreateAndList(t *testing.T) {
	reg := newTestRegistry(t)

	if got := reg.List(); len(got) != 0 {
		t.Fatalf("expected empty registry, got %d entries", len(got))
	}

	p, err := reg.Create("My Project")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID != "my-project" {
		t.Errorf("ID = %q, want %q", p.ID, "my-project")
	}
	if p.Name != "My Project" {
		t.Errorf("Name = %q, want %q", p.Name, "My Project")
	}
	if p.CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}

	list := reg.List()
	if len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("List = %+v, want single entry %+v", list, p)
	}
}

func TestCreateDuplicateNameGetsUniqueID(t *testing.T) {
	reg := newTestRegistry(t)

	p1, err := reg.Create("Same Name")
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	p2, err := reg.Create("Same Name")
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	if p1.ID == p2.ID {
		t.Fatalf("expected distinct ids, both were %q", p1.ID)
	}
}

func TestCreateEmptyNameFails(t *testing.T) {
	reg := newTestRegistry(t)
	if _, err := reg.Create("   "); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestGetNotFound(t *testing.T) {
	reg := newTestRegistry(t)
	if _, err := reg.Get("nope"); err != ErrNotFound {
		t.Fatalf("Get(unknown) err = %v, want ErrNotFound", err)
	}
}

func TestPersistenceAcrossReload(t *testing.T) {
	root := t.TempDir()
	projectsDir := filepath.Join(root, "projects")
	file := filepath.Join(root, "projects.json")

	reg1, err := NewRegistry(projectsDir, file)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, err := reg1.Create("Persisted")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg2, err := NewRegistry(projectsDir, file)
	if err != nil {
		t.Fatalf("NewRegistry (reload): %v", err)
	}
	got, err := reg2.Get(p.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Name != p.Name {
		t.Errorf("reloaded Name = %q, want %q", got.Name, p.Name)
	}
}

func TestDirResolvesProjectPath(t *testing.T) {
	reg := newTestRegistry(t)
	p, err := reg.Create("Dir Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dir, ok := reg.Dir(p.ID)
	if !ok {
		t.Fatal("Dir returned ok=false for known project")
	}
	if dir != p.Path {
		t.Errorf("Dir = %q, want %q", dir, p.Path)
	}

	if _, ok := reg.Dir("missing"); ok {
		t.Error("Dir returned ok=true for unknown project")
	}
}

func TestResolvePathWithinProject(t *testing.T) {
	reg := newTestRegistry(t)
	p, err := reg.Create("Resolve Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	full, err := reg.ResolvePath(p.ID, "sub/file.txt")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want := filepath.Join(p.Path, "sub", "file.txt")
	if full != want {
		t.Errorf("ResolvePath = %q, want %q", full, want)
	}
}

func TestResolvePathRejectsEscape(t *testing.T) {
	reg := newTestRegistry(t)
	p, err := reg.Create("Escape Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, bad := range []string{"../outside.txt", "../../etc/passwd", "sub/../../escape.txt"} {
		if _, err := reg.ResolvePath(p.ID, bad); err == nil {
			t.Errorf("ResolvePath(%q) = nil error, want an error", bad)
		}
	}
}

func TestResolvePathRejectsAbsolute(t *testing.T) {
	reg := newTestRegistry(t)
	p, err := reg.Create("Absolute Test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := reg.ResolvePath(p.ID, filepath.Join(string(filepath.Separator), "etc", "passwd")); err == nil {
		t.Error("ResolvePath(absolute) = nil error, want an error")
	}
}
