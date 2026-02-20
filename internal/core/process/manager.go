package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Daemon represents an external process managed by the application.
type Daemon interface {
	Name() string
	Command() (bin string, args []string)
	ReadyCheck() ReadyProbe
	Healthy(ctx context.Context) bool
}

type ReadyProbe struct {
	Check    func(ctx context.Context) bool
	Interval time.Duration
	Timeout  time.Duration
}

// Manager manages daemon lifecycles as child processes.
type Manager struct {
	mu      sync.Mutex
	daemons []managedDaemon
}

type managedDaemon struct {
	daemon Daemon
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Register(d Daemon) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.daemons = append(m.daemons, managedDaemon{daemon: d})
}

// StartAll launches all registered daemons and waits for ready.
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.daemons {
		if err := m.startOne(ctx, &m.daemons[i]); err != nil {
			return fmt.Errorf("start %s: %w", m.daemons[i].daemon.Name(), err)
		}
	}
	return nil
}

func (m *Manager) startOne(ctx context.Context, md *managedDaemon) error {
	bin, args := md.daemon.Command()
	// Use a dedicated context so daemon processes are not killed by parent
	// context cancellation — StopAll handles graceful shutdown explicitly.
	dCtx, cancel := context.WithCancel(context.Background())
	md.cancel = cancel

	cmd := exec.CommandContext(dCtx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	md.cmd = cmd

	log.Info().Str("daemon", md.daemon.Name()).Str("bin", bin).Msg("starting daemon")

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start process: %w", err)
	}

	// Wait for ready
	probe := md.daemon.ReadyCheck()
	deadline := time.Now().Add(probe.Timeout)
	for time.Now().Before(deadline) {
		if probe.Check(ctx) {
			log.Info().Str("daemon", md.daemon.Name()).Msg("daemon ready")
			return nil
		}
		time.Sleep(probe.Interval)
	}

	return fmt.Errorf("daemon %s not ready after %s", md.daemon.Name(), probe.Timeout)
}

const stopGracePeriod = 5 * time.Second

// StopAll gracefully stops all daemons in parallel with a shared deadline.
func (m *Manager) StopAll(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var wg sync.WaitGroup
	for i := range m.daemons {
		md := &m.daemons[i]
		if md.cmd == nil || md.cmd.Process == nil {
			continue
		}
		wg.Add(1)
		go func(md *managedDaemon) {
			defer wg.Done()
			m.stopOne(md)
		}(md)
	}
	wg.Wait()
	return nil
}

func (m *Manager) stopOne(md *managedDaemon) {
	log.Info().Str("daemon", md.daemon.Name()).Msg("stopping daemon")

	// Send SIGTERM
	_ = md.cmd.Process.Signal(os.Interrupt)

	// Wait for graceful exit
	done := make(chan struct{})
	go func() {
		_ = md.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		_ = md.cmd.Process.Kill()
	}

	if md.cancel != nil {
		md.cancel()
	}
}

// Watch monitors daemons and restarts crashed ones with backoff.
func (m *Manager) Watch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			m.checkAndRestart(ctx)
		}
	}
}

func (m *Manager) checkAndRestart(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.daemons {
		md := &m.daemons[i]
		if md.cmd == nil || md.cmd.Process == nil {
			continue
		}
		if !md.daemon.Healthy(ctx) {
			log.Warn().Str("daemon", md.daemon.Name()).Msg("daemon unhealthy, restarting")
			if md.cancel != nil {
				md.cancel()
			}
			if md.cmd.Process != nil {
				_ = md.cmd.Process.Kill()
				_ = md.cmd.Wait()
			}
			if err := m.startOne(ctx, md); err != nil {
				log.Error().Err(err).Str("daemon", md.daemon.Name()).Msg("restart failed")
			}
		}
	}
}
