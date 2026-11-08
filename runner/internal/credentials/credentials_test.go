package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const secret = "s3cr3t-VALUE-xyz"

func put(t *testing.T, s *Store, name, body string) (Info, error) {
	t.Helper()
	var req PutRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	return s.Put(name, req)
}

func TestStorePutListDelete(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if _, err := put(t, s, "github", `{"host":"api.github.com","auth":"bearer","secret":"`+secret+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := put(t, s, "trello", `{"host":"api.trello.com","auth":"query","secret":{"key":"k1","token":"`+secret+`"}}`); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Name != "github" || list[1].Host != "api.trello.com" || list[0].Auth != "bearer" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "k1") {
		t.Fatalf("List leaks a value: %s", b)
	}
	c, err := s.Get("github")
	if err != nil || c.Secret != secret {
		t.Fatalf("Get = %v", err)
	}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(f, c); strings.Contains(out, secret) {
			t.Fatalf("%s prints the secret: %s", f, out)
		}
	}
	// replace keeps one entry, other Store on the same file sees it
	if _, err := put(t, s, "github", `{"host":"api.github.com","auth":"header","headerName":"x-api-key","secret":"other"}`); err != nil {
		t.Fatal(err)
	}
	if c, _ := Open(dir).Get("github"); c.Auth != "header" || c.HeaderName != "X-Api-Key" || c.Secret != "other" {
		t.Fatalf("after replace: %+v %q", c.Info(), c.HeaderName)
	}
	if err := s.Delete("github"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := s.Get("github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted = %v", err)
	}
}

func TestStoreFilePermsAndAtomic(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	for i := 0; i < 3; i++ {
		if _, err := put(t, s, fmt.Sprintf("c%d", i), `{"host":"api.github.com","auth":"bearer","secret":"v"}`); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != FileName {
		t.Fatalf("dir has %v, want only %s (no temp files left)", ents, FileName)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, FileName))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
		}
	}
	// a failed write (dir not writable) leaves the old file intact
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		os.Chmod(dir, 0o500)
		defer os.Chmod(dir, 0o700)
		if _, err := put(t, s, "c9", `{"host":"api.github.com","auth":"bearer","secret":"v"}`); err == nil {
			t.Fatal("write into read-only dir succeeded")
		}
		if list, _ := s.List(); len(list) != 3 {
			t.Fatalf("after failed write: %d entries", len(list))
		}
	}
}

func TestPutValidation(t *testing.T) {
	s := Open(t.TempDir())
	bad := map[string]string{
		"Bad Name":                             `{"host":"api.github.com","auth":"bearer","secret":"x"}`,
		"this-name-is-way-too-long-for-a-cred": `{"host":"api.github.com","auth":"bearer","secret":"x"}`,
	}
	for n, b := range bad {
		if _, err := put(t, s, n, b); !errors.Is(err, ErrBadRequest) {
			t.Errorf("name %q: %v", n, err)
		}
	}
	for _, b := range []string{
		`{"host":"https://api.github.com","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"api.github.com:443","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"api.github.com/x","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"user@api.github.com","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"127.0.0.1","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"[::1]","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"10.0.0.5","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"localhost","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"nas.local","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"metadata.google.internal","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"api.github.com.","auth":"bearer","secret":"` + secret + `"}`,
		`{"host":"api.github.com","auth":"basic","secret":"` + secret + `"}`,
		`{"host":"api.github.com","auth":"bearer"}`,
		`{"host":"api.github.com","auth":"bearer","secret":"a\nb` + secret + `"}`,
		`{"host":"api.github.com","auth":"bearer","secret":{"a":"` + secret + `"}}`,
		`{"host":"api.github.com","auth":"header","secret":"` + secret + `"}`,
		`{"host":"api.github.com","auth":"header","headerName":"Host","secret":"` + secret + `"}`,
		`{"host":"api.github.com","auth":"query","secret":"` + secret + `"}`,
		`{"host":"api.github.com","auth":"query","secret":{"bad name":"` + secret + `"}}`,
	} {
		_, err := put(t, s, "x", b)
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: %v, want bad request", b, err)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the secret: %v", err)
		}
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("bad puts stored something: %v", list)
	}
}

func TestApply(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://api.trello.com/1/boards/x/cards?fields=id", nil)
	Credential{Auth: AuthQuery, Params: map[string]string{"token": "t&1", "key": "k"}}.Apply(r)
	if r.URL.RawQuery != "fields=id&key=k&token=t%261" {
		t.Fatalf("query = %q", r.URL.RawQuery)
	}
	r, _ = http.NewRequest("GET", "https://api.github.com/user", nil)
	Credential{Auth: AuthBearer, Secret: "abc"}.Apply(r)
	if r.Header.Get("Authorization") != "Bearer abc" {
		t.Fatal(r.Header)
	}
}
