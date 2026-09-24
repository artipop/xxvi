package inbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// The demo source: a file of items and a way to add one by hand.
//
// It exists because the whole of what a source has to demonstrate — items with
// stable ids, versions that mean "this changed", rules that sort them — needs
// no network, no account and no token. A real connector replaces the reading
// half and nothing else: everything after Item is already the same path.

// PluginDemo is the plugin name a source names to be read this way.
const PluginDemo = "demo"

// ConfigPath is the source config key holding the file to read.
const ConfigPath = "path"

// DemoFile is the file name a demo source reads when its config names none.
const DemoFile = "items.jsonl"

// Poller reads the enabled sources on a timer and hands what they find to the
// pipeline.
type Poller struct {
	store    *store.Store
	pipeline *Pipeline
	log      *slog.Logger

	mu      sync.Mutex
	rootCtx context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

func NewPoller(st *store.Store, p *Pipeline, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{store: st, pipeline: p, log: log}
}

// Start begins polling. One goroutine for all sources rather than one each: a
// handful of files read on a timer is not something to spend goroutines on, and
// a single loop makes "reload the registry" mean one thing.
func (p *Poller) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		return
	}
	p.rootCtx, p.stop = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.loop(p.rootCtx)
}

// Stop ends polling and waits for the loop.
func (p *Poller) Stop() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()
	if stop != nil {
		stop()
		p.wg.Wait()
	}
}

// tick is how often the loop looks at the registry. A source's own interval is
// respected on top of it, so a source asking for a minute is read once a minute
// however often the loop wakes.
const tick = 10 * time.Second

func (p *Poller) loop(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	lastRead := map[string]time.Time{}
	p.pollAll(lastRead)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAll(lastRead)
		}
	}
}

func (p *Poller) pollAll(lastRead map[string]time.Time) {
	sources, err := p.store.Sources()
	if err != nil {
		p.log.Error("could not read sources", "err", err)
		return
	}
	for _, src := range sources {
		if !src.Enabled || src.Plugin != PluginDemo {
			continue
		}
		interval := time.Duration(src.IntervalSeconds) * time.Second
		if interval > 0 && time.Since(lastRead[src.Name]) < interval {
			continue
		}
		lastRead[src.Name] = time.Now()
		if err := p.Poll(src); err != nil {
			p.log.Warn("source not read", "source", src.Name, "err", err)
		}
	}
}

// Poll reads one demo source now. It is also what the UI's «read now»
// calls, so a person never has to wait out an interval to see a change.
func (p *Poller) Poll(src model.Source) error {
	path := DemoPath(src)
	items, err := ReadItems(path)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	_, err = p.pipeline.Ingest(src.Name, items)
	return err
}

// PollByName reads one source by name.
func (p *Poller) PollByName(name string) error {
	src, err := p.store.Source(name)
	if err != nil {
		return err
	}
	if src.Plugin != PluginDemo {
		return msg.Err("source.notFromFile", "source", src.Name)
	}
	return p.Poll(src)
}

// DemoPath is the file a demo source reads.
func DemoPath(src model.Source) string {
	if path := strings.TrimSpace(src.Config[ConfigPath]); path != "" {
		return path
	}
	return DemoFile
}

// ReadItems reads a JSONL file of items. A missing file is not an error: a
// source configured before anybody put anything in it is a source with nothing
// to bring, not a broken one.
//
// A malformed line is skipped rather than failing the file, for the same reason
// a bad item does not fail a batch — and the line number is reported, because
// "somewhere in this file" is not something a person can act on.
func ReadItems(path string) ([]model.Item, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var (
		items []model.Item
		bad   []string
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		var item model.Item
		if err := json.Unmarshal([]byte(text), &item); err != nil {
			bad = append(bad, strconv.Itoa(line))
			continue
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bad) > 0 {
		return items, msg.Err("source.badLines", "path", path, "lines", strings.Join(bad, ", "))
	}
	return items, nil
}

// AppendItem writes an item into a demo source's file, which is how the UI adds
// one by hand. It goes through the file rather than straight into a card so
// that a hand-added item is an item like any other: it meets the same rules and
// keeps working after the file is read again.
func AppendItem(path string, item model.Item) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create the source folder: %w", err)
		}
	}
	item = item.WithFallbackID()
	if item.At.IsZero() {
		item.At = time.Now().UTC()
	}
	line, err := json.Marshal(item)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to %s: %w", path, err)
	}
	return nil
}
