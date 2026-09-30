package compose

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	name string
	args []string
	dir  string
	out  string
	err  error
}

func (f *fakeRunner) Run(name string, args []string, dir string) ([]byte, error) {
	f.name = name
	f.args = args
	f.dir = dir
	return []byte(f.out), f.err
}

func TestUpWithBuildsComposeUpForFile(t *testing.T) {
	f := &fakeRunner{}
	if err := UpWith(f, "/some/project", "docker-compose.dev.yml"); err != nil {
		t.Fatalf("UpWith: %v", err)
	}
	if f.name != "docker" {
		t.Errorf("name = %q, want docker", f.name)
	}
	want := []string{"compose", "-f", "docker-compose.dev.yml", "up", "-d"}
	if !reflect.DeepEqual(f.args, want) {
		t.Errorf("args = %v, want %v", f.args, want)
	}
	if f.dir != "/some/project" {
		t.Errorf("dir = %q, want /some/project", f.dir)
	}
}

func TestDownWithRemovesOrphans(t *testing.T) {
	f := &fakeRunner{}
	if err := DownWith(f, "/some/project", "docker-compose.yml"); err != nil {
		t.Fatalf("DownWith: %v", err)
	}
	want := []string{"compose", "-f", "docker-compose.yml", "down", "--remove-orphans"}
	if !reflect.DeepEqual(f.args, want) {
		t.Errorf("args = %v, want %v", f.args, want)
	}
}

func TestUpWithIncludesCommandOutputInError(t *testing.T) {
	f := &fakeRunner{out: "pulling...\nno such image: foo", err: errors.New("exit status 1")}
	err := UpWith(f, "/x", "compose.yml")
	if err == nil || !strings.Contains(err.Error(), "no such image: foo") {
		t.Fatalf("err = %v, want it to include docker's output", err)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"docker-compose.yml", "docker-compose.dev.yml", "docker-compose-prod.yaml",
		"compose.override.yml", "Dockerfile", "README.md", "compose.json",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with a matching name must be ignored.
	if err := os.Mkdir(filepath.Join(dir, "compose.yml"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	want := []string{"compose.override.yml", "docker-compose-prod.yaml", "docker-compose.dev.yml", "docker-compose.yml"}
	if !reflect.DeepEqual(d.ComposeFiles, want) {
		t.Errorf("ComposeFiles = %v, want %v", d.ComposeFiles, want)
	}
	if !d.HasDockerfile {
		t.Error("HasDockerfile = false, want true")
	}
}

func TestDefaultFile(t *testing.T) {
	cases := []struct {
		files []string
		want  string
	}{
		{nil, ""},
		{[]string{"docker-compose.dev.yml"}, "docker-compose.dev.yml"},
		{[]string{"docker-compose.dev.yml", "docker-compose.yml"}, "docker-compose.yml"},
		{[]string{"docker-compose.dev.yml", "docker-compose.prod.yml"}, ""},
	}
	for _, c := range cases {
		if got := DefaultFile(c.files); got != c.want {
			t.Errorf("DefaultFile(%v) = %q, want %q", c.files, got, c.want)
		}
	}
}

func TestStatusWithParsesDockerPs(t *testing.T) {
	f := &fakeRunner{out: "web|running|/p/docker-compose.dev.yml\ndb|exited|/p/docker-compose.dev.yml,/p/extra.yml\n"}
	got, err := StatusWith(f, "/p")
	if err != nil {
		t.Fatalf("StatusWith: %v", err)
	}
	want := []Container{
		{Service: "web", State: "running", ComposeFile: "docker-compose.dev.yml"},
		{Service: "db", State: "exited", ComposeFile: "docker-compose.dev.yml"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !strings.Contains(strings.Join(f.args, " "), "label=com.docker.compose.project.working_dir=/p") {
		t.Errorf("args %v don't filter by working dir", f.args)
	}
}

func TestStatusWithNoContainers(t *testing.T) {
	got, err := StatusWith(&fakeRunner{out: ""}, "/p")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty non-nil slice", got, err)
	}
}
