package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var exportAll bool
var exportOutput string
var exportJSON bool
var exportIncludeSecrets bool

var exportCmd = &cobra.Command{
	Use:          "export [tunnel-id]",
	Short:        "Export local tunnel sessions as sealtun.yaml",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return fmt.Errorf("accepts at most one tunnel id")
		}
		if exportAll && len(args) > 0 {
			return fmt.Errorf("--all cannot be combined with a tunnel id")
		}
		config, warnings, err := runExport(args)
		if err != nil {
			return err
		}
		if exportJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(config)
		}
		data, err := yaml.Marshal(config)
		if err != nil {
			return err
		}
		if strings.TrimSpace(exportOutput) != "" {
			if err := validateExportOutputPath(exportOutput); err != nil {
				return err
			}
			if err := os.WriteFile(exportOutput, data, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Exported %d tunnel(s) to %s.\n", len(config.Tunnels), exportOutput)
		} else {
			_, _ = cmd.OutOrStdout().Write(data)
		}
		for _, warning := range warnings {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.Flags().BoolVar(&exportAll, "all", false, "Export all local tunnel sessions")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Write YAML to a file")
	exportCmd.Flags().BoolVar(&exportJSON, "json", false, "Output exported configuration as JSON")
	exportCmd.Flags().BoolVar(&exportIncludeSecrets, "include-secret-placeholders", false, "Include passwordEnv/tokenEnv placeholders for configured auth")
}

func runExport(args []string) (*applyFile, []string, error) {
	var sessions []session.TunnelSession
	var err error
	switch {
	case len(args) == 1:
		sess, err := findSession(args[0])
		if err != nil {
			return nil, nil, err
		}
		sessions = []session.TunnelSession{*sess}
	case exportAll:
		sessions, err = session.List()
		if err != nil {
			return nil, nil, fmt.Errorf("load tunnel sessions: %w", err)
		}
	default:
		return nil, nil, fmt.Errorf("provide a tunnel id or use --all")
	}
	config := &applyFile{Version: "v1", Tunnels: make([]applyTunnel, 0, len(sessions))}
	warnings := []string{}
	for _, sess := range sessions {
		item, itemWarnings := exportSession(sess, exportIncludeSecrets)
		warnings = append(warnings, itemWarnings...)
		config.Tunnels = append(config.Tunnels, item)
	}
	return config, warnings, nil
}

func exportSession(sess session.TunnelSession, includeSecretPlaceholders bool) (applyTunnel, []string) {
	protocol := tunnelprotocol.Normalize(sess.Protocol)
	if protocol == "" {
		protocol = tunnelprotocol.HTTPS
	}
	item := applyTunnel{
		Name:      sess.TunnelID,
		LocalPort: parseSessionLocalPort(sess.LocalPort),
		Protocol:  protocol,
		Domain:    sess.CustomDomain,
		TTL:       sess.TTL,
	}
	warnings := []string{}
	if item.LocalPort == 0 {
		warnings = append(warnings, fmt.Sprintf("tunnel %s has no numeric local port; exported localPort as 0 and needs manual editing", sess.TunnelID))
	}
	if tunnelprotocol.IsHTTP(protocol) {
		if sess.BasicAuth != nil && sess.BasicAuth.Enabled {
			if includeSecretPlaceholders {
				item.BasicAuth = &applyBasicAuth{
					Username:    sess.BasicAuth.Username,
					PasswordEnv: envPlaceholder(sess.TunnelID, "BASIC_AUTH_PASSWORD"),
				}
			} else {
				warnings = append(warnings, fmt.Sprintf("tunnel %s uses Basic Auth; password cannot be exported because only a hash is stored", sess.TunnelID))
			}
		}
		if exportedPolicy, policyWarnings := exportAccessPolicy(sess, includeSecretPlaceholders); exportedPolicy != nil {
			item.AccessPolicy = exportedPolicy
			warnings = append(warnings, policyWarnings...)
		} else {
			warnings = append(warnings, policyWarnings...)
		}
	}
	return item, warnings
}

func exportAccessPolicy(sess session.TunnelSession, includeSecretPlaceholders bool) (*applyAccessPolicy, []string) {
	if sess.AccessPolicy == nil {
		return nil, nil
	}
	policy := &applyAccessPolicy{
		IPAllowlist: append([]string(nil), sess.AccessPolicy.IPAllowlist...),
		IPDenylist:  append([]string(nil), sess.AccessPolicy.IPDenylist...),
	}
	warnings := []string{}
	if len(sess.AccessPolicy.BearerTokenHashes) > 0 {
		if includeSecretPlaceholders {
			policy.BearerTokenEnv = envPlaceholder(sess.TunnelID, "BEARER_TOKEN")
		}
		warnings = append(warnings, fmt.Sprintf("tunnel %s has bearer token auth; token values cannot be exported because only hashes are stored", sess.TunnelID))
	}
	for i, token := range sess.AccessPolicy.TemporaryTokens {
		if includeSecretPlaceholders {
			link := applyTemporaryLink{
				Name:     token.Name,
				TTL:      token.TTL,
				TokenEnv: envPlaceholder(sess.TunnelID, fmt.Sprintf("TEMP_TOKEN_%d", i+1)),
			}
			if link.TTL == "" {
				link.ExpiresAt = token.ExpiresAt
			}
			policy.TemporaryLinks = append(policy.TemporaryLinks, link)
		} else {
			warnings = append(warnings, fmt.Sprintf("tunnel %s temporary link %q token cannot be exported because only a hash is stored", sess.TunnelID, token.Name))
		}
	}
	if policy.BearerTokenEnv == "" && len(policy.IPAllowlist) == 0 && len(policy.IPDenylist) == 0 && len(policy.TemporaryLinks) == 0 {
		return nil, warnings
	}
	return policy, warnings
}

func parseSessionLocalPort(value string) int {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func envPlaceholder(tunnelID, suffix string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_")
	name := replacer.Replace(strings.ToUpper(strings.TrimSpace(tunnelID)))
	suffix = replacer.Replace(strings.ToUpper(strings.TrimSpace(suffix)))
	return "SEALTUN_" + name + "_" + suffix
}

// validateExportOutputPath rejects writing the export through a symlink or to a
// non-regular file. A local attacker could otherwise pre-create the output path
// as a symlink to a sensitive file and have the export follow it on write.
func validateExportOutputPath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write export to %q: path is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to write export to %q: not a regular file", path)
	}
	return nil
}
