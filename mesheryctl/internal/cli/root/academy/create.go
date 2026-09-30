package academy

import (
	"os"
	"path/filepath"
	"strings"

	mesheryctlflags "github.com/meshery/meshery/mesheryctl/internal/cli/pkg/flags"
	"github.com/meshery/meshery/mesheryctl/pkg/utils"
	academyModel "github.com/meshery/schemas/models/v1beta3/academy"
	"github.com/spf13/cobra"
)

type cmdAcademyCreateFlags struct {
	Type        string `json:"type" validate:"required"`
	Title       string `json:"title" validate:"required"`
	Description string `json:"description" validate:"required"`
	Into        string `json:"into"`
	OrgID       string `json:"org" validate:"omitempty,uuid"`
	Level       string `json:"level"`
	Category    string `json:"category"`
	Tags        string `json:"tags"`
	Force       bool   `json:"force"`
	DryRun      bool   `json:"dryRun"`
	ID          string `json:"id"`
	Banner      string `json:"banner"`
	Draft       bool   `json:"draft"`
}

var createAcademyFlags cmdAcademyCreateFlags

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold Layer5 Academy content",
	Long: `Create scaffolding for Layer5 Academy content types such as learning paths, courses, modules, and pages.
For 'learning-path', it creates a full starter tree.
For others, it adds a single node into an existing tree at the path specified by '--into'.
Use '--dry-run' to preview everything the command would do without touching the filesystem.`,
	Example: `
// Scaffold a full learning path tree (root type via --type flag)
mesheryctl exp academy create --type learning-path --title "My Path" --description "Desc" --level beginner --org 123e4567-e89b-12d3-a456-426614174000

// Scaffold a single course into an existing tree (structural node via subcommand)
mesheryctl exp academy create course "New Course" --description "Desc" --into ./my-path

// Scaffold a challenge
mesheryctl exp academy create --type challenge --title "My Challenge" --description "Desc" --org 123e4567-e89b-12d3-a456-426614174000

// Preview a scaffold without writing anything
mesheryctl exp academy create learning-path --title "Kubernetes Basics" --org 123e4567-e89b-12d3-a456-426614174000 --dry-run
`,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		// Accept the positional form `create <root-type>` as sugar for
		// `create --type <root-type>` (e.g. `academy create learning-path
		// --title ... --dry-run`). Root types are not registered subcommands,
		// so their name arrives here as a positional arg. Explicit --type
		// always wins, and structural types keep their subcommand form.
		if createAcademyFlags.Type == "" && len(args) > 0 && isRootType(args[0]) {
			createAcademyFlags.Type = args[0]
		}
		return mesheryctlflags.ValidateCmdFlags(cmd, &createAcademyFlags)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		cType := academyModel.ContentType(createAcademyFlags.Type)
		if !isRootType(string(cType)) {
			return errTaxonomyType(createAcademyFlags.Type)
		}
		return executeCreate()
	},
}

func executeCreate() error {
	cType := academyModel.ContentType(createAcademyFlags.Type)
	if !IsValidNodeType(string(cType)) {
		return errTaxonomyType(createAcademyFlags.Type)
	}

	targetDir := createAcademyFlags.Into

	if targetDir == "" {
		if !isRootType(string(cType)) {
			return errMissingInto()
		}
		var err error
		targetDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	orgID := createAcademyFlags.OrgID

	if isRootType(string(cType)) {
		if orgID == "" {
			return errMissingOrgID()
		}
		if err := validatePathSegment(orgID); err != nil {
			return err
		}
		targetDir = filepath.Join(targetDir, "content", contentDirSegment(string(cType)), orgID)
	}

	var tagsList []string
	if createAcademyFlags.Tags != "" {
		for _, t := range strings.Split(createAcademyFlags.Tags, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				tagsList = append(tagsList, trimmed)
			}
		}
	}

	if createAcademyFlags.Level == "" {
		if isRootType(string(cType)) {
			createAcademyFlags.Level = string(academyModel.Beginner)
		}
	} else {
		if err := validateLevel(createAcademyFlags.Level); err != nil {
			return err
		}
	}

	opts := ScaffoldOptions{
		Type:        cType,
		Title:       createAcademyFlags.Title,
		Description: createAcademyFlags.Description,
		Level:       academyModel.Level(createAcademyFlags.Level),
		OrgID:       orgID,
		Category:    createAcademyFlags.Category,
		Tags:        tagsList,
		TargetDir:   targetDir,
		Force:       createAcademyFlags.Force,
		DryRun:      createAcademyFlags.DryRun,
		ID:          createAcademyFlags.ID,
		Banner:      createAcademyFlags.Banner,
		Draft:       createAcademyFlags.Draft,
	}

	if createAcademyFlags.DryRun {
		return executeDryRun(opts)
	}

	// Root types (learning-path, certification) scaffold a full starter tree; challenge
	// has its own lab/exam/content shape. Structural nodes (course, module, page, etc.)
	// add a single node into the existing tree at --into.
	if cType == academyModel.Challenge {
		return scaffold(realScaffoldView{}, opts)
	}
	if isRootType(string(cType)) {
		return scaffold(realScaffoldView{}, opts)
	}

	return scaffold(realScaffoldView{}, opts)
}

// executeDryRun runs the identical scaffolding path against a recording view,
// then prints the plan. Validation errors (invalid nesting, missing parent
// metadata, ...) surface exactly as they would in a real run: when nothing
// was planned yet the error is returned bare; when the run stopped partway
// (e.g. a node colliding without --force) the partial plan is still reported
// so the user sees what a real run would have written before failing, and the
// same error is returned.
func executeDryRun(opts ScaffoldOptions) error {
	dry := newDryRunScaffoldView()
	err := scaffold(dry, opts)
	plan := dry.plan
	if err != nil && plan.isEmpty() {
		// The run failed before planning any effect — report the validation
		// error itself, exactly as a real run would.
		return err
	}

	planErr := printScaffoldPlan(plan, opts.Force)
	if planErr != nil {
		err = planErr
	}
	printScaffoldPlanVerbose(plan)

	if err != nil {
		utils.Log.Warnf("Dry run — no files were created. Fix the reported error and re-run.")
		return err
	}
	return nil
}

func makeSubCmd(kind string) *cobra.Command {
	subCmd := &cobra.Command{
		Use:   kind + " <title>",
		Short: "Scaffold a " + kind,
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			createAcademyFlags.Type = kind
			createAcademyFlags.Title = args[0]
			return mesheryctlflags.ValidateCmdFlags(cmd, &createAcademyFlags)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeCreate()
		},
	}
	return subCmd
}

func init() {
	createCmd.Flags().StringVarP(&createAcademyFlags.Type, "type", "t", "", "Content type (learning-path, certification, challenge)")
	createCmd.Flags().StringVar(&createAcademyFlags.Title, "title", "", "Title of the content")
	createCmd.Flags().StringVar(&createAcademyFlags.Description, "description", "", "Description of the content")
	createCmd.Flags().StringVar(&createAcademyFlags.Into, "into", "", "Target directory path")
	createCmd.Flags().StringVar(&createAcademyFlags.OrgID, "org", "", "Organization ID")
	createCmd.Flags().StringVar(&createAcademyFlags.Level, "level", "", "Content level (e.g., beginner, intermediate, advanced)")
	createCmd.Flags().StringVar(&createAcademyFlags.Category, "category", "", "Category of the content")
	createCmd.Flags().StringVar(&createAcademyFlags.Tags, "tags", "", "Comma-separated list of tags")
	createCmd.Flags().StringVar(&createAcademyFlags.ID, "id", "", "Content ID for Instructor Console")
	createCmd.Flags().StringVar(&createAcademyFlags.Banner, "banner", "", "Banner image filename placed in the same directory")
	createCmd.Flags().BoolVar(&createAcademyFlags.Draft, "draft", false, "Mark the content as draft (not published)")
	createCmd.Flags().BoolVar(&createAcademyFlags.DryRun, "dry-run", false, "Preview what would be scaffolded without creating, modifying, or deleting any files")
	createCmd.Flags().BoolVarP(&createAcademyFlags.Force, "force", "f", false, "Overwrite existing files")

	subcommands := []string{string(Course), string(Module), string(Page), string(Lab), string(Test), string(Exam)}
	for _, kind := range subcommands {
		subCmd := makeSubCmd(kind)
		subCmd.Flags().StringVar(&createAcademyFlags.Description, "description", "", "Description of the content")
		subCmd.Flags().StringVar(&createAcademyFlags.Into, "into", "", "Target directory path")
		subCmd.Flags().StringVar(&createAcademyFlags.Level, "level", "", "Content level (e.g., beginner, intermediate, advanced)")
		subCmd.Flags().StringVar(&createAcademyFlags.Category, "category", "", "Category of the content")
		subCmd.Flags().StringVar(&createAcademyFlags.Tags, "tags", "", "Comma-separated list of tags")
		subCmd.Flags().StringVar(&createAcademyFlags.Banner, "banner", "", "Banner image filename placed in the same directory")
		subCmd.Flags().BoolVar(&createAcademyFlags.Draft, "draft", false, "Mark the content as draft (not published)")
		subCmd.Flags().BoolVar(&createAcademyFlags.DryRun, "dry-run", false, "Preview what would be scaffolded without creating, modifying, or deleting any files")
		subCmd.Flags().BoolVarP(&createAcademyFlags.Force, "force", "f", false, "Overwrite existing files")
		createCmd.AddCommand(subCmd)
	}
}
