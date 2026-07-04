// file: internal/watcher/watcher.go
// version: 2.1.2
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012
// last-edited: 2026-07-03

package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// audioExtensions are the file extensions we care about.
var audioExtensions = map[string]bool{
	".mp3":  true,
	".m4a":  true,
	".m4b":  true,
	".flac": true,
	".ogg":  true,
	".opus": true,
	".wma":  true,
	".aac":  true,
}

// DefaultDebounce is the default debounce period.
const DefaultDebounce = 5 * time.Second

// Callback is invoked after the debounce period with the root directory.
type Callback func(rootDir string)

// Watcher monitors a directory tree for audio file changes and invokes a
// callback after a debounce period.
type Watcher struct {
	fsWatcher *fsnotify.Watcher
	rootDir   string
	debounce  time.Duration
	callback  Callback
	stop      chan struct{}
	stopped   chan struct{}
	mu        sync.Mutex
	timer     *time.Timer
	scanGen   uint64
	running   bool
}

// New creates a Watcher. The callback is called with rootDir after events
// settle for the debounce duration. Pass 0 for debounce to use DefaultDebounce.
func New(callback Callback, debounce time.Duration) *Watcher {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	return &Watcher{
		debounce: debounce,
		callback: callback,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start begins watching rootDir recursively. It is safe to call only once.
func (w *Watcher) Start(rootDir string) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fsWatcher = fsw
	w.rootDir = rootDir

	// Walk the tree and add all directories.
	if err := w.addRecursive(rootDir); err != nil {
		fsw.Close()
		return err
	}

	go w.eventLoop()
	return nil
}

// Stop gracefully shuts down the watcher and waits for the event loop to exit.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	w.mu.Unlock()

	close(w.stop)
	if w.fsWatcher != nil {
		w.fsWatcher.Close()
	}
	<-w.stopped

	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if d.IsDir() {
			if watchErr := w.fsWatcher.Add(path); watchErr != nil {
				slog.Warn("watcher cannot watch", "path", path, "watchErr", watchErr)
			}
		}
		return nil
	})
}

func (w *Watcher) eventLoop() {
	defer close(w.stopped)

	for {
		select {
		case <-w.stop:
			return
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			slog.Error("watcher error", "err", err)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	// On Create, if it's a directory, watch it recursively.
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			_ = w.addRecursive(event.Name)
		}
	}

	relevant := event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Write) != 0
	if !relevant {
		return
	}
	if !IsAudioFile(event.Name) {
		return
	}

	w.scheduleScan()
}

func (w *Watcher) scheduleScan() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.scanGen++
	gen := w.scanGen

	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}

	w.timer = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		if w.scanGen != gen {
			// A newer event superseded this timer; do nothing.
			w.mu.Unlock()
			return
		}
		w.timer = nil
		w.mu.Unlock()

		slog.Info("watcher triggering callback", "rootDir", w.rootDir)
		if w.callback != nil {
			w.callback(w.rootDir)
		}
	})
}

// IsAudioFile reports whether name has a recognized audio extension.
func IsAudioFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return audioExtensions[ext]
}
