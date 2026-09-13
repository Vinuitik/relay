package compose

import (
	"reflect"
	"testing"
)

type fakeRunner struct {
	name string
	args []string
	dir  string
	err  error
}

func (f *fakeRunner) Run(name string, args []string, dir string) error {
	f.name = name
	f.args = args
	f.dir = dir
	return f.err
}

func TestStartWithBuildsComposeUp(t *testing.T) {
	f := &fakeRunner{}
	if err := StartWith(f, "/some/project"); err != nil {
		t.Fatalf("StartWith: %v", err)
	}
	if f.name != "docker" {
		t.Errorf("name = %q, want docker", f.name)
	}
	want := []string{"compose", "up", "-d"}
	if !reflect.DeepEqual(f.args, want) {
		t.Errorf("args = %v, want %v", f.args, want)
	}
	if f.dir != "/some/project" {
		t.Errorf("dir = %q, want /some/project", f.dir)
	}
}

func TestStopWithBuildsComposeDown(t *testing.T) {
	f := &fakeRunner{}
	if err := StopWith(f, "/some/project"); err != nil {
		t.Fatalf("StopWith: %v", err)
	}
	want := []string{"compose", "down"}
	if !reflect.DeepEqual(f.args, want) {
		t.Errorf("args = %v, want %v", f.args, want)
	}
	if f.dir != "/some/project" {
		t.Errorf("dir = %q, want /some/project", f.dir)
	}
}

func TestStartWithPropagatesError(t *testing.T) {
	wantErr := &fakeErr{"boom"}
	f := &fakeRunner{err: wantErr}
	if err := StartWith(f, "/x"); err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
