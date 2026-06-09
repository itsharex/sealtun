package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
)

type rotateServerSecretPayload struct {
	TunnelID     string `json:"tunnelId"`
	ServerSecret string `json:"serverSecret"`
	Message      string `json:"message"`
}

var rotateServerSecret bool
var rotateJSON bool

var rotateCmd = &cobra.Command{
	Use:          "rotate [tunnel-id]",
	Short:        "Rotate tunnel secrets",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !rotateServerSecret {
			return fmt.Errorf("choose what to rotate, e.g. --server-secret")
		}
		payload, err := rotateTunnelServerSecret(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if rotateJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(payload)
		}
		printRotateServerSecret(cmd, payload)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rotateCmd)
	rotateCmd.Flags().BoolVar(&rotateServerSecret, "server-secret", false, "Rotate the tunnel server secret")
	rotateCmd.Flags().BoolVar(&rotateJSON, "json", false, "Output rotation result as JSON")
}

func rotateTunnelServerSecret(ctx context.Context, tunnelID string) (*rotateServerSecretPayload, error) {
	sess, err := findSession(tunnelID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sess.Secret) == "" {
		return nil, fmt.Errorf("tunnel %s has no local secret; recreate it before rotating", sess.TunnelID)
	}
	if sess.ConnectionState == session.ConnectionStateStopped {
		return nil, fmt.Errorf("tunnel %s is stopped; run `sealtun start %s` before rotating the server secret", sess.TunnelID, sess.TunnelID)
	}
	if sessionExpired(*sess, nowUTC()) {
		return nil, fmt.Errorf("tunnel %s has expired; run cleanup and recreate the tunnel", sess.TunnelID)
	}
	newSecret := uuid.New().String()
	if err := updateTunnelServerSecret(ctx, sess, newSecret); err != nil {
		return nil, err
	}
	return &rotateServerSecretPayload{
		TunnelID:     sess.TunnelID,
		ServerSecret: newSecret,
		Message:      "New server secret is shown once and saved locally.",
	}, nil
}

func updateTunnelServerSecret(ctx context.Context, sess *session.TunnelSession, newSecret string) error {
	client, err := k8sClientForSession(*sess)
	if err != nil {
		return err
	}
	namespacedClient := client.WithNamespace(sess.Namespace)
	hosts, err := namespacedClient.EnsureTunnelWithOptions(ctx, sess.TunnelID, newSecret, sessionProtocol(*sess), sess.LocalPort, k8s.TunnelOptions{
		CustomDomain: sess.CustomDomain,
		SealosHost:   sessionSealosHostForDomain(*sess, namespacedClient.SealosHost(sess.TunnelID)),
		BasicAuth:    basicAuthToK8s(sess.BasicAuth),
		AccessPolicy: accessPolicyToK8s(sess.AccessPolicy),
	})
	if err != nil {
		return fmt.Errorf("rotate remote server secret: %w", err)
	}
	sess.Secret = newSecret
	sess.Host = hosts.PublicHost
	sess.SealosHost = hosts.SealosHost
	sess.CustomDomain = hosts.CustomDomain
	sess.PublicPort = hosts.PublicPort
	if err := session.Update(*sess); err != nil {
		return fmt.Errorf("save rotated session: %w", err)
	}
	return nil
}

func printRotateServerSecret(cmd *cobra.Command, payload *rotateServerSecretPayload) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Sealtun Server Secret Rotated")
	fmt.Fprintf(out, "  Tunnel ID: %s\n", payload.TunnelID)
	fmt.Fprintf(out, "  New server secret: %s\n", payload.ServerSecret)
	fmt.Fprintf(out, "  Note: %s\n", payload.Message)
}
