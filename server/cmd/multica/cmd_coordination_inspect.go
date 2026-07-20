package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var coordinationInspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Read one consistent passive coordination scope snapshot",
	Args:  coordinationNoArgs,
	RunE:  runCoordinationInspect,
}

type coordinationReceiptRefCLI struct {
	ID             string `json:"id"`
	ReceiptOrdinal int64  `json:"receipt_ordinal"`
	Operation      string `json:"operation"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id"`
	RevisionBefore int64  `json:"revision_before"`
	RevisionAfter  int64  `json:"revision_after"`
	ActorType      string `json:"actor_type"`
	CreatedAt      string `json:"created_at"`
}

type coordinationInspectionCLIResponse struct {
	Scope              coordinationScopeCLI        `json:"scope"`
	ScopeRevision      int64                       `json:"scope_revision"`
	ActiveDependencies []coordinationDependencyCLI `json:"active_dependencies"`
	OpenBlockers       []coordinationBlockerCLI    `json:"open_blockers"`
	ReceiptRefs        []coordinationReceiptRefCLI `json:"receipt_refs"`
	NextReceiptCursor  *string                     `json:"next_receipt_cursor"`
}

func init() {
	coordinationInspectCmd.Flags().String("scope", "", "Coordination scope UUID")
	coordinationInspectCmd.Flags().String("receipt-cursor", "", "Opaque receipt page cursor")
	coordinationCmd.AddCommand(coordinationInspectCmd)
}

func runCoordinationInspect(cmd *cobra.Command, _ []string) error {
	scopeID, err := coordinationUUIDFlag(cmd, "scope")
	if err != nil {
		return err
	}
	receiptCursor, _ := cmd.Flags().GetString("receipt-cursor")
	if receiptCursor != strings.TrimSpace(receiptCursor) || len(receiptCursor) > 3000 {
		return coordinationValidationError("--receipt-cursor must be a valid opaque cursor")
	}
	client, err := coordinationClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	path := "/api/coordination/scopes/" + url.PathEscape(scopeID) + "/inspect"
	if receiptCursor != "" {
		path += "?" + url.Values{"receipt_cursor": []string{receiptCursor}}.Encode()
	}
	var response coordinationInspectionCLIResponse
	if err := client.GetJSON(ctx, path, &response); err != nil {
		return cli.CoordinationProductError(err)
	}
	return renderCoordinationInspection(cmd, response)
}

func renderCoordinationInspection(cmd *cobra.Command, response coordinationInspectionCLIResponse) error {
	if coordinationOutput != "table" {
		return writeCoordinationJSON(cmd, response)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "scope=%s revision=%d active_dependencies=%d open_blockers=%d receipt_refs=%d\n",
		response.Scope.ID, response.ScopeRevision, len(response.ActiveDependencies), len(response.OpenBlockers), len(response.ReceiptRefs)); err != nil {
		return err
	}
	for _, dependency := range response.ActiveDependencies {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "dependency %s  %s blocked_by %s\n", dependency.ID, dependency.DownstreamIssueID, dependency.UpstreamIssueID); err != nil {
			return err
		}
	}
	for _, blocker := range response.OpenBlockers {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "blocker %s  %s blocked_by %s  reason=%s\n", blocker.ID, blocker.DownstreamIssueID, blocker.UpstreamIssueID, blocker.ReasonCode); err != nil {
			return err
		}
	}
	for _, receipt := range response.ReceiptRefs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "receipt %d  %s %s=%s actor=%s revision=%d..%d\n",
			receipt.ReceiptOrdinal, receipt.Operation, receipt.ResourceType, receipt.ResourceID, receipt.ActorType,
			receipt.RevisionBefore, receipt.RevisionAfter); err != nil {
			return err
		}
	}
	if response.NextReceiptCursor != nil {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "next_receipt_cursor=%s revision=%d\n", *response.NextReceiptCursor, response.ScopeRevision)
		return err
	}
	return nil
}
