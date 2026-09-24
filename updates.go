package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"

	"github.com/artipop/xxvi/internal/app"
	"github.com/artipop/xxvi/internal/msg"
)

// How this application replaces itself.
//
// `app.Updater` owns the risky part — streaming, checking the signature,
// unpacking, and the helper that swaps the bundle once we are gone. This file
// adds the three things the framework leaves to the application: which feed to
// trust, what survives a restart, and who is told.
//
// **The feed is a signed manifest at an address of ours.** The `github`
// provider verifies a SHA256SUMS sidecar, which catches a corrupted download
// and nothing else — the hash and the file come from the same place — and it
// ties the application to a public repository. `endpoint` reads a manifest
// whose artifacts are signed against updaterPublicKey below, compiled in, so
// the feed has no say in which key authenticates it.
//
// **The framework's own window is not used** (updater.WindowNone): it is
// hard-coded English, and everything a person reads here is Russian. The
// «Update» screen draws the whole thing off one event of ours.

//go:embed build/updater.key.pub
var updaterPublicKey []byte

// updateManifestURL is where the manifest of the newest release lives, and it
// is **the one address this application can never change**: every copy already
// installed asks here and nowhere else. Which is the whole reason it is a
// domain of ours rather than a release page on somebody's platform — where the
// files are kept is then a decision that can be revisited every release, and
// this line cannot.
//
// Nothing answering, or a 404, is read as «nothing to update», so a bucket that
// is not there yet costs a line in the log rather than an error on the screen.
//
// One const per line, no block: the release workflow reads these with sed, so
// the address a release is published under and the address an installed copy
// asks cannot drift apart.
const updateManifestURL = "https://updates.deffun.org/xxvi.json"

// updateChannel is the manifest field the provider filters on, and the value
// the release workflow signs a manifest with. A manifest that ended up at this
// address by mistake is then refused rather than installed.
const updateChannel = "stable"

// updateCheckInterval is our own timer rather than updater.Config.CheckInterval:
// Init may be called once and StopPeriodicCheck cannot be undone, so the
// framework's timer would make «check by itself» a switch that takes effect at
// the next launch. This one is read on every tick.
const updateCheckInterval = 6 * time.Hour

// updateFirstCheckDelay keeps the first check off the launch path: starting up
// is when this application opens a database, starts terminals and reads its
// sources, and none of that should wait behind an HTTP request to another
// continent.
const updateFirstCheckDelay = time.Minute

// updateSettings is <DataDir>/updates.json — the part of updating that has to
// survive a restart. The framework keeps none of it: the download is a
// temporary directory the helper deletes, and the version a person skipped
// lives in a field of the running Updater and dies with the process.
type updateSettings struct {
	// Enabled is a pointer so that a file written before this field existed
	// reads as «did not answer», not as «turned off».
	Enabled *bool `json:"enabled,omitempty"`
	// SkippedVersion is the release a person said no to. Restored into the
	// Updater at startup, or «skip» would mean «until restart».
	SkippedVersion string `json:"skippedVersion,omitempty"`
	// LastCheckedAt is RFC 3339, and exists so the screen can answer «when did it
	// last look» without looking again.
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
}

func (s updateSettings) enabled() bool { return s.Enabled == nil || *s.Enabled }

// readUpdateSettings never fails: a missing file is the defaults, and a file
// somebody has broken is the defaults plus a line in the log. Refusing to start
// because a preferences file is malformed would be a worse answer than checking
// for updates one more time than asked.
func readUpdateSettings(path string, log *slog.Logger) updateSettings {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return updateSettings{}
	}
	if err != nil {
		log.Warn("could not read the update settings", "path", path, "err", err)
		return updateSettings{}
	}
	var s updateSettings
	if err := json.Unmarshal(data, &s); err != nil {
		log.Warn("update settings are corrupt", "path", path, "err", err)
		return updateSettings{}
	}
	return s
}

// writeUpdateSettings writes the whole file, indented: it is small, it is
// occasionally read by a person, and there is one writer.
func writeUpdateSettings(path string, s updateSettings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// updateController owns the Updater, the settings file and the snapshot the
// screen reads. It implements app.Updates.
type updateController struct {
	up   *updater.Updater
	core *app.App
	path string
	log  *slog.Logger

	mu       sync.Mutex
	settings updateSettings
	state    app.UpdateState

	stop     chan struct{}
	stopOnce sync.Once
}

// newUpdateController configures the framework's Updater and restores what the
// last run remembered. It returns nil (and says so in the log) rather than
// failing the launch: an application that will not start because a release feed
// is misconfigured is worse than one that cannot update itself.
func newUpdateController(wails *application.App, core *app.App, log *slog.Logger) *updateController {
	path := filepath.Join(core.DataDir, "updates.json")
	settings := readUpdateSettings(path, log)

	feed, err := endpoint.New(endpoint.Config{URL: updateManifestURL, Channel: updateChannel})
	if err != nil {
		log.Warn("updates disabled: could not set up the release feed", "err", err)
		return nil
	}
	if err := wails.Updater.Init(updater.Config{
		CurrentVersion: appVersion,
		Providers:      []updater.Provider{feed},
		PublicKey:      updaterPublicKey,
		// Drawn by frontend/src/views/updates.tsx, in Russian, off the event
		// below.
		Window: updater.WindowNone,
	}); err != nil {
		log.Warn("updates disabled", "err", err)
		return nil
	}
	if settings.SkippedVersion != "" {
		wails.Updater.SkipVersion(settings.SkippedVersion)
	}

	c := &updateController{
		up:       wails.Updater,
		core:     core,
		path:     path,
		log:      log,
		settings: settings,
		stop:     make(chan struct{}),
	}
	c.state = app.UpdateState{
		Supported:      true,
		Enabled:        settings.enabled(),
		CurrentVersion: appVersion,
		Status:         string(wails.Updater.State()),
		SkippedVersion: settings.SkippedVersion,
		LastCheckedAt:  settings.LastCheckedAt,
		Path:           path,
	}
	c.listen(wails)
	go c.poll()
	log.Info("updates enabled", "version", appVersion, "feed", updateManifestURL)
	return c
}

// listen turns the framework's eleven events into one of ours. Everything the
// snapshot cannot read back off the Updater — the release that was found, how
// many bytes have arrived, what went wrong — is recorded here as it passes.
func (c *updateController) listen(wails *application.App) {
	release := func(e *application.CustomEvent) {
		rel, ok := e.Data.(*updater.Release)
		if !ok || rel == nil {
			c.publish(nil)
			return
		}
		c.publish(func(s *app.UpdateState) {
			s.AvailableVersion = rel.Version
			s.ReleaseName = rel.Name
			s.Notes = rel.Notes
			s.SizeBytes = rel.Artifact.Size
			s.Error, s.ErrorStage = "", ""
		})
	}

	wails.Event.On(updater.EventCheckStarted, func(*application.CustomEvent) {
		c.publish(func(s *app.UpdateState) { s.Error, s.ErrorStage = "", "" })
	})
	wails.Event.On(updater.EventUpdateAvailable, func(e *application.CustomEvent) {
		c.checked()
		release(e)
	})
	wails.Event.On(updater.EventNoUpdate, func(*application.CustomEvent) {
		c.checked()
		c.publish(func(s *app.UpdateState) {
			// Nothing on offer, so nothing left over from a previous find: a
			// version named for a release that is no longer offered is a button
			// that does nothing.
			s.AvailableVersion = ""
			s.ReleaseName = ""
			s.Notes = ""
			s.SizeBytes = 0
			s.Downloaded = 0
			s.Error, s.ErrorStage = "", ""
		})
	})
	wails.Event.On(updater.EventDownloadStarted, release)
	wails.Event.On(updater.EventDownloadProgress, func(e *application.CustomEvent) {
		p, ok := e.Data.(updater.Progress)
		if !ok {
			return
		}
		c.publish(func(s *app.UpdateState) {
			s.Downloaded = p.Written
			if p.Total > 0 {
				s.SizeBytes = p.Total
			}
		})
	})
	wails.Event.On(updater.EventDownloadComplete, release)
	wails.Event.On(updater.EventVerifying, release)
	wails.Event.On(updater.EventInstalling, release)
	wails.Event.On(updater.EventUpdateReady, release)
	wails.Event.On(updater.EventError, func(e *application.CustomEvent) {
		info, ok := e.Data.(updater.ErrorInfo)
		if !ok {
			c.publish(nil)
			return
		}
		c.log.Warn("update failed", "stage", string(info.Stage), "err", info.Message)
		c.publish(func(s *app.UpdateState) {
			s.Error, s.ErrorStage = info.Message, string(info.Stage)
		})
	})
}

// poll is the automatic half. It only ever checks: finding a release is worth
// telling somebody about, spending a hundred megabytes of somebody's connection
// without being asked is not, so the download waits for the button.
func (c *updateController) poll() {
	timer := time.NewTimer(updateFirstCheckDelay)
	defer timer.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-timer.C:
		}
		timer.Reset(updateCheckInterval)

		c.mu.Lock()
		enabled := c.settings.enabled()
		c.mu.Unlock()
		if !enabled {
			continue
		}
		// Already recorded by the error event and shown on the screen; a timer
		// is not the place to make it louder.
		_, _ = c.up.Check(context.Background())
	}
}

func (c *updateController) close() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.stop) })
}

// publish applies one change to the snapshot, re-reads the status off the
// Updater and tells the screen that something moved. The event carries nothing:
// like every other event here it says *that*, and the screen then asks — one
// way to learn the state rather than two that drift.
func (c *updateController) publish(change func(*app.UpdateState)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if change != nil {
		change(&c.state)
	}
	c.state.Status = string(c.up.State())
	c.state.Enabled = c.settings.enabled()
	c.state.SkippedVersion = c.settings.SkippedVersion
	c.state.LastCheckedAt = c.settings.LastCheckedAt
	c.mu.Unlock()

	c.core.Emit(app.EventUpdate, nil)
}

// checked records that the feed answered, whatever it answered. It is the one
// thing worth writing to disk on every check: it is how the screen says when it
// last looked without looking again.
func (c *updateController) checked() {
	c.mu.Lock()
	c.settings.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	settings := c.settings
	c.mu.Unlock()
	if err := writeUpdateSettings(c.path, settings); err != nil {
		c.log.Warn("could not write the update settings", "err", err)
	}
}

// State is what the screen draws.
func (c *updateController) State() app.UpdateState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Status = string(c.up.State())
	return c.state
}

// SetEnabled turns the automatic check on or off. It takes effect on the next
// tick rather than at the next launch, which is the whole reason the timer is
// ours.
func (c *updateController) SetEnabled(enabled bool) (app.UpdateState, error) {
	c.mu.Lock()
	c.settings.Enabled = &enabled
	settings := c.settings
	c.mu.Unlock()
	if err := writeUpdateSettings(c.path, settings); err != nil {
		return c.State(), err
	}
	c.publish(nil)
	return c.State(), nil
}

// Skip records the release now on offer as one to stop mentioning. Both halves
// matter: the Updater treats it as up to date from here on, and the file is
// what makes that survive a restart.
func (c *updateController) Skip() error {
	c.mu.Lock()
	version := c.state.AvailableVersion
	c.mu.Unlock()
	if version == "" {
		return msg.Err("update.nothingToSkip")
	}

	c.up.SkipVersion(version)

	c.mu.Lock()
	c.settings.SkippedVersion = version
	settings := c.settings
	c.mu.Unlock()
	if err := writeUpdateSettings(c.path, settings); err != nil {
		return err
	}
	c.publish(func(s *app.UpdateState) {
		s.AvailableVersion = ""
		s.ReleaseName = ""
		s.Notes = ""
		s.SizeBytes = 0
		s.Downloaded = 0
	})
	return nil
}

// Check asks the feed. It returns as soon as the request is under way: the
// answer arrives as the event, so the screen draws «checking» from the same state
// machine that draws everything else rather than from a promise.
func (c *updateController) Check() error {
	go func() {
		if _, err := c.up.Check(context.Background()); err != nil {
			c.failed(updater.StageCheck, err)
		}
	}()
	return nil
}

// Install downloads, verifies and stages what the last check found. Same
// bargain as Check: the progress is the event.
func (c *updateController) Install() error {
	go func() {
		if err := c.up.DownloadAndInstall(context.Background()); err != nil {
			c.failed(updater.StageDownload, err)
		}
	}()
	return nil
}

// failed puts an error the caller cannot see onto the screen. Most failures
// arrive as the framework's own error event and land in the snapshot that way;
// the ones that do not are those refused before any step began — nothing to
// download, a download already running — and without this they would be a line
// in a log nobody has open and a button that appeared to do nothing.
func (c *updateController) failed(stage updater.Stage, err error) {
	c.log.Warn("update failed", "stage", string(stage), "err", err)
	c.publish(func(s *app.UpdateState) {
		s.Error, s.ErrorStage = err.Error(), string(stage)
	})
}

// Restart hands over to the framework's helper: it spawns a copy of this binary
// in helper mode, we quit, it waits for us to be gone, swaps the bundle and
// launches the new one. The helper is why main() calls
// updater.HandleHelperMode before anything else.
func (c *updateController) Restart() error {
	if err := c.up.Restart(context.Background()); err != nil {
		return msg.Wrap(err, "update.restartFailed")
	}
	return nil
}
