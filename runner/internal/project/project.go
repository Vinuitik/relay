// Package project implements the runner's project registry: known project
// directories, scaffolded and persisted to a JSON file on disk.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned by Get when no project with the given id exists.
var ErrNotFound = errors.New("project not found")

// Project matches the shape defined in shared/API.md.
type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	CreatedAt string `json:"createdAt"` // RFC3339
}

// Registry is a mutex-guarded, disk-persisted list of known projects.
type Registry struct {
	mu       sync.Mutex
	root     string // directory under which project dirs are scaffolded
	file     string // path to the projects.json registry file
	projects []Project
}

// NewRegistry loads an existing registry file (if any) rooted at root and
// backed by file, creating both if they don't yet exist.
func NewRegistry(root, file string) (*Registry, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create projects root %q: %w", root, err)
	}

	r := &Registry{root: root, file: file}

	data, err := os.ReadFile(file)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &r.projects); err != nil {
				return nil, fmt.Errorf("parse projects file %q: %w", file, err)
			}
		}
	case os.IsNotExist(err):
		r.projects = []Project{}
		if err := r.persistLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("read projects file %q: %w", file, err)
	}

	if r.projects == nil {
		r.projects = []Project{}
	}
	return r, nil
}

// List returns a snapshot of all known projects.
func (r *Registry) List() []Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Project, len(r.projects))
	copy(out, r.projects)
	return out
}

// Get returns the project with the given id.
func (r *Registry) Get(id string) (Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, ErrNotFound
}

// Dir resolves a project id to its absolute directory path. It's a small
// adapter so other packages (e.g. session) can resolve a project directory
// without importing this package's full API.
func (r *Registry) Dir(id string) (string, bool) {
	p, err := r.Get(id)
	if err != nil {
		return "", false
	}
	return p.Path, true
}

// ResolvePath validates that projectRelPath stays within project id's
// directory (rejecting absolute paths and any "../" escape) and returns the
// resulting absolute path. Used by the file-viewing endpoints to guarantee a
// runner never serves a path outside the project it was asked about - see
// ARCHITECTURE.md "Runner responsibilities" (file read/write scoped to that
// project's directory only, never the whole filesystem).
func (r *Registry) ResolvePath(id, projectRelPath string) (string, error) {
	p, err := r.Get(id)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(projectRelPath) {
		return "", fmt.Errorf("path must be relative")
	}
	full := filepath.Join(p.Path, projectRelPath)
	rel, err := filepath.Rel(p.Path, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project directory")
	}
	return full, nil
}

// Create scaffolds a new project directory under the registry's root and
// registers it, persisting the registry to disk.
func (r *Registry) Create(name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("project name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	id := r.uniqueSlugLocked(slugify(name))
	dir := filepath.Join(r.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Project{}, fmt.Errorf("scaffold project dir %q: %w", dir, err)
	}

	p := Project{
		ID:        id,
		Name:      name,
		Path:      dir,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	r.projects = append(r.projects, p)
	if err := r.persistLocked(); err != nil {
		return Project{}, err
	}
	return p, nil
}

func (r *Registry) uniqueSlugLocked(base string) string {
	if base == "" {
		base = "project"
	}
	id := base
	for n := 2; ; n++ {
		exists := false
		for _, p := range r.projects {
			if p.ID == id {
				exists = true
				break
			}
		}
		if !exists {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func (r *Registry) persistLocked() error {
	data, err := json.MarshalIndent(r.projects, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projects registry: %w", err)
	}
	if err := os.WriteFile(r.file, data, 0o600); err != nil {
		return fmt.Errorf("write projects file %q: %w", r.file, err)
	}
	return nil
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns an arbitrary project name into a stable directory-safe id.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
