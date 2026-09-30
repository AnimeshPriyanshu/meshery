package academy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mesheryctlflags "github.com/meshery/meshery/mesheryctl/internal/cli/pkg/flags"
	"github.com/meshery/meshery/mesheryctl/pkg/utils"
	"github.com/meshery/meshkit/errors"
)

// setupDryRunTest switches to a fresh temp working directory and installs the
// flag validators. Flag values themselves are reset per run in runCreateArgs,
// because createAcademyFlags is a package-level global that cobra/pflag keeps
// across Execute() calls.
func setupDryRunTest(t *testing.T) {
	t.Helper()

	t.Chdir(t.TempDir())

	mesheryctlflags.InitValidators(AcademyCmd)
}

// runCreateArgs resets all create flags to their defaults, executes the
// academy command with the given args, and returns (captured output, error).
func runCreateArgs(t *testing.T, args []string) (string, error) {
	t.Helper()

	defaults := map[string]string{
		"type": "", "title": "", "description": "", "into": "",
		"org": "", "level": "", "category": "", "tags": "",
		"id": "", "force": "false", "banner": "", "draft": "false",
		"dry-run": "false",
	}
	for name, value := range defaults {
		if err := createCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("failed to reset flag %q: %v", name, err)
		}
	}

	buf := utils.SetupMeshkitLoggerTesting(t, false)
	AcademyCmd.SetArgs(args)
	err := AcademyCmd.Execute()
	return buf.String(), err
}

func requireNoFilesystemEntry(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("dry-run must not create %s, but it exists", p)
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected stat error for %s: %v", p, err)
		}
	}
}

// snapshotTree records the byte-for-byte content of every regular file under
// root, keyed by path relative to root (forward slashes). Empty when root does
// not exist.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	}); err != nil {
		t.Fatalf("snapshot of %s failed: %v", root, err)
	}
	return files
}

// requireTreeUnchanged asserts the on-disk tree under root is byte-for-byte
// identical to the given snapshot — content, presence and file count.
func requireTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	if !reflect.DeepEqual(before, after) {
		removed, added, modified := diffSnapshots(before, after)
		t.Errorf("filesystem changed under %s: added=%v removed=%v modified=%v", root, added, removed, modified)
	}
}

func diffSnapshots(before, after map[string]string) (removed, added, modified []string) {
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			removed = append(removed, path)
		} else if got != want {
			modified = append(modified, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			added = append(added, path)
		}
	}
	return removed, added, modified
}

// extractRenderedFile returns the rendered file content printed in the
// "Rendered content:" section of a dry-run capture. dryrun.go prints one
// "--- <path> ---" header line followed by the file content, per planned file,
// with paths as produced by canonicalPath (absolute).
func extractRenderedFile(t *testing.T, out, path string) string {
	t.Helper()
	start := strings.Index(out, "--- "+path+" ---\n")
	if start < 0 {
		t.Fatalf("rendered content section for %s not found in output:\n%s", path, out)
	}
	start += len("--- " + path + " ---\n")
	end := strings.Index(out[start:], "\n--- ")
	if end < 0 {
		// Last section in the capture — runs to the end of the buffer.
		return out[start:]
	}
	return out[start : start+end+1]
}

// requireWeight asserts weight: <n> is rendered in content.
func requireWeight(t *testing.T, content string, weight int) {
	t.Helper()
	want := fmt.Sprintf("weight: %d\n", weight)
	if !strings.Contains(content, want) {
		t.Errorf("expected %q in rendered content, got:\n%s", want, content)
	}
}

// TestAcademyCreateDryRun covers the --dry-run flag end to end through the
// command. Every test asserts on real filesystem state, not just output.
func TestAcademyCreateDryRun(t *testing.T) {
	setupDryRunTest(t)

	t.Run("simple learning path creates nothing", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		out, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Kubernetes Basics",
			"--description", "Desc", "--org", "11111111-1111-1111-1111-111111111111", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if !strings.Contains(out, "Dry run") {
			t.Errorf("output should mention dry run, got: %s", out)
		}
		if !strings.Contains(out, "kubernetes-basics") {
			t.Errorf("output should show the planned path, got: %s", out)
		}
		// Nothing may exist: not the content tree, not even the org directory.
		requireNoFilesystemEntry(t,
			filepath.Join(wd, "content"),
			filepath.Join(wd, "content", "learning-paths"),
			filepath.Join(wd, "content", "learning-paths", "11111111-1111-1111-1111-111111111111"),
			filepath.Join(wd, "content", "learning-paths", "11111111-1111-1111-1111-111111111111", "kubernetes-basics"),
			filepath.Join(wd, "content", "learning-paths", "11111111-1111-1111-1111-111111111111", "kubernetes-basics", "_index.md"),
		)
	})

	t.Run("nested learning path tree fully planned", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		out, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Nested Path",
			"--description", "Desc", "--org", "22222222-2222-2222-2222-222222222222", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		// learning-path -> course -> module -> page
		for _, want := range []string{
			"nested-path/_index.md",
			"course-1/_index.md",
			"module-1/_index.md",
			"page-1/_index.md",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("planned output missing %s, got:\n%s", want, out)
			}
		}
		requireNoFilesystemEntry(t, filepath.Join(wd, "content"))
	})

	t.Run("challenge scaffolding fully planned", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		out, err := runCreateArgs(t, []string{
			"create", "challenge", "--title", "Dry Challenge",
			"--description", "Desc", "--org", "33333333-3333-3333-3333-333333333333", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		for _, want := range []string{
			"dry-challenge/_index.md",
			"dry-challenge/lab/_index.md",
			"dry-challenge/exam/_index.md",
			"dry-challenge/content/description/_index.md",
			"dry-challenge/content/getting-started/_index.md",
			"dry-challenge/content/faq/_index.md",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("planned output missing %s, got:\n%s", want, out)
			}
		}
		requireNoFilesystemEntry(t, filepath.Join(wd, "content"))
	})

	t.Run("single node dry run into existing tree", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		// Build a real parent tree first (normal mode).
		parent := filepath.Join(wd, "tree")
		if err := os.MkdirAll(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, "_index.md"),
			[]byte("---\ntype: \"learning-path\"\nlevel: \"advanced\"\ntags: []\n---\n"), 0644); err != nil {
			t.Fatal(err)
		}
		before := map[string]string{}
		for _, name := range []string{"_index.md"} {
			data, err := os.ReadFile(filepath.Join(parent, name))
			if err != nil {
				t.Fatal(err)
			}
			before[name] = string(data)
		}

		out, err := runCreateArgs(t, []string{
			"create", "course", "Dry Course", "--description", "Desc", "--into", parent, "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if !strings.Contains(out, "dry-course/_index.md") {
			t.Errorf("planned output missing dry-course, got:\n%s", out)
		}
		// The planned course must inherit level "advanced" from the parent in
		// the rendered content, proving metadata inheritance ran.
		if !strings.Contains(out, `level: "advanced"`) {
			t.Errorf("dry run should show inherited level, got:\n%s", out)
		}
		// Parent unchanged; no child created.
		requireNoFilesystemEntry(t, filepath.Join(parent, "dry-course"))
		data, err := os.ReadFile(filepath.Join(parent, "_index.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != before["_index.md"] {
			t.Errorf("dry-run modified the parent _index.md")
		}
	})

	t.Run("dry run does not overwrite with force", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		// Create a real learning path first.
		_, err = runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Force Target",
			"--description", "Desc", "--org", "44444444-4444-4444-4444-444444444444",
		})
		if err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		treeRoot := filepath.Join(wd, "content")
		target := filepath.Join(treeRoot, "learning-paths", "44444444-4444-4444-4444-444444444444", "force-target", "_index.md")
		original, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("setup failed reading target: %v", err)
		}
		// Full-tree snapshot BEFORE the dry run: --dry-run --force must not
		// mutate anything, anywhere, not just the overwrite target.
		before := snapshotTree(t, treeRoot)

		// Dry run with --force and a different description must not modify it.
		out, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Force Target",
			"--description", "CHANGED", "--org", "44444444-4444-4444-4444-444444444444", "--force", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if !strings.Contains(out, "Would overwrite") {
			t.Errorf("dry-run --force should announce the overwrite, got:\n%s", out)
		}
		after, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(original) {
			t.Errorf("dry-run --force modified the existing file")
		}
		// And the announced content shows CHANGED (what would be written).
		if !strings.Contains(out, "CHANGED") {
			t.Errorf("rendered content should show what would be written, got:\n%s", out)
		}
		// The whole tree must be untouched, not just the overwrite target.
		requireTreeUnchanged(t, treeRoot, before)
	})

	t.Run("collision without force reports same error", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		_, err = runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Collision Target",
			"--description", "Desc", "--org", "55555555-5555-5555-5555-555555555555",
		})
		if err != nil {
			t.Fatalf("setup failed: %v", err)
		}

		// Snapshot the tree the setup run produced; the dry run must leave it
		// byte-for-byte identical.
		treeRoot := filepath.Join(wd, "content")
		before := map[string]string{}
		if err := filepath.Walk(treeRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			before[path] = string(data)
			return nil
		}); err != nil {
			t.Fatalf("setup snapshot failed: %v", err)
		}

		out, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Collision Target",
			"--description", "Desc", "--org", "55555555-5555-5555-5555-555555555555", "--dry-run",
		})
		if err == nil {
			t.Fatal("expected collision error without --force, got success")
		}
		if code := errors.GetCode(err); code != ErrScaffoldExistsCode {
			t.Errorf("expected error code %q, got %q", ErrScaffoldExistsCode, code)
		}
		if !strings.Contains(out, "Dry run") {
			t.Errorf("collision output should still mention dry run, got:\n%s", out)
		}
		for path, want := range before {
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("dry-run removed or broke %s: %v", path, rerr)
				continue
			}
			if string(data) != want {
				t.Errorf("dry-run modified %s", path)
			}
		}
		// No new files either.
		afterCount := 0
		if err := filepath.Walk(treeRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				afterCount++
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if afterCount != len(before) {
			t.Errorf("dry-run changed the file count: before=%d after=%d", len(before), afterCount)
		}
	})

	t.Run("validation errors identical to normal mode", func(t *testing.T) {
		_, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "No Org", "--description", "Desc", "--dry-run",
		})
		if err == nil {
			t.Fatal("expected missing org error")
		}
		if code := errors.GetCode(err); code != ErrMissingOrgIDCode {
			t.Errorf("expected %q, got %q", ErrMissingOrgIDCode, code)
		}

		_, err = runCreateArgs(t, []string{
			"create", "course", "Orphan", "--description", "Desc", "--dry-run",
		})
		if err == nil {
			t.Fatal("expected missing into error")
		}
		if code := errors.GetCode(err); code != ErrMissingIntoCode {
			t.Errorf("expected %q, got %q", ErrMissingIntoCode, code)
		}

		_, err = runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Bad Level", "--description", "Desc",
			"--org", "66666666-6666-6666-6666-666666666666", "--level", "wizard", "--dry-run",
		})
		if err == nil {
			t.Fatal("expected invalid level error")
		}
		if code := errors.GetCode(err); code != ErrInvalidLevelCode {
			t.Errorf("expected %q, got %q", ErrInvalidLevelCode, code)
		}
	})

	t.Run("rendered content shown in dry run", func(t *testing.T) {
		out, err := runCreateArgs(t, []string{
			"create", "learning-path", "--title", "Rendered Check",
			"--description", "Rendered description", "--org", "77777777-7777-7777-7777-777777777777", "--draft", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if !strings.Contains(out, "Rendered content:") {
			t.Errorf("dry run should show rendered content, got:\n%s", out)
		}
		for _, want := range []string{`title: "Rendered Check"`, "draft: true", `id: "REPLACE_WITH_INSTRUCTOR_CONSOLE_ID"`} {
			if !strings.Contains(out, want) {
				t.Errorf("rendered content missing %q, got:\n%s", want, out)
			}
		}
	})

	t.Run("dry run leaves no directories anywhere", func(t *testing.T) {
		// Fresh working directory: earlier subtests legitimately created real
		// scaffolds in the suite's dir.
		wd := t.TempDir()
		t.Chdir(wd)
		_, err := runCreateArgs(t, []string{
			"create", "certification", "--title", "No Dir Cert",
			"--description", "Desc", "--org", "88888888-8888-8888-8888-888888888888", "--dry-run",
		})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		requireNoFilesystemEntry(t,
			filepath.Join(wd, "content"),
			filepath.Join(wd, "content", "certifications"),
			filepath.Join(wd, "content", "certifications", "88888888-8888-8888-8888-888888888888"),
			filepath.Join(wd, "content", "certifications", "88888888-8888-8888-8888-888888888888", "no-dir-cert"),
		)
	})
}

// TestAcademyCreateNormalModeUnchanged pins the invariant that normal create
// still writes after the dry-run refactor: a plain create must produce the
// identical tree the dry run only planned.
func TestAcademyCreateNormalModeUnchanged(t *testing.T) {
	setupDryRunTest(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCreateArgs(t, []string{
		"create", "learning-path", "--title", "Real Path",
		"--description", "Desc", "--org", "99999999-9999-9999-9999-999999999999",
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if strings.Contains(out, "Dry run") {
		t.Errorf("normal mode must not print dry-run output, got:\n%s", out)
	}

	// The full tree must exist now.
	for _, p := range []string{
		filepath.Join(wd, "content", "learning-paths", "99999999-9999-9999-9999-999999999999", "real-path", "_index.md"),
		filepath.Join(wd, "content", "learning-paths", "99999999-9999-9999-9999-999999999999", "real-path", "course-1", "_index.md"),
		filepath.Join(wd, "content", "learning-paths", "99999999-9999-9999-9999-999999999999", "real-path", "course-1", "module-1", "_index.md"),
		filepath.Join(wd, "content", "learning-paths", "99999999-9999-9999-9999-999999999999", "real-path", "course-1", "module-1", "page-1", "_index.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("normal create should have created %s: %v", p, err)
		}
	}

	// Re-run in normal mode: same collision error code as before the refactor.
	_, err = runCreateArgs(t, []string{
		"create", "learning-path", "--title", "Real Path",
		"--description", "Desc", "--org", "99999999-9999-9999-9999-999999999999",
	})
	if err == nil {
		t.Fatal("expected collision error on re-run")
	}
	if code := errors.GetCode(err); code != ErrScaffoldExistsCode {
		t.Errorf("expected %q, got %q", ErrScaffoldExistsCode, code)
	}

	// And force overwrites in normal mode, as before.
	_, err = runCreateArgs(t, []string{
		"create", "learning-path", "--title", "Real Path",
		"--description", "Desc", "--org", "99999999-9999-9999-9999-999999999999", "--force",
	})
	if err != nil {
		t.Fatalf("expected force overwrite to succeed, got: %v", err)
	}
}

// withDryRun returns args with --dry-run appended or stripped.
func withDryRun(args []string, dry bool) []string {
	if dry {
		return append(append([]string{}, args...), "--dry-run")
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--dry-run" {
			out = append(out, a)
		}
	}
	return out
}

// TestAcademyCreateDryRunParity proves the core dry-run invariant: the plan
// (paths + rendered content printed by --dry-run) is exactly what a normal
// run writes from the same starting state. Both runs execute the identical
// command against identical pristine trees, and the on-disk result is compared
// against the prediction path-for-path, byte-for-byte — no more, no less.
func TestAcademyCreateDryRunParity(t *testing.T) {
	setupDryRunTest(t)

	const (
		org   = "12121212-1212-1212-1212-121212121212"
		title = "Parity Path"
	)
	baseArgs := []string{
		"create", "--type", "learning-path", "--title", title,
		"--description", "Desc", "--org", org, "--level", "intermediate",
	}
	rels := []string{
		"content/learning-paths/" + org + "/parity-path/_index.md",
		"content/learning-paths/" + org + "/parity-path/course-1/_index.md",
		"content/learning-paths/" + org + "/parity-path/course-1/module-1/_index.md",
		"content/learning-paths/" + org + "/parity-path/course-1/module-1/page-1/_index.md",
	}

	// --- Pass 1: dry-run from a pristine tree, capture the plan ---
	dryWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCreateArgs(t, withDryRun(baseArgs, true))
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	plannedContent := make(map[string]string, len(rels))
	for _, rel := range rels {
		// canonicalPath prints forward-slash absolute paths; build the search
		// key the same way regardless of host OS separators.
		abs := filepath.ToSlash(filepath.Join(dryWd, filepath.FromSlash(rel)))
		plannedContent[rel] = extractRenderedFile(t, out, abs)
	}
	// The dry run must have written nothing.
	requireNoFilesystemEntry(t, filepath.Join(dryWd, "content"))

	// --- Pass 2: an identical pristine tree, run for real ---
	// The dry run created nothing, so a fresh temp dir IS the same starting
	// state pass 1 saw.
	wd := t.TempDir()
	t.Chdir(wd)
	out, err = runCreateArgs(t, withDryRun(baseArgs, false))
	if err != nil {
		t.Fatalf("real run failed: %v", err)
	}
	if strings.Contains(out, "Dry run") {
		t.Errorf("real run must not print dry-run output, got:\n%s", out)
	}

	// The real tree must contain exactly the planned files with exactly the
	// planned content.
	got := snapshotTree(t, wd)
	if !reflect.DeepEqual(got, plannedContent) {
		missing, extra, changed := diffSnapshots(plannedContent, got)
		t.Errorf("real run diverged from plan: missing=%v extra=%v changed=%v", missing, extra, changed)
	}
}

// TestAcademyCreateDryRunMidTreeCollision covers a multi-node tree where a
// LATER node collides with an existing file: the dry run must stop at the
// collision with the same error a real run would return, report the partial
// plan (nodes planned before the collision), and leave the disk byte-for-byte
// untouched — no partial files or directories from the earlier planned nodes.
func TestAcademyCreateDryRunMidTreeCollision(t *testing.T) {
	setupDryRunTest(t)

	const org = "13131313-1313-1313-1313-131313131313"
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Pristine tree with a pre-existing course-1 inside the (not yet
	// existing) path directory: the starter tree plans root -> course ->
	// module -> page, so the collision hits the SECOND node, after the root
	// was already planned.
	pathDir := filepath.Join(wd, "content", "learning-paths", org, "mid-tree")
	if err := os.MkdirAll(filepath.Join(pathDir, "course-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathDir, "course-1", "_index.md"),
		[]byte("---\ntype: \"course\"\nlevel: \"beginner\"\ntags: []\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	treeRoot := filepath.Join(wd, "content")
	before := snapshotTree(t, treeRoot)

	out, err := runCreateArgs(t, []string{
		"create", "--type", "learning-path", "--title", "Mid Tree",
		"--description", "Desc", "--org", org, "--dry-run",
	})
	if err == nil {
		t.Fatal("expected collision error without --force, got success")
	}
	if code := errors.GetCode(err); code != ErrScaffoldExistsCode {
		t.Errorf("expected error code %q, got %q", ErrScaffoldExistsCode, code)
	}
	// The partial plan reports what a real run would have written before
	// stopping at the collision (the root _index.md).
	if !strings.Contains(out, "mid-tree/_index.md") {
		t.Errorf("partial plan should include nodes planned before the collision, got:\n%s", out)
	}
	// And the disk must be byte-for-byte untouched — no partial writes from
	// the earlier planned nodes.
	requireTreeUnchanged(t, treeRoot, before)
}

// TestAcademyCreateDryRunTestNumbering covers test-N numbering under a
// certification through the CLI: numbering must skip numbers already on disk
// and the dry run must create nothing while predicting the next number.
func TestAcademyCreateDryRunTestNumbering(t *testing.T) {
	setupDryRunTest(t)

	const org = "14141414-1414-1414-1414-141414141414"
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	certDir := filepath.Join(wd, "content", "certifications", org, "numbered-cert")

	// Real certification with one test on disk.
	_, err = runCreateArgs(t, []string{
		"create", "--type", "certification", "--title", "Numbered Cert",
		"--description", "Desc", "--org", org,
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	_, err = runCreateArgs(t, []string{
		"create", "test", "Cert Test 1", "--description", "Desc", "--into", certDir,
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	t.Run("skips numbers already on disk", func(t *testing.T) {
		out, err := runCreateArgs(t, []string{
			"create", "test", "Cert Test 2", "--description", "Desc", "--into", certDir, "--dry-run",
		})
		if err != nil {
			t.Fatalf("dry run failed: %v", err)
		}
		if !strings.Contains(out, "test-2/_index.md") {
			t.Errorf("dry run should plan test-2 (test-1 exists on disk), got:\n%s", out)
		}
		if strings.Contains(out, "test-1/_index.md") {
			t.Errorf("plan should only contain planned files, not the on-disk test-1, got:\n%s", out)
		}
		requireNoFilesystemEntry(t, filepath.Join(certDir, "test-2"))
	})

	t.Run("advances when disk state advances", func(t *testing.T) {
		// Create test-2 for real, then the next dry run must plan test-3.
		_, err := runCreateArgs(t, []string{
			"create", "test", "Cert Test 2", "--description", "Desc", "--into", certDir,
		})
		if err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		out, err := runCreateArgs(t, []string{
			"create", "test", "Cert Test 3", "--description", "Desc", "--into", certDir, "--dry-run",
		})
		if err != nil {
			t.Fatalf("dry run failed: %v", err)
		}
		if !strings.Contains(out, "test-3/_index.md") {
			t.Errorf("dry run should plan test-3 after test-1 and test-2 exist, got:\n%s", out)
		}
		requireNoFilesystemEntry(t, filepath.Join(certDir, "test-3"))
	})
}

// TestAcademyCreateDryRunTestNumberingPlannedOnly exercises the situation the
// filesystem alone cannot represent: within a single dry run, test-1 is
// planned but never written, and the very next node must number itself test-2
// by consulting the plan (v.stat), not the disk. Drives the same scaffoldNode
// the CLI uses, with the dry-run view, twice in one run.
func TestAcademyCreateDryRunTestNumberingPlannedOnly(t *testing.T) {
	setupDryRunTest(t)

	const org = "16161616-1616-1616-1616-161616161616"
	certDir := filepath.Join("content", "certifications", org, "planned-cert")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "_index.md"),
		[]byte("---\ntype: \"certification\"\nlevel: \"beginner\"\ntags: []\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dry := newDryRunScaffoldView()
	base := ScaffoldOptions{TargetDir: certDir, DryRun: true}
	first, second := base, base
	first.Type, first.Title, first.Description = Test, "Planned Test One", "Desc"
	second.Type, second.Title, second.Description = Test, "Planned Test Two", "Desc"

	if err := scaffoldNode(dry, first, ""); err != nil {
		t.Fatalf("first planned test failed: %v", err)
	}
	if err := scaffoldNode(dry, second, ""); err != nil {
		t.Fatalf("second planned test failed: %v", err)
	}

	// test-1 exists only in the plan — the first node's folder was never
	// created on disk, yet the second node numbered itself test-2.
	firstContent, err := dry.readFile(filepath.Join(certDir, "test-1", "_index.md"))
	if err != nil {
		t.Fatalf("planned test-1 not found in plan: %v", err)
	}
	if !strings.Contains(string(firstContent), `title: "Planned Test One"`) {
		t.Errorf("unexpected planned test-1 content:\n%s", firstContent)
	}
	secondContent, err := dry.readFile(filepath.Join(certDir, "test-2", "_index.md"))
	if err != nil {
		t.Fatalf("second test must be numbered test-2 based on the planned test-1 (which does not exist on disk): %v", err)
	}
	if !strings.Contains(string(secondContent), `title: "Planned Test Two"`) {
		t.Errorf("unexpected planned test-2 content:\n%s", secondContent)
	}
	// And none of it touched the disk.
	requireNoFilesystemEntry(t,
		filepath.Join(certDir, "test-1"),
		filepath.Join(certDir, "test-2"),
	)
}

// TestAcademyCreateDryRunWeightInference covers weight inference against the
// planned filesystem view. A challenge run plans root -> Lab -> Exam in one
// dry run: when the Exam is planned, the Lab's _index.md exists ONLY in the
// plan, so the Exam's weight (2) can only come from reading the planned Lab
// weight (1) through the view — on disk nothing exists yet.
func TestAcademyCreateDryRunWeightInference(t *testing.T) {
	setupDryRunTest(t)

	const org = "15151515-1515-1515-1515-151515151515"
	dryRunArgs := []string{
		"create", "--type", "challenge", "--title", "Weighted Challenge",
		"--description", "Desc", "--org", org,
	}
	out, err := runCreateArgs(t, withDryRun(dryRunArgs, true))
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.ToSlash(filepath.Join(wd, "content", "challenges", org, "weighted-challenge"))
	root := extractRenderedFile(t, out, base+"/_index.md")
	lab := extractRenderedFile(t, out, base+"/lab/_index.md")
	exam := extractRenderedFile(t, out, base+"/exam/_index.md")

	// Empty target -> the first node takes weight 1.
	requireWeight(t, root, 1)
	// The Lab sees only the planned root _index.md (a file, not a counted
	// sibling directory), so it also takes weight 1.
	requireWeight(t, lab, 1)
	// The Exam's weight (2) is max(planned Lab weight)+1. The Lab exists only
	// in the plan at this point — if inference read only the disk, the Exam
	// would get 1. This is the plan-only visibility proof.
	requireWeight(t, exam, 2)

	// Nothing was written — the weights came from the planned view only.
	requireNoFilesystemEntry(t, filepath.Join(wd, "content", "challenges"))

	// Parity: a real run from the same pristine state must render the same
	// weights into the same files.
	wd2 := t.TempDir()
	t.Chdir(wd2)
	_, err = runCreateArgs(t, withDryRun(dryRunArgs, false))
	if err != nil {
		t.Fatalf("real run failed: %v", err)
	}
	realBase := filepath.Join(wd2, "content", "challenges", org, "weighted-challenge")
	for name, weight := range map[string]int{
		filepath.Join(realBase, "_index.md"):         1,
		filepath.Join(realBase, "lab", "_index.md"):  1,
		filepath.Join(realBase, "exam", "_index.md"): 2,
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("real run did not create %s: %v", name, err)
		}
		requireWeight(t, string(data), weight)
	}
}
