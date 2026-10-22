package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HerokeyVN/SkyLoader/internal/pluginbundle"
	"github.com/HerokeyVN/SkyLoader/internal/pluginid"
	"github.com/HerokeyVN/SkyLoader/internal/protocol"
	"github.com/HerokeyVN/SkyLoader/internal/uiserver"
)

// Import installs DLLs, zip bundles, or unpacked bundle directories into the
// managed store. A conflicting bundle is returned as an item with action
// "conflict"; retry the same call with force=true after confirmation.
func (l *Launcher) Import(paths []string, force bool) ImportReport {
	report := ImportReport{Paths: append([]string(nil), paths...), Force: force}
	var added, updated, conflicts, failed int

	for _, source := range paths {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		result := l.store.Import(source, force)
		item := ImportItem{
			Source:  source,
			Action:  string(result.Action),
			ID:      result.ID,
			Name:    result.Name,
			Version: result.Version,
			Message: result.Message,
		}
		report.Items = append(report.Items, item)

		switch result.Action {
		case pluginbundle.ActionAdded, pluginbundle.ActionUpdated:
			if result.Action == pluginbundle.ActionAdded {
				added++
			} else {
				updated++
			}
			l.afterInstall(result, source)
		case pluginbundle.ActionConflict:
			conflicts++
		case pluginbundle.ActionUnchanged:
			// Nothing to do; the message already explains why.
		default:
			failed++
		}
	}

	switch {
	case conflicts > 0:
		report.NeedsConfirm = true
		report.Message = "One or more plugins already exist. Confirm replacement to continue."
	case added+updated > 0:
		report.Message = fmt.Sprintf("Imported %d plugin(s): %d added, %d updated.", added+updated, added, updated)
	case len(report.Items) > 0:
		report.Message = "No plugin files were changed."
	}
	if failed > 0 {
		if report.Message != "" {
			report.Message += " "
		}
		report.Message += fmt.Sprintf("%d plugin(s) could not be imported.", failed)
	}
	if len(report.Items) == 0 {
		report.Message = "No plugin files were selected."
	}
	if report.Message != "" {
		l.emitNotice("info", report.Message)
	}
	return report
}

func (l *Launcher) afterInstall(result pluginbundle.Result, source string) {
	bundle, err := pluginbundle.Load(filepath.Join(l.store.Root, pluginid.DirectoryName(result.ID)))
	if err != nil {
		l.logger.Printf("reload imported bundle %s: %v", result.ID, err)
		return
	}

	l.mu.Lock()
	key := strings.ToLower(bundle.ID)
	existing := l.plugins[key]
	enabled := true
	if existing != nil {
		enabled = existing.Enabled
	}
	l.plugins[key] = &pluginRecord{
		ID:          bundle.ID,
		Name:        firstNonEmpty(bundle.Name, bundle.ID),
		Version:     bundle.Version,
		Author:      bundle.Author,
		Description: bundle.Description,
		DLLPath:     bundle.DLLPath,
		BundleRoot:  bundle.Root,
		UIDir:       bundle.UIDir,
		UIEntry:     bundle.UIEntry,
		Enabled:     enabled,
		Managed:     true,
	}
	if existing == nil {
		l.order = append(l.order, key)
	}
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	l.sortOrder()
	if err := l.persist(); err != nil {
		l.setStatus("Plugin installed, but settings could not be saved: " + err.Error())
		return
	}

	if injected && enabled {
		if err := l.client.Load(bundle.DLLPath); err != nil {
			l.setStatus("Plugin installed, but Sky did not load it: " + err.Error())
		} else {
			l.setStatus("Plugin installed and loaded into Sky.")
		}
		l.refreshLoaded()
	} else {
		l.setStatus("Plugin installed from " + filepath.Base(source) + ".")
	}
	l.notify()
}

func (l *Launcher) sortOrder() {
	l.mu.Lock()
	defer l.mu.Unlock()
	sort.SliceStable(l.order, func(i, j int) bool {
		first := l.plugins[l.order[i]]
		second := l.plugins[l.order[j]]
		firstKey := strings.ToLower(firstNonEmpty(first.Name, first.ID))
		secondKey := strings.ToLower(firstNonEmpty(second.Name, second.ID))
		if firstKey == secondKey {
			return strings.ToLower(first.ID) < strings.ToLower(second.ID)
		}
		return firstKey < secondKey
	})
}

// SetPluginEnabled toggles a plugin and, when Sky is already injected, applies
// the change to the running game.
func (l *Launcher) SetPluginEnabled(id string, enabled bool) error {
	l.mu.Lock()
	record := l.plugins[strings.ToLower(id)]
	if record == nil {
		l.mu.Unlock()
		return errors.New("plugin was not found")
	}
	record.Enabled = enabled
	dllPath := record.DLLPath
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Could not save the plugin setting: " + err.Error())
		return err
	}

	if injected {
		var pipeErr error
		if enabled {
			pipeErr = l.client.Load(dllPath)
		} else {
			pipeErr = l.client.Unload(dllPath)
		}
		if pipeErr != nil {
			l.setStatus("Setting saved, but Sky did not accept it: " + pipeErr.Error())
		} else if enabled {
			l.setStatus("Plugin loaded into Sky.")
		} else {
			l.setStatus("Plugin unloaded from Sky.")
		}
		l.refreshLoaded()
	} else {
		l.setStatus("Plugin setting saved.")
	}
	l.notify()
	return nil
}

// RemovePlugin unregisters a plugin. Managed bundles are deleted from the
// managed store; legacy plugins outside the store are only unregistered.
func (l *Launcher) RemovePlugin(id string) error {
	key := strings.ToLower(id)
	l.mu.Lock()
	record := l.plugins[key]
	if record == nil {
		l.mu.Unlock()
		return errors.New("plugin was not found")
	}
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	if injected {
		_ = l.client.Unload(record.DLLPath)
	}

	if record.Managed {
		directory := record.BundleRoot
		if directory == "" {
			directory = filepath.Dir(record.DLLPath)
		}
		directory = filepath.Clean(directory)
		if directory == l.storeRoot {
			return errors.New("refusing to delete the managed plugin store itself")
		}
		if !l.insideStore(directory) {
			return errors.New("refusing to delete a plugin outside the managed store")
		}
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("remove plugin files (close Sky and try again): %w", err)
		}
	}

	l.mu.Lock()
	delete(l.plugins, key)
	delete(l.loaded, strings.ToLower(record.DLLPath))
	next := l.order[:0]
	for _, existing := range l.order {
		if existing != key {
			next = append(next, existing)
		}
	}
	l.order = next
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Plugin removed, but settings could not be saved: " + err.Error())
		return err
	}
	if record.Managed {
		l.setStatus("Plugin and its files were removed.")
	} else {
		l.setStatus("Plugin unregistered; its original file was not deleted.")
	}
	l.refreshLoaded()
	return nil
}

func (l *Launcher) insideStore(path string) bool {
	relative, err := filepath.Rel(l.storeRoot, filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// PluginUI implements uiserver.Source.
func (l *Launcher) PluginUI(id string) (uiserver.Plugin, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record := l.plugins[strings.ToLower(id)]
	if record == nil || record.UIDir == "" || record.UIEntry == "" {
		return uiserver.Plugin{}, false
	}
	return uiserver.Plugin{
		ID:      record.ID,
		Name:    record.Name,
		Version: record.Version,
		Root:    record.UIDir,
		Entry:   record.UIEntry,
	}, true
}

// PluginRPC implements uiserver.Source.
func (l *Launcher) PluginRPC(id string, payload []byte) ([]byte, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	injected := l.gameRunning && l.injectedPID == l.running.PID
	dllPath := ""
	if record != nil {
		dllPath = record.DLLPath
	}
	l.mu.RUnlock()

	if record == nil {
		return nil, errors.New("plugin was not found")
	}
	if !injected {
		return nil, errors.New("Sky is not running with SkyBootstrap yet. Launch Sky from SkyLoader, then try again.")
	}

	response, err := l.client.UI(dllPath, payload)
	if err != nil {
		var remote *protocol.RemoteError
		if errors.As(err, &remote) {
			return nil, errors.New(remote.Message)
		}
		return nil, fmt.Errorf("plugin request failed: %w", err)
	}
	return response, nil
}

// PluginEvents implements uiserver.Source.
func (l *Launcher) PluginEvents(id string) ([][]byte, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	injected := l.gameRunning && l.injectedPID == l.running.PID
	dllPath := ""
	if record != nil {
		dllPath = record.DLLPath
	}
	l.mu.RUnlock()
	if record == nil || !injected {
		return nil, nil
	}
	events, err := l.client.Poll(dllPath)
	if err != nil {
		var remote *protocol.RemoteError
		if errors.As(err, &remote) {
			return nil, errors.New(remote.Message)
		}
		return nil, err
	}
	return events, nil
}
