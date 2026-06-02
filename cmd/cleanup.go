package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
)

var cleanupAll bool

var cleanupCmd = &cobra.Command{
	Use:   "cleanup [tunnel-id]",
	Short: "Clean up stopped, expired, stale, or managed Sealtun tunnel resources",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if cleanupAll && len(args) > 0 {
			return fmt.Errorf("--all cannot be used with a specific tunnel id")
		}
		if len(args) > 0 {
			sess, err := findSession(args[0])
			if err != nil {
				return err
			}
			if !cleanupAll && !sessionCleanupEligible(*sess, time.Minute) {
				return fmt.Errorf("tunnel %s is not stopped, expired, stale, or error; refusing cleanup without --all", sess.TunnelID)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if err := cleanupSessionResources(ctx, *sess); err != nil {
				return fmt.Errorf("cleanup tunnel %s: %w", sess.TunnelID, err)
			}
			if err := session.Delete(sess.TunnelID); err != nil {
				return fmt.Errorf("delete local session %s: %w", sess.TunnelID, err)
			}
			fmt.Printf("Cleanup complete. Removed tunnel %s and its remote resources.\n", sess.TunnelID)
			return nil
		}
		sessions, err := session.List()
		if err != nil {
			return fmt.Errorf("load local session records: %w", err)
		}

		if cleanupAll {
			removed := 0
			failed := 0
			for _, sess := range sessions {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				if err := cleanupSessionResources(ctx, sess); err != nil {
					cancel()
					failed++
					fmt.Fprintf(cmd.ErrOrStderr(), "[!] Skipped tunnel %s: %v\n", sess.TunnelID, err)
					continue
				}
				cancel()
				if err := session.Delete(sess.TunnelID); err != nil {
					return fmt.Errorf("delete local session %s: %w", sess.TunnelID, err)
				}
				removed++
			}

			fmt.Printf("Cleanup complete. Removed %d Sealtun tunnel session(s) and their remote resources.\n", removed)
			if failed > 0 {
				return fmt.Errorf("failed to clean up %d tunnel session(s); local records were kept", failed)
			}
			return nil
		}

		cleaned := 0
		skipped := 0
		failed := 0
		for _, sess := range sessions {
			if !sessionCleanupEligible(sess, time.Minute) {
				skipped++
				continue
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			if err := cleanupSessionResources(ctx, sess); err != nil {
				cancel()
				failed++
				if errors.Is(err, errMissingSessionKubeconfig) {
					fmt.Fprintf(cmd.ErrOrStderr(), "[!] Skipped cleanup-eligible tunnel %s: %v\n", sess.TunnelID, err)
					continue
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "[!] Failed to clean up cleanup-eligible tunnel %s: %v\n", sess.TunnelID, err)
				continue
			}
			cancel()
			if err := session.Delete(sess.TunnelID); err != nil {
				return fmt.Errorf("delete local session %s: %w", sess.TunnelID, err)
			}
			cleaned++
		}

		fmt.Printf("Cleanup complete. Removed %d stopped, expired, stale, or error tunnels; skipped %d active session records.\n", cleaned, skipped)
		if failed > 0 {
			return fmt.Errorf("failed to clean up %d cleanup-eligible tunnel session(s); local records were kept", failed)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
	cleanupCmd.Flags().BoolVar(&cleanupAll, "all", false, "Force delete all locally tracked Sealtun tunnel resources and remove matching local session records")
}
