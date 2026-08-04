package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

type issueMergeDelegation struct {
	ID      string `json:"id"`
	IssueID string `json:"issue_id"`
	State   string `json:"state"`
}

type issueMergeDelegationList struct {
	Delegations []issueMergeDelegation `json:"delegations"`
}

var issueMergeCmd = &cobra.Command{
	Use:   "merge",
	Short: "Inspect or approve the server-derived merge authority for an issue",
}

var issueMergeStatusCmd = &cobra.Command{
	Use:   "status <issue>",
	Short: "Show the current server-derived merge approval request",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, issueRef, list, err := loadIssueMergeDelegations(cmd, args[0])
		_ = client
		if err != nil {
			return err
		}
		output, _ := cmd.Flags().GetString("output")
		if output == "json" {
			return cli.PrintJSON(os.Stdout, map[string]any{"issue_id": issueRef.ID, "delegations": list.Delegations})
		}
		rows := make([][]string, 0, len(list.Delegations))
		for _, row := range list.Delegations {
			rows = append(rows, []string{row.ID, row.State})
		}
		cli.PrintTable(os.Stdout, []string{"DELEGATION", "STATE"}, rows)
		return nil
	},
}

var issueMergeApproveCmd = &cobra.Command{
	Use:   "approve <issue>",
	Short: "Approve the one current exact merge request derived by the server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateIssueMergeDelegation(cmd, args[0], "approve", "pending_approval")
	},
}

var issueMergeRevokeCmd = &cobra.Command{
	Use:   "revoke <issue>",
	Short: "Revoke the one current unconsumed merge request derived by the server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateIssueMergeDelegation(cmd, args[0], "revoke", "")
	},
}

func loadIssueMergeDelegations(cmd *cobra.Command, issue string) (*cli.APIClient, resolvedID, issueMergeDelegationList, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, resolvedID{}, issueMergeDelegationList{}, err
	}
	if client.WorkspaceID == "" {
		workspaceID, err := requireWorkspaceID(cmd)
		if err != nil {
			return nil, resolvedID{}, issueMergeDelegationList{}, err
		}
		client.WorkspaceID = workspaceID
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, issue)
	if err != nil {
		return nil, resolvedID{}, issueMergeDelegationList{}, fmt.Errorf("resolve issue: %w", err)
	}
	path := "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/workload-delegations/pr-merge?issue_id=" + url.QueryEscape(issueRef.ID)
	var list issueMergeDelegationList
	if err := client.GetJSON(ctx, path, &list); err != nil {
		return nil, resolvedID{}, list, fmt.Errorf("read merge authority: %w", err)
	}
	return client, issueRef, list, nil
}

func mutateIssueMergeDelegation(cmd *cobra.Command, issue, action, requiredState string) error {
	client, issueRef, list, err := loadIssueMergeDelegations(cmd, issue)
	if err != nil {
		return err
	}
	candidates := make([]issueMergeDelegation, 0, 1)
	for _, row := range list.Delegations {
		if row.IssueID != issueRef.ID {
			continue
		}
		if requiredState != "" {
			if row.State == requiredState {
				candidates = append(candidates, row)
			}
		} else if row.State == "pending_approval" || row.State == "approved" {
			candidates = append(candidates, row)
		}
	}
	if len(candidates) != 1 {
		return fmt.Errorf("issue has %d eligible merge authority requests; expected exactly one", len(candidates))
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/workload-delegations/pr-merge/" + url.PathEscape(candidates[0].ID) + "/" + action
	var result map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{}, &result); err != nil {
		return fmt.Errorf("%s merge authority: %w", action, err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stdout, "%s merge authority %s for %s\n", action, candidates[0].ID, issueRef.Display)
	return nil
}

func init() {
	issueMergeCmd.AddCommand(issueMergeStatusCmd, issueMergeApproveCmd, issueMergeRevokeCmd)
	issueCmd.AddCommand(issueMergeCmd)
}
