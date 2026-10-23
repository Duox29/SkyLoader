//go:build windows

package launcher

import (
	"fmt"
	"strings"
	"time"
)

const (
	watchInterval  = 700 * time.Millisecond
	injectionLimit = 30 * time.Second
)

func (l *Launcher) watch() {
	defer close(l.done)
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	l.pollProcess()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.pollProcess()
		}
	}
}

func (l *Launcher) pollProcess() {
	l.mu.RLock()
	gamePath := l.cfg.GamePath
	l.mu.RUnlock()

	process, running := findProcess("Sky.exe", gamePath)

	l.mu.Lock()
	changed := running != l.gameRunning
	if running && l.gameRunning && process.PID != l.running.PID {
		changed = true
	}
	l.gameRunning = running
	if running {
		l.running = process
	} else if l.gameRunning || l.running.PID != 0 {
		l.running = Process{}
		l.injectedPID = 0
		l.attemptedPID = 0
		l.loaded = map[string]bool{}
		l.pending = false
	}

	autoInject := l.cfg.AutoInject
	pending := l.pending
	injectedPID := l.injectedPID
	attemptedPID := l.attemptedPID
	injectingPID := l.injectingPID
	var pathUpdate string
	if running && process.Path != "" && !strings.EqualFold(normalizePath(process.Path), normalizePath(gamePath)) {
		l.cfg.GamePath = normalizePath(process.Path)
		pathUpdate = l.cfg.GamePath
	}
	l.mu.Unlock()

	if pathUpdate != "" {
		if err := l.persist(); err != nil {
			l.logger.Printf("save detected Sky path: %v", err)
		}
	}

	if changed {
		if running {
			l.setStatus(fmt.Sprintf("Sky.exe detected (PID %d).", process.PID))
		} else {
			l.setStatus("Sky.exe exited; plugins are no longer loaded.")
		}
	}

	if !running {
		return
	}

	shouldInject := autoInject || pending
	if shouldInject && injectedPID != process.PID && attemptedPID != process.PID && injectingPID != process.PID {
		l.injectAsync(process)
	}
}

func (l *Launcher) injectAsync(process Process) {
	l.mu.Lock()
	if l.injectingPID == process.PID {
		l.mu.Unlock()
		return
	}
	l.injectingPID = process.PID
	l.attemptedPID = process.PID
	l.pending = false
	l.mu.Unlock()

	go l.inject(process)
}

func (l *Launcher) inject(process Process) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()

	l.mu.RLock()
	bootstrap := l.cfg.BootstrapPath
	enabled := make([]pluginRecord, 0, len(l.plugins))
	for _, key := range l.order {
		record := l.plugins[key]
		if record != nil && record.Enabled && !record.Missing {
			enabled = append(enabled, *record)
		}
	}
	l.mu.RUnlock()

	fail := func(message string) {
		l.mu.Lock()
		l.injectingPID = 0
		l.mu.Unlock()
		l.setStatus(message)
		l.emitNotice("error", message)
	}

	if bootstrap == "" || !isRegularFile(bootstrap) {
		fail("SkyBootstrap.dll was not found. Choose a valid bootstrap DLL in Settings.")
		return
	}

	if err := injectDLL(process.PID, bootstrap, injectionLimit); err != nil {
		fail("Could not inject SkyBootstrap into Sky.exe: " + err.Error())
		return
	}

	// The Go runtime starts the pipe server as the DLL loads. Ping with the
	// normal timeout so a slow initialization is not mistaken for failure.
	if err := l.client.Ping(); err != nil {
		fail("SkyBootstrap was injected, but its pipe did not respond: " + err.Error())
		return
	}

	loadedPaths := make([]string, 0, len(enabled))
	var failures []string
	for _, record := range enabled {
		if err := l.client.Load(record.DLLPath); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%v)", firstNonEmpty(record.Name, record.ID), err))
			l.logger.Printf("load plugin %s: %v", record.DLLPath, err)
			continue
		}
		loadedPaths = append(loadedPaths, record.DLLPath)
	}

	authoritative, err := l.client.List()
	if err == nil {
		loadedPaths = authoritative
	}

	l.mu.Lock()
	stillRunning := l.gameRunning && l.running.PID == process.PID
	if stillRunning {
		l.injectedPID = process.PID
		l.injectingPID = 0
	}
	l.mu.Unlock()
	if !stillRunning {
		l.emitNotice("warning", "Sky.exe exited before SkyBootstrap finished loading.")
		return
	}

	l.setLoaded(loadedPaths)

	message := fmt.Sprintf("SkyBootstrap injected (PID %d); %d plugin(s) loaded.", process.PID, len(loadedPaths))
	if len(failures) > 0 {
		message = fmt.Sprintf("SkyBootstrap injected (PID %d), but %d plugin(s) failed to load: %s",
			process.PID, len(failures), strings.Join(failures, ", "))
		l.setStatus(message)
		l.emitNotice("warning", message)
		return
	}
	l.setStatus(message)
	l.emitNotice("success", message)
}
