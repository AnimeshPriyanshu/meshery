package academy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/meshery/meshery/mesheryctl/pkg/utils"
)

// scaffoldEffect is one filesystem effect a scaffold run would perform.
// Overwrite records which pre-existing path the effect would replace, so
// dry-run can report overwrites distinctly from creations.
type scaffoldEffect struct {
	path      string
	isDir     bool
	overwrite bool
	content   []byte
}

// ScaffoldPlan is the complete set of filesystem effects a create run would
// perform, collected by dryRunScaffoldView. Nothing is written to disk while
// the plan is built.
type ScaffoldPlan struct {
	effects []scaffoldEffect
	// index maps canonical path -> position in effects for existence checks.
	index map[string]int
}

func newScaffoldPlan() *ScaffoldPlan {
	return &ScaffoldPlan{index: make(map[string]int)}
}

// record appends an effect, collapsing repeats onto the same path: creating a
// planned directory twice, or writing a planned file again, is a no-op just as
// it is on a real filesystem (os.MkdirAll succeeds, os.Create truncates).
func (p *ScaffoldPlan) record(e scaffoldEffect) {
	key := canonicalPath(e.path)
	if pos, ok := p.index[key]; ok {
		p.effects[pos].overwrite = p.effects[pos].overwrite || e.overwrite
		return
	}
	e.path = key
	p.index[key] = len(p.effects)
	p.effects = append(p.effects, e)
}

func (p *ScaffoldPlan) exists(path string) bool {
	_, ok := p.index[canonicalPath(path)]
	return ok
}

// isEmpty reports whether the plan would touch the filesystem at all.
func (p *ScaffoldPlan) isEmpty() bool {
	return len(p.effects) == 0
}

// overwrites lists the effects that would replace existing files or directories.
func (p *ScaffoldPlan) overwrites() []scaffoldEffect {
	var out []scaffoldEffect
	for _, e := range p.effects {
		if e.overwrite {
			out = append(out, e)
		}
	}
	return out
}

// canonicalPath normalizes a path for plan bookkeeping: absolute for stable
// comparisons, forward slashes, and no trailing separator.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)
	return filepath.ToSlash(abs)
}

// dryRunScaffoldView is a scaffoldView that never touches the filesystem:
// creates, mkdirs and stats are answered from the in-memory plan, and readFile
// falls back to disk only for files that existed before the dry run. Planned
// content is answered from the plan so later nodes in the same run observe
// earlier ones exactly as they would on disk.
type dryRunScaffoldView struct {
	plan *ScaffoldPlan
}

func newDryRunScaffoldView() *dryRunScaffoldView {
	return &dryRunScaffoldView{plan: newScaffoldPlan()}
}

func (d *dryRunScaffoldView) mkdirAll(path string) error {
	_, statErr := os.Stat(path)
	d.plan.record(scaffoldEffect{path: path, isDir: true, overwrite: statErr == nil})
	return nil
}

func (d *dryRunScaffoldView) stat(path string) error {
	if d.plan.exists(path) {
		return nil
	}
	_, err := os.Stat(path)
	return err
}

func (d *dryRunScaffoldView) readFile(path string) ([]byte, error) {
	key := canonicalPath(path)
	if pos, ok := d.plan.index[key]; ok {
		e := d.plan.effects[pos]
		if e.isDir {
			return nil, fmt.Errorf("read %s: is a directory", path)
		}
		return e.content, nil
	}
	return os.ReadFile(path)
}

func (d *dryRunScaffoldView) readDir(path string) ([]dirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// The directory may exist only in the plan (created earlier in this
		// same dry run); fall through and derive entries from the plan.
		entries = nil
	}

	out := make([]dirEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		out = append(out, dirEntry{name: entry.Name(), isDir: entry.IsDir()})
		seen[entry.Name()] = true
	}

	// Add planned children created inside this directory during the run,
	// skipping ones already on disk. A planned child of an on-disk directory
	// is not visible to a real os.ReadDir either — but in a real run the
	// mkdir/create would have happened already, so the plan is the truth here.
	key := canonicalPath(path) + "/"
	for p, pos := range d.plan.index {
		e := d.plan.effects[pos]
		if !strings.HasPrefix(p, key) {
			continue
		}
		rest := strings.TrimPrefix(p, key)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		if seen[rest] {
			continue
		}
		seen[rest] = true
		out = append(out, dirEntry{name: rest, isDir: e.isDir})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func (d *dryRunScaffoldView) create(path string, content []byte) error {
	_, statErr := os.Stat(path)
	e := scaffoldEffect{path: canonicalPath(path), content: content, overwrite: statErr == nil}
	d.plan.record(e)
	return nil
}

// printScaffoldPlan renders the dry-run report. The flag combination decides
// how collisions are communicated, mirroring what a real run would do:
// without --force a collision is a hard error (the plan is truncated at the
// collision, exactly where a real run would have stopped) and the same
// errScaffoldExists is returned; with --force it is an announced overwrite —
// and the dry run itself still writes nothing. Pre-existing directories are
// silently reused, exactly as a real run does.
func printScaffoldPlan(plan *ScaffoldPlan, force bool) error {
	if plan.isEmpty() {
		utils.Log.Info("Dry run — no files would be created.")
		return nil
	}

	var fileOverwrites []scaffoldEffect
	for _, e := range plan.overwrites() {
		if !e.isDir {
			fileOverwrites = append(fileOverwrites, e)
		}
	}

	if len(fileOverwrites) > 0 && !force {
		// A real run without --force fails on the first existing file; report
		// the same error and stop there.
		err := errScaffoldExists(fileOverwrites[0].path)
		utils.Log.Error(err)
		utils.Log.Info("Dry run — no files were created. Re-run with --force to overwrite, or without --dry-run after reviewing.")
		return err
	}

	for _, e := range fileOverwrites {
		utils.Log.Warnf("Would overwrite: %s", e.path)
	}

	wouldCreate := 0
	for _, e := range plan.effects {
		if e.overwrite || e.isDir {
			continue
		}
		if wouldCreate == 0 {
			utils.Log.Info("Would create:")
		}
		utils.Log.Infof("  %s", e.path)
		wouldCreate++
	}

	switch {
	case wouldCreate > 0 && len(fileOverwrites) > 0:
		utils.Log.Infof("Dry run — no files were created. %d file(s) would be written, %d overwritten.", wouldCreate, len(fileOverwrites))
	case wouldCreate > 0:
		utils.Log.Infof("Dry run — no files were created. %d file(s) would be written.", wouldCreate)
	case len(fileOverwrites) > 0:
		utils.Log.Infof("Dry run — no files were created. %d file(s) would be overwritten.", len(fileOverwrites))
	default:
		utils.Log.Info("Dry run — no files would be created.")
	}
	return nil
}

// printScaffoldPlanVerbose additionally prints the rendered frontmatter/content
// of each planned file so the user can inspect exactly what would be written.
func printScaffoldPlanVerbose(plan *ScaffoldPlan) {
	var buf bytes.Buffer
	var files int
	for _, e := range plan.effects {
		if e.isDir {
			continue
		}
		fmt.Fprintf(&buf, "--- %s ---\n", e.path)
		buf.Write(e.content)
		files++
	}
	if files == 0 {
		return
	}
	utils.Log.Info("Rendered content:")
	utils.Log.Info(strings.TrimRight(buf.String(), "\n"))
}
