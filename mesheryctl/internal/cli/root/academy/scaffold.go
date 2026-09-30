package academy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"github.com/meshery/meshery/mesheryctl/internal/cli/root/academy/templates"
	"github.com/meshery/meshery/mesheryctl/pkg/utils"
	academyModel "github.com/meshery/schemas/models/v1beta3/academy"
	"gopkg.in/yaml.v3"
)

// TemplateData carries the data rendered into the frontmatter template.
// The local ID field intentionally shadows ChildNode.ID, which is a typed uuid,
// because the scaffolded frontmatter exposes a replaceable placeholder (e.g."REPLACE_WITH_INSTRUCTOR_CONSOLE_ID")
// rather than a uuid.
type TemplateData struct {
	academyModel.ChildNode
	Level    academyModel.Level
	OrgID    string
	Category string
	Tags     []string
	ID       string
	Draft    bool
}

// TypeString returns the content type for the frontmatter template.
func (t TemplateData) TypeString() string {
	if t.Type != nil {
		return string(*t.Type)
	}
	return ""
}

// LevelString returns the level for the frontmatter template.
func (t TemplateData) LevelString() string {
	return string(t.Level)
}

// BannerString returns the banner filename for the frontmatter template.
func (t TemplateData) BannerString() string {
	if t.Banner != nil {
		return *t.Banner
	}
	return ""
}

// WeightInt returns the weight for the frontmatter template.
func (t TemplateData) WeightInt() int {
	if t.Weight != nil {
		return int(*t.Weight)
	}
	return 0
}

// Frontmatter representation to extract weight
type Frontmatter struct {
	Weight int `yaml:"weight"`
}

func extractWeight(content []byte) int {
	parts := bytes.SplitN(content, []byte("---"), 3)
	if len(parts) >= 3 {
		var fm Frontmatter
		err := yaml.Unmarshal(parts[1], &fm)
		if err == nil {
			return fm.Weight
		}
	}
	return 0
}

// dirEntry is the view-level projection of os.DirEntry needed by inferWeight.
type dirEntry struct {
	name  string
	isDir bool
}

// scaffoldView abstracts every filesystem effect of scaffolding so a single
// code path serves both create modes: realScaffoldView applies effects to disk
// (normal create), dryRunScaffoldView records them without touching the
// filesystem (--dry-run). Reads consult the recorded plan first, so nodes
// scaffolded later in the same run observe earlier ones exactly as they would
// on disk — e.g. checkNesting validating a child against a parent _index.md
// written moments before, or weight inference counting a sibling created
// earlier in the same tree.
type scaffoldView interface {
	// mkdirAll creates path and its parents (os.MkdirAll semantics).
	mkdirAll(path string) error
	// stat returns nil when path exists (on disk or planned), the stat error otherwise.
	stat(path string) error
	readFile(path string) ([]byte, error)
	readDir(path string) ([]dirEntry, error)
	// create truncating-writes content to path (os.Create semantics).
	create(path string, content []byte) error
}

// realScaffoldView applies scaffolding effects to the real filesystem.
type realScaffoldView struct{}

func (realScaffoldView) mkdirAll(path string) error {
	return os.MkdirAll(path, 0755)
}

func (realScaffoldView) stat(path string) error {
	_, err := os.Stat(path)
	return err
}

func (realScaffoldView) readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (realScaffoldView) readDir(path string) ([]dirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, dirEntry{name: entry.Name(), isDir: entry.IsDir()})
	}
	return out, nil
}

func (realScaffoldView) create(path string, content []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			utils.Log.Errorf("failed to close file %s: %v", path, cerr)
		}
	}()
	_, err = f.Write(content)
	return err
}

func inferWeight(v scaffoldView, parentDir, excludeDir string) int {
	entries, err := v.readDir(parentDir)
	if err != nil {
		return 1
	}

	maxWeight := 0
	for _, entry := range entries {
		if entry.isDir && entry.name != excludeDir {
			indexPath := filepath.Join(parentDir, entry.name, "_index.md")
			content, err := v.readFile(indexPath)
			if err == nil {
				weight := extractWeight(content)
				if weight > maxWeight {
					maxWeight = weight
				}
			}
		}
	}
	if maxWeight == 0 {
		return 1
	}
	return maxWeight + 1
}

func getTemplateString(cType string) string {
	if IsValidNodeType(cType) {
		return templates.NodeTemplate
	}
	return ""
}

func contentDirSegment(cType string) string {
	return cType + "s"
}

// ParentFrontmatter is the slice of a parent _index.md frontmatter needed for
// nesting validation and metadata inheritance. Categories and tags have no schema
// equivalent and stay as plain values.
type ParentFrontmatter struct {
	Type     academyModel.ContentType `yaml:"type"`
	Level    academyModel.Level       `yaml:"level"`
	Category string                   `yaml:"categories"`
	Tags     []string                 `yaml:"tags"`
}

func isRootType(cType string) bool {
	return cType == string(academyModel.LearningPath) || cType == string(academyModel.Certification) || cType == string(academyModel.Challenge)
}

func checkNesting(v scaffoldView, cType string, parentDir string) (ParentFrontmatter, error) {
	var pf ParentFrontmatter
	indexPath := filepath.Join(parentDir, "_index.md")
	content, err := v.readFile(indexPath)
	if err != nil {
		if isRootType(cType) {
			return pf, nil
		}
		return pf, errInvalidParentMetadata(indexPath, cType, "no parent _index.md found")
	}

	parts := bytes.SplitN(content, []byte("---"), 3)
	if len(parts) < 3 {
		if isRootType(cType) {
			return pf, nil
		}
		return pf, errInvalidParentMetadata(indexPath, cType, "malformed frontmatter")
	}

	if err := yaml.Unmarshal(parts[1], &pf); err != nil {
		if isRootType(cType) {
			return pf, nil
		}
		return pf, errInvalidParentMetadata(indexPath, cType, fmt.Sprintf("invalid YAML: %v", err))
	}

	if pf.Type == "" {
		if isRootType(cType) {
			return pf, nil
		}
		return pf, errInvalidParentMetadata(indexPath, cType, "has no type field")
	}

	allowed, exists := AllowedChildren[string(pf.Type)]
	if !exists {
		return pf, errInvalidNesting(string(pf.Type), cType)
	}
	for _, child := range allowed {
		if child == cType {
			return pf, nil
		}
	}
	return pf, errInvalidNesting(string(pf.Type), cType)
}

type ScaffoldOptions struct {
	Type        academyModel.ContentType
	Title       string
	Description string
	Level       academyModel.Level
	OrgID       string
	Category    string
	Tags        []string
	TargetDir   string
	Force       bool
	ID          string
	Banner      string
	Draft       bool
	DryRun      bool
	SkipNesting bool
}

// scaffold dispatches to the scaffolding routine for the requested content
// type, routing every filesystem effect through v.
func scaffold(v scaffoldView, opts ScaffoldOptions) error {
	// Root types (learning-path, certification) scaffold a full starter tree; challenge
	// has its own lab/exam/content shape. Structural nodes (course, module, page, etc.)
	// add a single node into the existing tree at --into.
	if opts.Type == academyModel.Challenge {
		return scaffoldChallenge(v, opts)
	}
	if isRootType(string(opts.Type)) {
		return scaffoldTree(v, opts)
	}
	return scaffoldNode(v, opts, "")
}

func scaffoldNode(v scaffoldView, opts ScaffoldOptions, explicitFolderName string) error {
	tmplStr := getTemplateString(string(opts.Type))
	if tmplStr == "" {
		return errTaxonomyType(string(opts.Type))
	}

	var pf ParentFrontmatter
	if !opts.SkipNesting {
		var err error
		pf, err = checkNesting(v, string(opts.Type), opts.TargetDir)
		if err != nil {
			return err
		}
	}
	parentType := pf.Type

	if opts.Level == "" && pf.Level != "" {
		opts.Level = pf.Level
	}
	if opts.Category == "" && pf.Category != "" {
		opts.Category = pf.Category
	}
	if len(opts.Tags) == 0 && len(pf.Tags) > 0 {
		opts.Tags = pf.Tags
	}

	if opts.ID == "" && isRootType(string(opts.Type)) {
		opts.ID = "REPLACE_WITH_INSTRUCTOR_CONSOLE_ID"
	}

	var indexPath string
	folderName := explicitFolderName
	if folderName == "" {
		var err error
		folderName, err = makeSlug(opts.Title)
		if err != nil {
			return err
		}
	}

	if opts.Type == Test && (parentType == Course || parentType == Module) {
		indexPath = filepath.Join(opts.TargetDir, "test.md")
	} else if opts.Type == Exam && parentType == Course {
		indexPath = filepath.Join(opts.TargetDir, "course-exam.md")
	} else {
		if opts.Type == Test && parentType == academyModel.Certification {
			const maxTests = 1000
			found := false
			for testNum := 1; testNum <= maxTests; testNum++ {
				testFolderName := fmt.Sprintf("test-%d", testNum)
				statErr := v.stat(filepath.Join(opts.TargetDir, testFolderName))
				if statErr == nil {
					continue
				}
				if !os.IsNotExist(statErr) {
					return statErr
				}
				folderName = testFolderName
				found = true
				break
			}
			if !found {
				return errScaffoldExists(filepath.Join(opts.TargetDir, "test-*"))
			}
		}

		nodeDir := filepath.Join(opts.TargetDir, folderName)
		if err := v.mkdirAll(nodeDir); err != nil {
			return err
		}
		indexPath = filepath.Join(nodeDir, "_index.md")
	}
	if _, err := os.Stat(indexPath); err == nil && !opts.Force {
		return errScaffoldExists(indexPath)
	}

	weight := inferWeight(v, opts.TargetDir, folderName)

	tmpl, err := template.New(string(opts.Type)).Funcs(template.FuncMap{
		"yamlQuote": strconv.Quote,
	}).Parse(tmplStr)
	if err != nil {
		return err
	}

	weightF := float32(weight)
	var banner *string
	if opts.Banner != "" {
		banner = &opts.Banner
	}
	data := TemplateData{
		ChildNode: academyModel.ChildNode{
			Title:       opts.Title,
			Description: opts.Description,
			Type:        &opts.Type,
			Weight:      &weightF,
			Banner:      banner,
		},
		Level:    opts.Level,
		OrgID:    opts.OrgID,
		Category: opts.Category,
		Tags:     opts.Tags,
		ID:       opts.ID,
		Draft:    opts.Draft,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err
	}

	if err := v.create(indexPath, buf.Bytes()); err != nil {
		return err
	}

	if !opts.DryRun {
		utils.Log.Infof("Scaffolded %s '%s' at %s", opts.Type, opts.Title, indexPath)
	}
	return nil
}

func scaffoldChild(v scaffoldView, opts ScaffoldOptions, cType academyModel.ContentType, title, into string) (string, error) {
	child := opts
	child.Type = cType
	child.Title = title
	child.Description = ""
	child.Level = ""
	child.Category = ""
	child.Tags = nil
	child.ID = ""
	child.Banner = ""
	child.TargetDir = into

	if err := scaffoldNode(v, child, ""); err != nil {
		return "", err
	}

	slug, err := makeSlug(title)
	if err != nil {
		return "", err
	}

	return filepath.Join(into, slug), nil
}

func scaffoldTree(v scaffoldView, opts ScaffoldOptions) error {
	folderName, err := makeSlug(opts.Title)
	if err != nil {
		return err
	}
	baseDir := filepath.Join(opts.TargetDir, folderName)

	err = scaffoldNode(v, opts, folderName)
	if err != nil {
		return err
	}

	currentDir := baseDir

	// Only root types reach scaffoldTree: learning-path builds a course/module/page
	// starter tree; certification builds an exam.
	if opts.Type == academyModel.LearningPath {
		currentDir, err = scaffoldChild(v, opts, Course, "Course 1", currentDir)
		if err != nil {
			return err
		}
		currentDir, err = scaffoldChild(v, opts, Module, "Module 1", currentDir)
		if err != nil {
			return err
		}
		_, err = scaffoldChild(v, opts, Page, "Page 1", currentDir)
		if err != nil {
			return err
		}
	}

	if opts.Type == academyModel.Certification {
		_, err = scaffoldChild(v, opts, Exam, "Exam 1", currentDir)
		if err != nil {
			return err
		}
	}

	return nil
}

func scaffoldChallenge(v scaffoldView, opts ScaffoldOptions) error {
	folderName, err := makeSlug(opts.Title)
	if err != nil {
		return err
	}
	baseDir := filepath.Join(opts.TargetDir, folderName)

	err = scaffoldNode(v, opts, folderName)
	if err != nil {
		return err
	}

	currentDir := baseDir

	_, err = scaffoldChild(v, opts, Lab, "Lab", currentDir)
	if err != nil {
		return err
	}

	_, err = scaffoldChild(v, opts, Exam, "Exam", currentDir)
	if err != nil {
		return err
	}

	contentDirs := []struct {
		name string
	}{
		{"description"},
		{"getting-started"},
		{"faq"},
	}

	for _, dir := range contentDirs {
		pageOpts := opts
		pageOpts.Type = Page
		pageOpts.Title = dir.name
		pageOpts.Description = ""
		pageOpts.Category = ""
		pageOpts.Tags = nil
		pageOpts.Banner = ""
		pageOpts.TargetDir = filepath.Join(currentDir, "content")
		pageOpts.ID = ""
		pageOpts.SkipNesting = true
		err = scaffoldNode(v, pageOpts, dir.name)
		if err != nil {
			return err
		}
	}

	return nil
}
