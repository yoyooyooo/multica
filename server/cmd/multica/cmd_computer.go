package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica computer` is the user-facing entry point for the RFC v6.1
// Runtime → Computer refactor. It wraps the new /api/computers/* aggregate
// endpoints (one row per (workspace_id, daemon_id) pair) and is the only
// surface the new docs / install flow reference.
//
// The legacy `multica runtime` subcommand tree is preserved unchanged but
// hidden from `--help`. Old daemons that exec `multica runtime …` keep
// working; new users only see `computer`. See §A4 / Appendix B of the RFC.
var computerCmd = &cobra.Command{
	Use:     "computer",
	Aliases: []string{"computers"},
	Short:   "Work with connected computers (replaces `runtime`)",
	Long: "List, inspect, and remove computers connected to the workspace.\n\n" +
		"A Computer is one daemon installation regardless of how many agent\n" +
		"runtimes it hosts. The legacy `multica runtime` command is hidden but\n" +
		"continues to work for compatibility.",
}

var computerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List computers connected to the workspace",
	RunE:  runComputerList,
}

var computerGetCmd = &cobra.Command{
	Use:   "get <computer-id>",
	Short: "Show details for a computer, including its agent runtimes",
	Args:  exactArgs(1),
	RunE:  runComputerGet,
}

var computerRemoveCmd = &cobra.Command{
	Use:     "remove <computer-id>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a computer from the workspace (revokes its daemon credential)",
	Args:    exactArgs(1),
	RunE:    runComputerRemove,
}

func init() {
	computerListCmd.Flags().String("output", "table", "Output format: table or json")
	computerGetCmd.Flags().String("output", "table", "Output format: table or json")
	computerRemoveCmd.Flags().String("output", "table", "Output format: table or json")

	computerCmd.AddCommand(computerListCmd)
	computerCmd.AddCommand(computerGetCmd)
	computerCmd.AddCommand(computerRemoveCmd)
}

// ---------------------------------------------------------------------------
// computer list
// ---------------------------------------------------------------------------

func runComputerList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var computers []map[string]any
	if err := client.GetJSON(ctx, "/api/computers", &computers); err != nil {
		return fmt.Errorf("list computers: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, computers)
	}

	headers := []string{"ID", "NAME", "KIND", "STATUS", "RUNTIMES", "LAST_SEEN"}
	rows := make([][]string, 0, len(computers))
	for _, c := range computers {
		rows = append(rows, []string{
			strVal(c, "id"),
			strVal(c, "name"),
			strVal(c, "kind"),
			strVal(c, "status"),
			strVal(c, "runtime_count"),
			strVal(c, "last_seen_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// computer get
// ---------------------------------------------------------------------------

func runComputerGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var computer map[string]any
	if err := client.GetJSON(ctx, "/api/computers/"+args[0], &computer); err != nil {
		return fmt.Errorf("get computer: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, computer)
	}

	// Two-section table: top is the computer header, bottom is per-runtime.
	fmt.Fprintf(os.Stdout, "ID:         %s\n", strVal(computer, "id"))
	fmt.Fprintf(os.Stdout, "Name:       %s\n", strVal(computer, "name"))
	fmt.Fprintf(os.Stdout, "Kind:       %s\n", strVal(computer, "kind"))
	fmt.Fprintf(os.Stdout, "Status:     %s\n", strVal(computer, "status"))
	fmt.Fprintf(os.Stdout, "Last seen:  %s\n", strVal(computer, "last_seen_at"))
	if di := strVal(computer, "device_info"); di != "" {
		fmt.Fprintf(os.Stdout, "Device:     %s\n", di)
	}
	if src := strVal(computer, "install_source"); src != "" {
		fmt.Fprintf(os.Stdout, "Source:     %s\n", src)
	}

	runtimes, _ := computer["runtimes"].([]any)
	if len(runtimes) == 0 {
		fmt.Fprintln(os.Stdout, "\nNo agent runtimes registered.")
		return nil
	}
	fmt.Fprintln(os.Stdout)
	headers := []string{"RUNTIME_ID", "NAME", "PROVIDER", "VERSION", "STATUS", "LAST_SEEN"}
	rows := make([][]string, 0, len(runtimes))
	for _, rt := range runtimes {
		m, _ := rt.(map[string]any)
		rows = append(rows, []string{
			strVal(m, "id"),
			strVal(m, "name"),
			strVal(m, "provider"),
			strVal(m, "version"),
			strVal(m, "status"),
			strVal(m, "last_seen_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// ---------------------------------------------------------------------------
// computer remove
// ---------------------------------------------------------------------------

func runComputerRemove(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/computers/"+args[0]); err != nil {
		// Surface the 409 active_agents response from the server with a
		// human-readable message. Per §6.3 / D2, the server refuses when
		// any agent runtime under this daemon has active runs — there is
		// no force override; archive or reassign the agents first.
		if msg, n, ok := parseActiveAgentsConflict(err); ok {
			plural := "agent"
			if n != 1 {
				plural = "agents"
			}
			return fmt.Errorf("cannot remove: computer has %d active %s. %s", n, plural, msg)
		}
		return fmt.Errorf("remove computer: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Removed computer %s\n", args[0])
	return nil
}

// parseActiveAgentsConflict extracts the active_agents field from the 409
// response the DELETE handler returns. The body shape is
//
//	{ "error": "<human readable message>", "active_agents": N }
//
// We surface N + the server's message so the user knows how many runs are
// blocking the removal and what to do about it.
func parseActiveAgentsConflict(err error) (string, int, bool) {
	var herr *cli.HTTPError
	if !errors.As(err, &herr) {
		return "", 0, false
	}
	if herr.StatusCode != http.StatusConflict {
		return "", 0, false
	}
	body := strings.TrimSpace(herr.Body)
	if body == "" {
		return "", 0, false
	}
	var payload struct {
		Error        string `json:"error"`
		ActiveAgents int    `json:"active_agents"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", 0, false
	}
	if payload.Error == "" {
		return "", 0, false
	}
	return payload.Error, payload.ActiveAgents, true
}
