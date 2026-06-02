package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
)

var resourcesJSON bool

var resourcesCmd = &cobra.Command{
	Use:          "resources [tunnel-id]",
	Short:        "Show Kubernetes resources used by a Sealtun tunnel",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		payload, err := collectTunnelResources(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if resourcesJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(payload)
		}
		printTunnelResources(cmd, payload)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(resourcesCmd)
	resourcesCmd.Flags().BoolVar(&resourcesJSON, "json", false, "Output resources as JSON")
}

func collectTunnelResources(parent context.Context, tunnelID string) (*k8s.TunnelResourceList, error) {
	sess, err := activeScopedSession(tunnelID)
	if err != nil {
		return nil, err
	}
	client, err := k8sClientForSession(*sess)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	return client.WithNamespace(sess.Namespace).TunnelResources(ctx, sess.TunnelID)
}

func activeScopedSession(tunnelID string) (*session.TunnelSession, error) {
	sess, err := findSession(tunnelID)
	if err != nil {
		return nil, err
	}
	scope, err := dashboardActiveScope()
	if err != nil {
		return nil, err
	}
	if sess.Region != scope.region || sess.Namespace != scope.namespace {
		return nil, fmt.Errorf("tunnel %s is outside the active scope", sess.TunnelID)
	}
	return sess, nil
}

func printTunnelResources(cmd *cobra.Command, payload *k8s.TunnelResourceList) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Sealtun Resources")
	fmt.Fprintf(out, "  Tunnel ID: %s\n", payload.TunnelID)
	fmt.Fprintf(out, "  Namespace: %s\n", payload.Namespace)
	fmt.Fprintln(out, "  Note: resource hints show Kubernetes occupancy, not cloud billing estimates.")
	if len(payload.Resources) == 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "No Kubernetes resources were reported for this tunnel.")
		return
	}
	fmt.Fprintln(out, "")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tNAME\tSTATUS\tMANAGED\tAGE\tHINTS")
	for _, item := range payload.Resources {
		hints := append([]string{}, item.CostHints...)
		hints = append(hints, item.Warnings...)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Kind,
			item.Name,
			valueOr(item.Status, "-"),
			yesNo(item.Managed),
			valueOr(item.Age, "-"),
			valueOr(strings.Join(hints, "; "), "-"),
		)
	}
	_ = tw.Flush()
	if len(payload.Warnings) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Warnings")
		for _, warning := range payload.Warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}
}
