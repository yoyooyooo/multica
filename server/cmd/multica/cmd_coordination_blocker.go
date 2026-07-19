package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var coordinationBlockerCmd = &cobra.Command{
	Use:   "blocker",
	Short: "Manage strict typed blocker records",
	Args:  coordinationNoArgs,
}

var coordinationBlockerAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Append a strict typed blocker record",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationBlockerAdd,
}

var coordinationBlockerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List blocker records owned by a scope",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationBlockerList,
}

var coordinationBlockerResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve a strict typed blocker record",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationBlockerResolve,
}

type coordinationBlockerEvidenceCLI struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type coordinationBlockerPayloadFile struct {
	ReasonCode   *string                           `json:"reason_code"`
	EvidenceRefs *[]coordinationBlockerEvidenceCLI `json:"evidence_refs"`
}

type coordinationBlockerResolutionFile struct {
	ResolutionCode *string                           `json:"resolution_code"`
	EvidenceRefs   *[]coordinationBlockerEvidenceCLI `json:"evidence_refs"`
}

type coordinationBlockerActorCLI struct {
	Type   string  `json:"type"`
	ID     string  `json:"id"`
	TaskID *string `json:"task_id"`
}

type coordinationBlockerCLI struct {
	ID                     string                           `json:"id"`
	WorkspaceID            string                           `json:"workspace_id"`
	ScopeID                string                           `json:"scope_id"`
	Kind                   string                           `json:"kind"`
	SchemaVersion          int32                            `json:"schema_version"`
	Status                 string                           `json:"status"`
	RootIssueID            string                           `json:"root_issue_id"`
	DownstreamIssueID      string                           `json:"downstream_issue_id"`
	UpstreamIssueID        string                           `json:"upstream_issue_id"`
	DependencyID           *string                          `json:"dependency_id"`
	ReasonCode             string                           `json:"reason_code"`
	ResolutionCode         *string                          `json:"resolution_code"`
	CreateEvidenceRefs     []coordinationBlockerEvidenceCLI `json:"create_evidence_refs"`
	ResolutionEvidenceRefs []coordinationBlockerEvidenceCLI `json:"resolution_evidence_refs"`
	CreatedBy              coordinationBlockerActorCLI      `json:"created_by"`
	ResolvedBy             *coordinationBlockerActorCLI     `json:"resolved_by"`
	CreatedAt              string                           `json:"created_at"`
	ResolvedAt             *string                          `json:"resolved_at"`
}

type coordinationBlockerMutationCLIResponse struct {
	Receipt       coordinationReceiptCLI `json:"receipt"`
	Resource      coordinationBlockerCLI `json:"resource"`
	ScopeRevision int64                  `json:"scope_revision"`
	Changed       bool                   `json:"changed"`
	Replayed      bool                   `json:"replayed"`
}

type coordinationBlockerPageCLIResponse struct {
	ScopeID       string                   `json:"scope_id"`
	ScopeRevision int64                    `json:"scope_revision"`
	StatusFilter  string                   `json:"status_filter"`
	Items         []coordinationBlockerCLI `json:"items"`
	NextCursor    *string                  `json:"next_cursor"`
}

func init() {
	coordinationBlockerAddCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationBlockerAddCmd.Flags().String("downstream", "", "Downstream issue UUID or issue key")
	coordinationBlockerAddCmd.Flags().String("upstream", "", "Upstream issue UUID or issue key")
	coordinationBlockerAddCmd.Flags().String("dependency", "", "Optional canonical dependency UUID")
	coordinationBlockerAddCmd.Flags().String("payload-file", "", "Strict blocker payload JSON file (max 4096 bytes)")
	coordinationBlockerAddCmd.Flags().String("expected-revision", "", "Expected non-negative scope revision")
	coordinationBlockerAddCmd.Flags().String("idempotency-key", "", "Idempotency key")

	coordinationBlockerListCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationBlockerListCmd.Flags().String("status", "open", "Status filter: open, resolved, or all")
	coordinationBlockerListCmd.Flags().String("cursor", "", "Opaque blocker page cursor")
	coordinationBlockerListCmd.Flags().Int("limit", 100, "Page size (1-100)")

	coordinationBlockerResolveCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationBlockerResolveCmd.Flags().String("blocker", "", "Coordination blocker UUID")
	coordinationBlockerResolveCmd.Flags().String("resolution-file", "", "Strict blocker resolution JSON file (max 4096 bytes)")
	coordinationBlockerResolveCmd.Flags().String("expected-revision", "", "Expected non-negative scope revision")
	coordinationBlockerResolveCmd.Flags().String("idempotency-key", "", "Idempotency key")

	coordinationBlockerCmd.AddCommand(coordinationBlockerAddCmd, coordinationBlockerListCmd, coordinationBlockerResolveCmd)
	coordinationCmd.AddCommand(coordinationBlockerCmd)
}

func runCoordinationBlockerAdd(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	downstream, _ := cmd.Flags().GetString("downstream")
	upstream, _ := cmd.Flags().GetString("upstream")
	dependency, _ := cmd.Flags().GetString("dependency")
	payloadPath, _ := cmd.Flags().GetString("payload-file")
	key, _ := cmd.Flags().GetString("idempotency-key")
	if downstream == "" || upstream == "" || downstream != strings.TrimSpace(downstream) || upstream != strings.TrimSpace(upstream) ||
		payloadPath == "" || payloadPath != strings.TrimSpace(payloadPath) || key == "" || key != strings.TrimSpace(key) || len(key) > 200 {
		return coordinationValidationError("--downstream, --upstream, --payload-file, and --idempotency-key are required and must be valid")
	}
	if dependency != "" {
		parsed, parseErr := uuid.Parse(dependency)
		if parseErr != nil || dependency != strings.TrimSpace(dependency) {
			return coordinationValidationError("--dependency must be a UUID")
		}
		dependency = strings.ToLower(parsed.String())
	}
	expected, err := coordinationExpectedRevision(cmd)
	if err != nil {
		return err
	}
	var payload coordinationBlockerPayloadFile
	if err := readCoordinationStrictFile(payloadPath, &payload); err != nil || payload.ReasonCode == nil || payload.EvidenceRefs == nil {
		return coordinationValidationError("--payload-file must contain a strict blocker payload object")
	}
	if *payload.ReasonCode != "waiting_on_issue" {
		return coordinationValidationError("payload reason_code must be waiting_on_issue")
	}
	if err := validateCoordinationBlockerEvidence(*payload.EvidenceRefs); err != nil {
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
		ExpectedRevision  int64                          `json:"expected_revision"`
		DownstreamIssueID string                         `json:"downstream_issue_id"`
		UpstreamIssueID   string                         `json:"upstream_issue_id"`
		DependencyID      *string                        `json:"dependency_id,omitempty"`
		SchemaVersion     int32                          `json:"schema_version"`
		Payload           coordinationBlockerPayloadFile `json:"payload"`
	}{
		ExpectedRevision: expected, DownstreamIssueID: downstreamRef.ID, UpstreamIssueID: upstreamRef.ID,
		SchemaVersion: 1, Payload: payload,
	}
	if dependency != "" {
		body.DependencyID = &dependency
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", key)
	var response coordinationBlockerMutationCLIResponse
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/blockers"
	if err := client.PostJSONWithHeaders(ctx, path, body, headers, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationBlockerMutation(cmd, response)
}

func runCoordinationBlockerList(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	status, _ := cmd.Flags().GetString("status")
	cursor, _ := cmd.Flags().GetString("cursor")
	limit, _ := cmd.Flags().GetInt("limit")
	if (status != "open" && status != "resolved" && status != "all") || cursor != strings.TrimSpace(cursor) || limit < 1 || limit > 100 {
		return coordinationValidationError("--status, --cursor, and --limit must form a valid blocker page request")
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	query := url.Values{"status": []string{status}, "limit": []string{strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/blockers?" + query.Encode()
	var response coordinationBlockerPageCLIResponse
	if err := client.GetJSON(ctx, path, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationBlockerPage(cmd, response)
}

func runCoordinationBlockerResolve(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	blockerID, err := coordinationUUIDFlag(cmd, "blocker")
	if err != nil {
		return err
	}
	resolutionPath, _ := cmd.Flags().GetString("resolution-file")
	key, _ := cmd.Flags().GetString("idempotency-key")
	if resolutionPath == "" || resolutionPath != strings.TrimSpace(resolutionPath) || key == "" || key != strings.TrimSpace(key) || len(key) > 200 {
		return coordinationValidationError("--resolution-file and --idempotency-key are required and must be valid")
	}
	expected, err := coordinationExpectedRevision(cmd)
	if err != nil {
		return err
	}
	var resolution coordinationBlockerResolutionFile
	if err := readCoordinationStrictFile(resolutionPath, &resolution); err != nil || resolution.ResolutionCode == nil || resolution.EvidenceRefs == nil {
		return coordinationValidationError("--resolution-file must contain a strict blocker resolution object")
	}
	if *resolution.ResolutionCode != "no_longer_blocking" && *resolution.ResolutionCode != "superseded" {
		return coordinationValidationError("resolution_code must be no_longer_blocking or superseded")
	}
	if err := validateCoordinationBlockerEvidence(*resolution.EvidenceRefs); err != nil {
		return err
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	body := struct {
		ExpectedRevision int64                             `json:"expected_revision"`
		SchemaVersion    int32                             `json:"schema_version"`
		Resolution       coordinationBlockerResolutionFile `json:"resolution"`
	}{ExpectedRevision: expected, SchemaVersion: 1, Resolution: resolution}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", key)
	var response coordinationBlockerMutationCLIResponse
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/blockers/" + url.PathEscape(blockerID) + "/resolve"
	if err := client.PostJSONWithHeaders(ctx, path, body, headers, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationBlockerMutation(cmd, response)
}

func readCoordinationStrictFile(path string, dst any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return fmt.Errorf("coordination file exceeds 4096 bytes")
	}
	return cli.DecodeStrictCoordinationJSON(data, dst)
}

func validateCoordinationBlockerEvidence(refs []coordinationBlockerEvidenceCLI) error {
	if len(refs) > 32 {
		return coordinationValidationError("evidence_refs may contain at most 32 issue references")
	}
	seen := make(map[string]struct{}, len(refs))
	for index := range refs {
		if refs[index].Kind != "issue" {
			return coordinationValidationError("evidence_refs kind must be issue")
		}
		parsed, err := uuid.Parse(refs[index].ID)
		if err != nil {
			return coordinationValidationError("evidence_refs id must be a UUID")
		}
		refs[index].ID = parsed.String()
		key := refs[index].Kind + "\x00" + refs[index].ID
		if _, duplicate := seen[key]; duplicate {
			return coordinationValidationError("evidence_refs must not contain duplicates")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func renderCoordinationBlockerMutation(cmd *cobra.Command, response coordinationBlockerMutationCLIResponse) error {
	if coordinationOutput == "table" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "blocker=%s status=%s changed=%t replayed=%t revision=%d receipt=%s ordinal=%d\n",
			response.Resource.ID, response.Resource.Status, response.Changed, response.Replayed, response.ScopeRevision,
			response.Receipt.ID, response.Receipt.ReceiptOrdinal)
		return err
	}
	return writeCoordinationJSON(cmd, response)
}

func renderCoordinationBlockerPage(cmd *cobra.Command, response coordinationBlockerPageCLIResponse) error {
	if coordinationOutput == "table" {
		if len(response.Items) == 0 {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "No %s blockers at revision %d.\n", response.StatusFilter, response.ScopeRevision)
			return err
		}
		for _, blocker := range response.Items {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s blocked_by %s\n", blocker.ID, blocker.Status, blocker.DownstreamIssueID, blocker.UpstreamIssueID); err != nil {
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
