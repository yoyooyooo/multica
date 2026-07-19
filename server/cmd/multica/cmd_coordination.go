package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/multica-ai/multica/server/internal/cli"
)

var (
	coordinationOutput           = "json"
	coordinationProfileKeyRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	coordinationNonnegativeIntRE = regexp.MustCompile(`^[0-9]+$`)
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

var coordinationDependencyCmd = &cobra.Command{
	Use:   "dependency",
	Short: "Manage canonical blocked-by dependencies",
	Args:  coordinationNoArgs,
}

var coordinationDependencyAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a canonical dependency",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationDependencyAdd,
}

var coordinationDependencyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active dependencies owned by a scope",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationDependencyList,
}

var coordinationDependencyResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve a canonical dependency",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationDependencyResolve,
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

	coordinationDependencyAddCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationDependencyAddCmd.Flags().String("downstream", "", "Downstream issue UUID or issue key")
	coordinationDependencyAddCmd.Flags().String("upstream", "", "Upstream issue UUID or issue key")
	coordinationDependencyAddCmd.Flags().String("expected-revision", "", "Expected non-negative scope revision")
	coordinationDependencyAddCmd.Flags().String("idempotency-key", "", "Idempotency key")
	coordinationDependencyListCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationDependencyListCmd.Flags().String("cursor", "", "Opaque dependency page cursor")
	coordinationDependencyListCmd.Flags().Int("limit", 100, "Page size (1-100)")
	coordinationDependencyResolveCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationDependencyResolveCmd.Flags().String("dependency", "", "Coordination dependency UUID")
	coordinationDependencyResolveCmd.Flags().String("expected-revision", "", "Expected non-negative scope revision")
	coordinationDependencyResolveCmd.Flags().String("idempotency-key", "", "Idempotency key")

	coordinationScopeCmd.AddCommand(coordinationScopeEnsureCmd, coordinationScopeGetCmd)
	coordinationDependencyCmd.AddCommand(coordinationDependencyAddCmd, coordinationDependencyListCmd, coordinationDependencyResolveCmd)
	coordinationCmd.AddCommand(coordinationScopeCmd, coordinationDependencyCmd)
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

// prepareCoordinationArgs is a token/arity-aware pre-parser. It uses the full
// Cobra tree only to locate the selected command, then validates flags against
// that command's root/ancestor/current metadata so sibling flags fail before
// Cobra can switch the error renderer to table mode.
func prepareCoordinationArgs(args []string) error {
	commandIndex := coordinationCommandIndex(args)
	if commandIndex < 0 {
		return nil
	}
	allArity := coordinationFlagArity()
	selected := coordinationSelectedCommand(args, commandIndex, allArity)
	arity := coordinationAllowedFlagArity(selected)
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

// coordinationFlagArity returns the full-tree metadata used only while locating
// the selected command. Final validation uses coordinationAllowedFlagArity.
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

func coordinationSelectedCommand(args []string, commandIndex int, allArity map[string]bool) *cobra.Command {
	selected := coordinationCmd
	for i := commandIndex + 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name := arg
			hasEquals := false
			if strings.HasPrefix(arg, "--") {
				if at := strings.IndexByte(arg, '='); at >= 0 {
					name, hasEquals = arg[:at], true
				}
			}
			if takesValue, known := allArity[name]; known && takesValue && !hasEquals && i+1 < len(args) && args[i+1] != "--" {
				i++
			}
			continue
		}
		for _, child := range selected.Commands() {
			if child.Name() == arg || child.HasAlias(arg) {
				selected = child
				break
			}
		}
	}
	return selected
}

func coordinationAllowedFlagArity(selected *cobra.Command) map[string]bool {
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
	path := make([]*cobra.Command, 0, 4)
	for command := selected; command != nil && command != rootCmd; command = command.Parent() {
		path = append(path, command)
		if command == coordinationCmd {
			break
		}
	}
	for i := len(path) - 1; i >= 0; i-- {
		path[i].InitDefaultHelpFlag()
		collect(path[i].PersistentFlags())
	}
	selected.InitDefaultHelpFlag()
	collect(selected.LocalNonPersistentFlags())
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

type coordinationDependencyCLI struct {
	ID                  string `json:"id"`
	WorkspaceID         string `json:"workspace_id"`
	CoordinationScopeID string `json:"coordination_scope_id"`
	DownstreamIssueID   string `json:"downstream_issue_id"`
	UpstreamIssueID     string `json:"upstream_issue_id"`
	BlocksIssueID       string `json:"blocks_issue_id"`
	CreatedBy           struct {
		ActorType string  `json:"actor_type"`
		ActorID   string  `json:"actor_id"`
		TaskID    *string `json:"task_id"`
	} `json:"created_by"`
	CreatedAt  string `json:"created_at"`
	ResolvedBy *struct {
		ActorType string  `json:"actor_type"`
		ActorID   string  `json:"actor_id"`
		TaskID    *string `json:"task_id"`
	} `json:"resolved_by"`
	ResolvedAt *string `json:"resolved_at"`
}

type coordinationDependencyMutationCLIResponse struct {
	Dependency    coordinationDependencyCLI `json:"dependency"`
	ScopeRevision int64                     `json:"scope_revision"`
	Receipt       coordinationReceiptCLI    `json:"receipt"`
	Outcome       string                    `json:"outcome"`
}

type coordinationDependencyPageCLIResponse struct {
	Dependencies  []coordinationDependencyCLI `json:"dependencies"`
	ScopeRevision int64                       `json:"scope_revision"`
	NextCursor    *string                     `json:"next_cursor"`
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

func runCoordinationDependencyAdd(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	downstream, _ := cmd.Flags().GetString("downstream")
	upstream, _ := cmd.Flags().GetString("upstream")
	key, _ := cmd.Flags().GetString("idempotency-key")
	if downstream == "" || upstream == "" || downstream != strings.TrimSpace(downstream) || upstream != strings.TrimSpace(upstream) || key == "" || key != strings.TrimSpace(key) || len(key) > 200 {
		return coordinationValidationError("--downstream, --upstream, and --idempotency-key are required and must be valid")
	}
	expected, err := coordinationExpectedRevision(cmd)
	if err != nil {
		return err
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	downstreamRef, err := resolveIssueRef(ctx, client, downstream)
	if err != nil {
		return cli.CoordinationProductError(err)
	}
	upstreamRef, err := resolveIssueRef(ctx, client, upstream)
	if err != nil {
		return cli.CoordinationProductError(err)
	}
	body := struct {
		ExpectedRevision  int64  `json:"expected_revision"`
		DownstreamIssueID string `json:"downstream_issue_id"`
		UpstreamIssueID   string `json:"upstream_issue_id"`
	}{ExpectedRevision: expected, DownstreamIssueID: downstreamRef.ID, UpstreamIssueID: upstreamRef.ID}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", key)
	var response coordinationDependencyMutationCLIResponse
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/dependencies"
	if err := client.PostJSONWithHeaders(ctx, path, body, headers, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationDependencyMutation(cmd, response)
}

func runCoordinationDependencyList(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	cursor, _ := cmd.Flags().GetString("cursor")
	limit, _ := cmd.Flags().GetInt("limit")
	if cursor != strings.TrimSpace(cursor) || limit < 1 || limit > 100 {
		return coordinationValidationError("--cursor and --limit must form a valid dependency page request")
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/dependencies?" + query.Encode()
	var response coordinationDependencyPageCLIResponse
	if err := client.GetJSON(ctx, path, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationDependencyPage(cmd, response)
}

func runCoordinationDependencyResolve(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	dependencyID, err := coordinationUUIDFlag(cmd, "dependency")
	if err != nil {
		return err
	}
	key, _ := cmd.Flags().GetString("idempotency-key")
	if key == "" || key != strings.TrimSpace(key) || len(key) > 200 {
		return coordinationValidationError("--idempotency-key is required and must be valid")
	}
	expected, err := coordinationExpectedRevision(cmd)
	if err != nil {
		return err
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	body := struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}{ExpectedRevision: expected}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", key)
	var response coordinationDependencyMutationCLIResponse
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/dependencies/" + url.PathEscape(dependencyID) + "/resolve"
	if err := client.PostJSONWithHeaders(ctx, path, body, headers, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationDependencyMutation(cmd, response)
}

func coordinationUUIDFlag(cmd *cobra.Command, name string) (string, error) {
	value, _ := cmd.Flags().GetString(name)
	if value == "" || value != strings.TrimSpace(value) {
		return "", coordinationValidationError("--" + name + " must be a UUID")
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", coordinationValidationError("--" + name + " must be a UUID")
	}
	return strings.ToLower(parsed.String()), nil
}

func coordinationExpectedRevision(cmd *cobra.Command) (int64, error) {
	flag := cmd.Flags().Lookup("expected-revision")
	raw, _ := cmd.Flags().GetString("expected-revision")
	if flag == nil || !flag.Changed || !coordinationNonnegativeIntRE.MatchString(raw) {
		return 0, coordinationValidationError("--expected-revision must be explicitly set to a non-negative int64")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, coordinationValidationError("--expected-revision must be explicitly set to a non-negative int64")
	}
	return value, nil
}

func renderCoordinationDependencyMutation(cmd *cobra.Command, response coordinationDependencyMutationCLIResponse) error {
	if coordinationOutput == "table" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "dependency=%s downstream=%s blocked_by=%s outcome=%s revision=%d receipt=%s ordinal=%d\n",
			response.Dependency.ID, response.Dependency.DownstreamIssueID, response.Dependency.UpstreamIssueID,
			response.Outcome, response.ScopeRevision, response.Receipt.ID, response.Receipt.ReceiptOrdinal)
		return err
	}
	return writeCoordinationJSON(cmd, response)
}

func renderCoordinationDependencyPage(cmd *cobra.Command, response coordinationDependencyPageCLIResponse) error {
	if coordinationOutput == "table" {
		if len(response.Dependencies) == 0 {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "No active coordination dependencies (revision %d).\n", response.ScopeRevision)
			return err
		}
		for _, dependency := range response.Dependencies {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s blocked_by %s\n", dependency.ID, dependency.DownstreamIssueID, dependency.UpstreamIssueID); err != nil {
				return err
			}
		}
		if response.NextCursor != nil {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "next_cursor=%s revision=%d\n", *response.NextCursor, response.ScopeRevision)
			return err
		}
		return nil
	}
	return writeCoordinationJSON(cmd, response)
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
