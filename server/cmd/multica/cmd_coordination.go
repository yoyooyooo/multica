package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/multica-ai/multica/server/internal/cli"
)

var (
	coordinationOutput       = "json"
	coordinationProfileKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

var coordinationCmd = &cobra.Command{
	Use:   "coordination",
	Short: "Manage passive work coordination facts",
	Args:  coordinationNoArgs,
}

var coordinationScopeCmd = &cobra.Command{
	Use:   "scope",
	Short: "Create and read root coordination scopes",
	Args:  coordinationNoArgs,
}

var coordinationScopeEnsureCmd = &cobra.Command{
	Use:   "ensure",
	Short: "Ensure a root coordination scope and persist its receipt",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationScopeEnsure,
}

var coordinationScopeGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Read a coordination scope by id or root",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationScopeGet,
}

func init() {
	coordinationCmd.PersistentFlags().StringVar(&coordinationOutput, "output", "json", "Output format: json or table")
	coordinationCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return coordinationValidationError(err.Error())
	})
	coordinationCmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if coordinationOutput != "json" && coordinationOutput != "table" {
			return coordinationValidationError("--output must be json or table")
		}
		return nil
	}

	coordinationScopeEnsureCmd.Flags().String("root", "", "Root issue UUID or issue key")
	coordinationScopeEnsureCmd.Flags().String("workflow-profile", "", "Workflow profile key")
	coordinationScopeEnsureCmd.Flags().String("idempotency-key", "", "Idempotency key")
	coordinationScopeGetCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationScopeGetCmd.Flags().String("root", "", "Root issue UUID or issue key")
	coordinationScopeGetCmd.Flags().String("workflow-profile", "", "Workflow profile key")

	coordinationScopeCmd.AddCommand(coordinationScopeEnsureCmd, coordinationScopeGetCmd)
	coordinationCmd.AddCommand(coordinationScopeCmd)
}

func coordinationNoArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return coordinationValidationError("unexpected positional arguments")
	}
	return nil
}

func coordinationValidationError(message string) error {
	return &cli.ProductError{StatusCode: http.StatusBadRequest, Code: "coordination_invalid_payload", Message: message}
}

// prepareCoordinationArgs is a token/arity-aware pre-parser. Its flag metadata
// is derived from the actual coordination Cobra tree so adding a value-taking
// flag cannot silently desynchronize output detection.
func prepareCoordinationArgs(args []string) error {
	commandIndex := coordinationCommandIndex(args)
	if commandIndex < 0 {
		return nil
	}
	arity := coordinationFlagArity()
	seenOutput := false
	for i := commandIndex + 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		name := arg
		value := ""
		hasEquals := false
		if strings.HasPrefix(arg, "--") {
			if at := strings.IndexByte(arg, '='); at >= 0 {
				name, value, hasEquals = arg[:at], arg[at+1:], true
			}
		}
		takesValue, known := arity[name]
		if !known {
			return coordinationValidationError("unknown or ambiguous coordination flag")
		}
		if name == "--output" {
			if !hasEquals {
				if i+1 >= len(args) || args[i+1] == "--" {
					return coordinationValidationError("--output requires json or table")
				}
				i++
				value = args[i]
			}
			if seenOutput {
				return coordinationValidationError("--output may be specified only once")
			}
			seenOutput = true
			if value != "json" && value != "table" {
				return coordinationValidationError("--output must be json or table")
			}
			continue
		}
		if takesValue && !hasEquals {
			if i+1 >= len(args) || args[i+1] == "--" {
				return coordinationValidationError("coordination flag requires a value")
			}
			// Consume exactly one token even when it happens to spell --output.
			// Cobra owns validation of the consumed value itself.
			i++
		}
	}
	return nil
}

// coordinationFlagArity returns true for value-taking flags and false for
// boolean/no-value flags. Root persistent flags are included because Cobra
// accepts them after the coordination command as well as before it.
func coordinationFlagArity() map[string]bool {
	result := make(map[string]bool)
	collect := func(flags *pflag.FlagSet) {
		flags.VisitAll(func(flag *pflag.Flag) {
			result["--"+flag.Name] = flag.NoOptDefVal == ""
			if flag.Shorthand != "" {
				result["-"+flag.Shorthand] = flag.NoOptDefVal == ""
			}
		})
	}
	collect(rootCmd.PersistentFlags())
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		// Cobra installs the standard help flag lazily. Initialize it before
		// collecting metadata so the pre-parser accepts --help/-h without
		// weakening fail-closed handling for genuinely unknown flags.
		command.InitDefaultHelpFlag()
		collect(command.LocalNonPersistentFlags())
		collect(command.PersistentFlags())
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(coordinationCmd)
	return result
}

func coordinationCommandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return -1
		case arg == "--server-url" || arg == "--workspace-id" || arg == "--profile":
			i++
		case strings.HasPrefix(arg, "--server-url=") || strings.HasPrefix(arg, "--workspace-id=") || strings.HasPrefix(arg, "--profile="):
			continue
		case arg == "--help" || arg == "-h" || arg == "--version":
			return -1
		case arg == "--debug" || strings.HasPrefix(arg, "--debug="):
			continue
		case strings.HasPrefix(arg, "-"):
			return -1
		case arg == "coordination":
			return i
		default:
			return -1
		}
	}
	return -1
}

type coordinationScopeCLI struct {
	ID                 string `json:"id"`
	WorkspaceID        string `json:"workspace_id"`
	ScopeKind          string `json:"scope_kind"`
	State              string `json:"state"`
	RootIssueID        string `json:"root_issue_id"`
	WorkflowProfileKey string `json:"workflow_profile_key"`
	Revision           int64  `json:"revision"`
	CreatedBy          struct {
		ActorType string  `json:"actor_type"`
		ActorID   string  `json:"actor_id"`
		TaskID    *string `json:"task_id"`
	} `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type coordinationReceiptCLI struct {
	ID             string `json:"id"`
	ReceiptOrdinal int64  `json:"receipt_ordinal"`
	Operation      string `json:"operation"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id"`
	RevisionBefore int64  `json:"revision_before"`
	RevisionAfter  int64  `json:"revision_after"`
	CreatedAt      string `json:"created_at"`
}

type coordinationEnsureCLIResponse struct {
	Scope   coordinationScopeCLI   `json:"scope"`
	Receipt coordinationReceiptCLI `json:"receipt"`
}

type coordinationScopeCLIResponse struct {
	Scope coordinationScopeCLI `json:"scope"`
}

func coordinationClient(cmd *cobra.Command) (*cli.APIClient, error) {
	if _, err := requireWorkspaceID(cmd); err != nil {
		return nil, coordinationValidationError(err.Error())
	}
	return newAPIClient(cmd)
}

func runCoordinationScopeEnsure(cmd *cobra.Command, _ []string) error {
	root, _ := cmd.Flags().GetString("root")
	profile, _ := cmd.Flags().GetString("workflow-profile")
	key, _ := cmd.Flags().GetString("idempotency-key")
	if strings.TrimSpace(root) == "" || strings.TrimSpace(profile) == "" || strings.TrimSpace(key) == "" {
		return coordinationValidationError("--root, --workflow-profile, and --idempotency-key are required")
	}
	if root != strings.TrimSpace(root) || !coordinationProfileKeyRE.MatchString(profile) || key != strings.TrimSpace(key) || len(key) > 200 {
		return coordinationValidationError("invalid coordination scope input")
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	resolvedRoot, err := resolveIssueRef(ctx, client, root)
	if err != nil {
		return cli.CoordinationProductError(err)
	}
	body := map[string]string{"root_issue_id": resolvedRoot.ID, "workflow_profile_key": profile}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", key)
	var response coordinationEnsureCLIResponse
	if err := client.PostJSONWithHeaders(ctx, "/api/coordination/scopes", body, headers, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationEnsure(cmd, response)
}

func runCoordinationScopeGet(cmd *cobra.Command, _ []string) error {
	scopeID, _ := cmd.Flags().GetString("scope")
	root, _ := cmd.Flags().GetString("root")
	profile, _ := cmd.Flags().GetString("workflow-profile")
	byScope := strings.TrimSpace(scopeID) != ""
	byRoot := strings.TrimSpace(root) != "" || strings.TrimSpace(profile) != ""
	if byScope == byRoot || (byRoot && (strings.TrimSpace(root) == "" || strings.TrimSpace(profile) == "")) {
		return coordinationValidationError("use exactly one of --scope or --root with --workflow-profile")
	}
	if byScope {
		if scopeID != strings.TrimSpace(scopeID) {
			return coordinationValidationError("--scope must be a UUID")
		}
		if _, err := uuid.Parse(scopeID); err != nil {
			return coordinationValidationError("--scope must be a UUID")
		}
	} else if root != strings.TrimSpace(root) || !coordinationProfileKeyRE.MatchString(profile) {
		return coordinationValidationError("invalid root scope lookup")
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID)
	if byRoot {
		resolvedRoot, resolveErr := resolveIssueRef(ctx, client, root)
		if resolveErr != nil {
			return cli.CoordinationProductError(resolveErr)
		}
		query := url.Values{"root_issue_id": []string{resolvedRoot.ID}, "workflow_profile_key": []string{profile}}
		path = "/api/coordination/scopes/by-root?" + query.Encode()
	}
	var response coordinationScopeCLIResponse
	if err := client.GetJSON(ctx, path, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationScope(cmd, response.Scope)
}

func renderCoordinationEnsure(cmd *cobra.Command, response coordinationEnsureCLIResponse) error {
	if coordinationOutput == "table" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "scope=%s root=%s profile=%s revision=%d receipt=%s ordinal=%d\n",
			response.Scope.ID, response.Scope.RootIssueID, response.Scope.WorkflowProfileKey, response.Scope.Revision,
			response.Receipt.ID, response.Receipt.ReceiptOrdinal)
		return err
	}
	return writeCoordinationJSON(cmd, response)
}

func renderCoordinationScope(cmd *cobra.Command, scope coordinationScopeCLI) error {
	if coordinationOutput == "table" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "scope=%s root=%s profile=%s revision=%d state=%s\n",
			scope.ID, scope.RootIssueID, scope.WorkflowProfileKey, scope.Revision, scope.State)
		return err
	}
	return writeCoordinationJSON(cmd, coordinationScopeCLIResponse{Scope: scope})
}

func writeCoordinationJSON(cmd *cobra.Command, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	return err
}
