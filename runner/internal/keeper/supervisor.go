package keeper

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Supervisor keeps one child process (the runner) running: restarts it with
// backoff when it exits, until Stop.
type Supervisor struct {
	Path string   // binary to run
	Env  []string // full environment for the child

	mu      sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{} // closed when the current child exits
	wanted  bool          // should a child be running?
	started time.Time
}

// Start launches the child and keeps it running until Stop.
func (s *Supervisor) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wanted {
		return
	}
	s.wanted = true
	s.spawnLocked()
}

// Stop stops the child (graceful where the OS allows, then kill) and waits
// for it to exit. The supervisor won't restart it until the next Start.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.wanted = false
	cmd, exited := s.cmd, s.exited
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	terminate(cmd.Process)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

// Uptime reports how long the current child has been running (0 if none).
func (s *Supervisor) Uptime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil {
		return 0
	}
	return time.Since(s.started)
}

func (s *Supervisor) spawnLocked() {
	cmd := exec.Command(s.Path)
	cmd.Env = s.Env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	hideWindow(cmd)
	exited := make(chan struct{})
	if err := cmd.Start(); err != nil {
		log.Printf("keeper: start %s: %v (retrying in 5s)", s.Path, err)
		close(exited)
		s.cmd, s.exited = nil, exited
		time.AfterFunc(5*time.Second, s.respawn)
		return
	}
	s.cmd, s.exited, s.started = cmd, exited, time.Now()
	log.Printf("keeper: started %s (pid %d)", s.Path, cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		close(exited)
		s.mu.Lock()
		ranFor := time.Since(s.started)
		if s.cmd == cmd {
			s.cmd = nil
		}
		wanted := s.wanted
		s.mu.Unlock()
		if !wanted {
			return
		}
		// Crash-looping? Back off; a child that ran a while restarts fast.
		delay := time.Second
		if ranFor < 30*time.Second {
			delay = 10 * time.Second
		}
		log.Printf("keeper: runner exited (%v) after %s, restarting in %s", err, ranFor.Round(time.Second), delay)
		time.AfterFunc(delay, s.respawn)
	}()
}

func (s *Supervisor) respawn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wanted && s.cmd == nil {
		s.spawnLocked()
	}
}
