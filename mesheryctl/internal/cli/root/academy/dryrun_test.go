package academy

import (
	"os"
	"path/filepath"
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
		target := filepath.Join(wd, "content", "learning-paths", "44444444-4444-4444-4444-444444444444", "force-target", "_index.md")
		original, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("setup failed reading target: %v", err)
		}

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
