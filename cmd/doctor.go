package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
)

const doctorRemoteTimeout = 12 * time.Second
const doctorRemoteConcurrency = 4

type doctorPayload struct {
	DaemonRunning        bool     `json:"daemonRunning"`
	LoggedIn             bool     `json:"loggedIn"`
	KubeconfigPresent    bool     `json:"kubeconfigPresent"`
	TotalSessions        int      `json:"totalSessions"`
	ActiveSessions       int      `json:"activeSessions"`
	ConnectingSessions   int      `json:"connectingSessions"`
	ErrorSessions        int      `json:"errorSessions"`
	DegradedSessions     int      `json:"degradedSessions"`
	StoppedSessions      int      `json:"stoppedSessions"`
	StaleSessions        int      `json:"staleSessions"`
	ReachableActivePorts int      `json:"reachableActivePorts"`
	RemoteChecked        int      `json:"remoteChecked"`
	RemoteIssues         int      `json:"remoteIssues"`
	Warnings             []string `json:"warnings,omitempty"`
}

type tunnelDoctorPayload struct {
	TunnelID           string                 `json:"tunnelId"`
	Status             string                 `json:"status"`
	Protocol           string                 `json:"protocol,omitempty"`
	Endpoint           string                 `json:"endpoint,omitempty"`
	LocalTarget        string                 `json:"localTarget,omitempty"`
	Mode               string                 `json:"mode,omitempty"`
	Namespace          string                 `json:"namespace,omitempty"`
	ProcessAlive       bool                   `json:"processAlive"`
	LocalPortReachable bool                   `json:"localPortReachable"`
	Remote             *k8s.TunnelDiagnostics `json:"remote,omitempty"`
	Checks             []doctorCheck          `json:"checks"`
	Suggestions        []string               `json:"suggestions,omitempty"`
	Warnings           []string               `json:"warnings,omitempty"`
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

var doctorJSON bool
var doctorFix bool
var doctorFixDryRun bool

type doctorFixPayload struct {
	DryRun  bool              `json:"dryRun"`
	Actions []doctorFixAction `json:"actions"`
}

type doctorFixAction struct {
	Action   string `json:"action"`
	TunnelID string `json:"tunnelId,omitempty"`
	Command  string `json:"command,omitempty"`
	Reason   string `json:"reason"`
	Allowed  bool   `json:"allowed"`
	Executed bool   `json:"executed,omitempty"`
	Error    string `json:"error,omitempty"`
}

var doctorFixStartTunnel = startTunnelSession
var doctorFixCleanupResources = cleanupSessionResources
var doctorFixEnsureDaemon = ensureDaemonRunning

var doctorCmd = &cobra.Command{
	Use:          "doctor [tunnel-id]",
	Short:        "Run Sealtun diagnostics",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorFixDryRun && !doctorFix {
			return fmt.Errorf("--dry-run requires --fix")
		}
		if doctorFix {
			payload, err := runDoctorFix(cmd.Context(), args, doctorFixDryRun)
			if err != nil {
				return err
			}
			if doctorJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(payload); err != nil {
					return err
				}
				return doctorFixExecutionError(payload)
			}
			printDoctorFix(cmd, payload)
			return doctorFixExecutionError(payload)
		}
		if len(args) > 0 {
			payload, err := collectTunnelDoctorPayload(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if doctorJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}
			printTunnelDoctor(cmd, payload)
			return nil
		}

		payload, err := collectDoctorPayloadWithContext(cmd.Context())
		if err != nil {
			return err
		}

		if doctorJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(payload)
		}

		printDoctor(cmd, payload)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Output diagnostics as JSON")
	doctorCmd.Flags().BoolVar(&doctorFix, "fix", false, "Execute conservative automatic fixes")
	doctorCmd.Flags().BoolVar(&doctorFixDryRun, "dry-run", false, "Show conservative automatic fixes without executing them")
}

func collectDoctorPayload() (*doctorPayload, error) {
	return collectDoctorPayloadWithContext(context.Background())
}

func collectTunnelDoctorPayload(ctx context.Context, tunnelID string) (*tunnelDoctorPayload, error) {
	sess, err := findSession(tunnelID)
	if err != nil {
		return nil, err
	}
	ensureSessionPublicPort(ctx, sess)

	snapshot := classifySession(*sess, true)
	endpoint := endpointLabel(sess.Protocol, sess.Host, sess.SealosHost, sess.PublicPort)
	payload := &tunnelDoctorPayload{
		TunnelID:           sess.TunnelID,
		Status:             snapshot.Status,
		Protocol:           valueOr(sess.Protocol, "https"),
		Endpoint:           endpoint,
		LocalTarget:        "localhost:" + valueOr(sess.LocalPort, "unknown"),
		Mode:               valueOr(sess.Mode, "foreground"),
		Namespace:          sess.Namespace,
		ProcessAlive:       snapshot.ProcessAlive,
		LocalPortReachable: snapshot.LocalPortReachable,
	}

	payload.Checks = append(payload.Checks,
		doctorCheck{Name: "session", Status: "ok", Detail: "local session record exists"},
		ownerDoctorCheck(*sess, snapshot.ProcessAlive),
		localPortDoctorCheck(*sess, snapshot.LocalPortReachable),
	)

	remoteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	remote, err := collectRemoteDiagnosticsWithContext(remoteCtx, *sess)
	cancel()
	if err != nil {
		payload.Checks = append(payload.Checks, doctorCheck{Name: "remote", Status: "warn", Detail: err.Error()})
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("remote diagnostics unavailable: %v", err))
	} else {
		payload.Remote = remote
		payload.Checks = append(payload.Checks, remoteDoctorChecks(remote)...)
		payload.Warnings = append(payload.Warnings, remote.Warnings...)
	}

	payload.Suggestions = tunnelDoctorSuggestions(*sess, payload)
	return payload, nil
}

func collectDoctorPayloadWithContext(ctx context.Context) (*doctorPayload, error) {
	status, err := collectStatus()
	if err != nil {
		return nil, err
	}

	items, err := collectListItemsWithLocalCheck(true)
	if err != nil {
		return nil, err
	}
	return collectDoctorPayloadFromItems(ctx, status, items, collectRemoteDiagnosticsWithContext)
}

func collectDoctorPayloadFromItems(ctx context.Context, status *statusPayload, items []listItem, remoteCollector remoteDiagnosticsCollector) (*doctorPayload, error) {
	payload := &doctorPayload{
		DaemonRunning:     status.DaemonRunning,
		LoggedIn:          status.LoggedIn,
		KubeconfigPresent: status.Kubeconfig.Present,
		TotalSessions:     len(items),
		Warnings:          append([]string{}, status.Warnings...),
	}

	for _, item := range items {
		switch item.Status {
		case "active":
			payload.ActiveSessions++
			payload.ReachableActivePorts++
		case "degraded":
			payload.DegradedSessions++
		case "connecting":
			payload.ConnectingSessions++
		case "error":
			payload.ErrorSessions++
		case "stopped":
			payload.StoppedSessions++
		default:
			payload.StaleSessions++
		}
	}
	runDoctorRemoteDiagnostics(ctx, payload, items, remoteCollector)

	if payload.TotalSessions == 0 {
		payload.Warnings = append(payload.Warnings, "no local tunnel sessions found")
	}
	daemonManaged := 0
	for _, item := range items {
		if item.Mode == "daemon" {
			daemonManaged++
		}
	}
	if daemonManaged > 0 && !payload.DaemonRunning {
		payload.Warnings = append(payload.Warnings, "daemon is not running; daemon-managed tunnels will not reconnect until it starts")
	}
	if payload.StaleSessions > 0 {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("%d stale tunnel session(s) found; consider running `sealtun cleanup`", payload.StaleSessions))
	}
	if payload.StoppedSessions > 0 {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("%d stopped tunnel session(s) found; consider running `sealtun cleanup`", payload.StoppedSessions))
	}
	if payload.ConnectingSessions > 0 {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("%d tunnel session(s) are still connecting", payload.ConnectingSessions))
	}
	if payload.ErrorSessions > 0 {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("%d tunnel session(s) are in error state; inspect them for the last error", payload.ErrorSessions))
	}
	if payload.DegradedSessions > 0 {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("%d tunnel session(s) have a live owner but unreachable local port", payload.DegradedSessions))
	}

	return payload, nil
}

func runDoctorFix(ctx context.Context, args []string, dryRun bool) (*doctorFixPayload, error) {
	sessions, err := session.List()
	if err != nil {
		return nil, fmt.Errorf("load local session records: %w", err)
	}
	if len(args) > 0 {
		target := args[0]
		filtered := sessions[:0]
		for _, sess := range sessions {
			if sess.TunnelID == target {
				filtered = append(filtered, sess)
			}
		}
		if len(filtered) == 0 {
			if _, err := findSession(target); err != nil {
				return nil, err
			}
		}
		sessions = filtered
	}
	status, err := collectStatus()
	if err != nil {
		return nil, err
	}
	payload := &doctorFixPayload{DryRun: dryRun}
	if !status.DaemonRunning && hasDaemonSessionNeedingDaemon(sessions) {
		payload.Actions = append(payload.Actions, doctorFixAction{
			Action:  "daemon-start",
			Command: "sealtun daemon",
			Reason:  "daemon-managed tunnel sessions exist but the local daemon is not running",
			Allowed: true,
		})
	}
	for _, sess := range sessions {
		payload.Actions = append(payload.Actions, doctorFixActionsForSession(sess)...)
	}
	if dryRun {
		return payload, nil
	}
	for i := range payload.Actions {
		if !payload.Actions[i].Allowed {
			continue
		}
		err := executeDoctorFixAction(ctx, payload.Actions[i])
		if err != nil {
			payload.Actions[i].Error = err.Error()
			continue
		}
		payload.Actions[i].Executed = true
	}
	return payload, nil
}

func hasDaemonSessionNeedingDaemon(sessions []session.TunnelSession) bool {
	now := time.Now()
	for _, sess := range sessions {
		if sess.Mode == "daemon" && sess.ConnectionState != session.ConnectionStateStopped && !sessionExpired(sess, now) {
			return true
		}
	}
	return false
}

func doctorFixActionsForSession(sess session.TunnelSession) []doctorFixAction {
	if sess.ConnectionState == session.ConnectionStateStopped {
		if sessionExpired(sess, time.Now()) {
			return []doctorFixAction{{
				Action:   "cleanup",
				TunnelID: sess.TunnelID,
				Command:  commandForTunnelAction("cleanup", sess.TunnelID),
				Reason:   "stopped tunnel session has expired",
				Allowed:  true,
			}}
		}
		if strings.TrimSpace(sess.Secret) == "" {
			return []doctorFixAction{{
				Action:   "start",
				TunnelID: sess.TunnelID,
				Command:  commandForTunnelAction("start", sess.TunnelID),
				Reason:   "stopped tunnel has no local secret and must be recreated",
				Allowed:  false,
			}}
		}
		return []doctorFixAction{{
			Action:   "start",
			TunnelID: sess.TunnelID,
			Command:  commandForTunnelAction("start", sess.TunnelID),
			Reason:   "tunnel is stopped and can be resumed",
			Allowed:  true,
		}}
	}
	if sessionExpired(sess, time.Now()) {
		return []doctorFixAction{{
			Action:   "cleanup",
			TunnelID: sess.TunnelID,
			Command:  commandForTunnelAction("cleanup", sess.TunnelID),
			Reason:   "tunnel session has expired",
			Allowed:  true,
		}}
	}
	if sess.Mode == "daemon" {
		return nil
	}
	if session.IsStaleWithOwner(sess, time.Minute, sessionOwnerAlive(sess)) {
		return []doctorFixAction{{
			Action:   "cleanup",
			TunnelID: sess.TunnelID,
			Command:  commandForTunnelAction("cleanup", sess.TunnelID),
			Reason:   "tunnel session is stale and no active owner is keeping it alive",
			Allowed:  true,
		}}
	}
	return nil
}

func executeDoctorFixAction(ctx context.Context, action doctorFixAction) error {
	switch action.Action {
	case "daemon-start":
		return doctorFixEnsureDaemon()
	case "start":
		sess, err := findSession(action.TunnelID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(sess.Secret) == "" {
			return fmt.Errorf("tunnel %s cannot be started because its local secret is unavailable", sess.TunnelID)
		}
		if sessionExpired(*sess, time.Now()) {
			return fmt.Errorf("tunnel %s has expired", sess.TunnelID)
		}
		return doctorFixStartTunnel(ctx, sess)
	case "cleanup":
		sess, err := findSession(action.TunnelID)
		if err != nil {
			return err
		}
		if !sessionExpired(*sess, time.Now()) && !session.IsStaleWithOwner(*sess, time.Minute, sessionOwnerAlive(*sess)) {
			return fmt.Errorf("refusing to cleanup non-stale active tunnel %s", sess.TunnelID)
		}
		if sess.Mode == "daemon" && !sessionExpired(*sess, time.Now()) {
			return fmt.Errorf("refusing to cleanup daemon-managed active tunnel %s", sess.TunnelID)
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, tunnelCleanupTimeout)
		defer cancel()
		if err := doctorFixCleanupResources(cleanupCtx, *sess); err != nil {
			return err
		}
		return session.Delete(sess.TunnelID)
	default:
		return fmt.Errorf("unknown fix action %q", action.Action)
	}
}

func doctorFixExecutionError(payload *doctorFixPayload) error {
	if payload == nil || payload.DryRun {
		return nil
	}
	failed := 0
	for _, action := range payload.Actions {
		if action.Allowed && action.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("failed to execute %d doctor fix action(s)", failed)
	}
	return nil
}

func printDoctorFix(cmd *cobra.Command, payload *doctorFixPayload) {
	out := cmd.OutOrStdout()
	if payload.DryRun {
		fmt.Fprintln(out, "Sealtun Doctor Fix Plan")
	} else {
		fmt.Fprintln(out, "Sealtun Doctor Fix Results")
	}
	if len(payload.Actions) == 0 {
		fmt.Fprintln(out, "  No conservative automatic fixes are available.")
		return
	}
	for _, action := range payload.Actions {
		status := "planned"
		if !action.Allowed {
			status = "blocked"
		} else if action.Executed {
			status = "executed"
		}
		if action.Error != "" {
			status = "failed"
		}
		target := action.TunnelID
		if target == "" {
			target = "local"
		}
		fmt.Fprintf(out, "  - %s %s: %s\n", action.Action, target, status)
		fmt.Fprintf(out, "    Reason: %s\n", action.Reason)
		if action.Command != "" {
			fmt.Fprintf(out, "    Command: %s\n", action.Command)
		}
		if action.Error != "" {
			fmt.Fprintf(out, "    Error: %s\n", action.Error)
		}
	}
}

func runDoctorRemoteDiagnostics(parent context.Context, payload *doctorPayload, items []listItem, remoteCollector remoteDiagnosticsCollector) {
	if remoteCollector == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, doctorRemoteTimeout)
	defer cancel()

	type result struct {
		tunnelID string
		checked  bool
		warnings []string
		err      error
	}

	jobs := make(chan listItem, len(items))
	results := make(chan result, len(items))
	var wg sync.WaitGroup
	queued := 0

	workerCount := doctorRemoteConcurrency
	if workerCount < 1 {
		workerCount = 1
	}
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				sess, err := findSession(item.TunnelID)
				if err != nil {
					select {
					case results <- result{tunnelID: item.TunnelID, err: fmt.Errorf("session disappeared during diagnostics: %w", err)}:
					case <-ctx.Done():
					}
					continue
				}
				remote, err := remoteCollector(ctx, *sess)
				if err != nil {
					if ctx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
						return
					}
					select {
					case results <- result{tunnelID: item.TunnelID, err: fmt.Errorf("remote diagnostics unavailable: %w", err)}:
					case <-ctx.Done():
					}
					continue
				}
				select {
				case results <- result{tunnelID: item.TunnelID, checked: true, warnings: remote.Warnings}:
				case <-ctx.Done():
				}
			}
		}()
	}

enqueue:
	for _, item := range items {
		if item.Status == "stopped" || item.Status == "stale" {
			continue
		}
		select {
		case jobs <- item:
			queued++
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if result.checked {
			payload.RemoteChecked++
		}
		if result.err != nil {
			if ctx.Err() != nil && (errors.Is(result.err, context.DeadlineExceeded) || errors.Is(result.err, context.Canceled)) {
				continue
			}
			payload.RemoteIssues++
			payload.Warnings = append(payload.Warnings, fmt.Sprintf("tunnel %s %v", result.tunnelID, result.err))
			continue
		}
		if len(result.warnings) > 0 {
			payload.RemoteIssues++
			for _, warning := range result.warnings {
				payload.Warnings = append(payload.Warnings, fmt.Sprintf("tunnel %s: %s", result.tunnelID, warning))
			}
		}
	}

	if queued > 0 && ctx.Err() != nil {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("remote diagnostics stopped after %s; some tunnels may not have been checked", doctorRemoteTimeout))
	}
}

func printDoctor(cmd *cobra.Command, payload *doctorPayload) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Sealtun Doctor")
	fmt.Fprintf(out, "  Daemon running: %s\n", yesNo(payload.DaemonRunning))
	fmt.Fprintf(out, "  Logged in: %s\n", yesNo(payload.LoggedIn))
	fmt.Fprintf(out, "  Kubeconfig present: %s\n", yesNo(payload.KubeconfigPresent))
	fmt.Fprintf(out, "  Sessions: %d total, %d active, %d degraded, %d connecting, %d error, %d stopped, %d stale\n", payload.TotalSessions, payload.ActiveSessions, payload.DegradedSessions, payload.ConnectingSessions, payload.ErrorSessions, payload.StoppedSessions, payload.StaleSessions)
	fmt.Fprintf(out, "  Reachable active local ports: %d\n", payload.ReachableActivePorts)
	fmt.Fprintf(out, "  Remote checks: %d checked, %d with issues\n", payload.RemoteChecked, payload.RemoteIssues)

	if len(payload.Warnings) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Warnings")
		for _, warning := range payload.Warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	} else {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "No issues detected from local diagnostics.")
	}
}

func printTunnelDoctor(cmd *cobra.Command, payload *tunnelDoctorPayload) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Sealtun Tunnel Doctor")
	fmt.Fprintf(out, "  Tunnel ID: %s\n", payload.TunnelID)
	fmt.Fprintf(out, "  Status: %s\n", payload.Status)
	fmt.Fprintf(out, "  Protocol: %s\n", payload.Protocol)
	fmt.Fprintf(out, "  Endpoint: %s\n", valueOr(payload.Endpoint, "-"))
	fmt.Fprintf(out, "  Local target: %s\n", payload.LocalTarget)
	fmt.Fprintf(out, "  Mode: %s\n", valueOr(payload.Mode, "unknown"))
	fmt.Fprintf(out, "  Namespace: %s\n", valueOr(payload.Namespace, "unknown"))

	if len(payload.Checks) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Checks")
		for _, check := range payload.Checks {
			fmt.Fprintf(out, "  - %s: %s", check.Name, check.Status)
			if check.Detail != "" {
				fmt.Fprintf(out, " (%s)", check.Detail)
			}
			fmt.Fprintln(out)
		}
	}

	if len(payload.Suggestions) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Suggestions")
		for _, suggestion := range payload.Suggestions {
			fmt.Fprintf(out, "  - %s\n", suggestion)
		}
	}

	if len(payload.Warnings) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Warnings")
		for _, warning := range payload.Warnings {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}
}

func checkStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "warn"
}

func ownerDoctorCheck(sess session.TunnelSession, alive bool) doctorCheck {
	status := checkStatus(alive)
	if sess.ConnectionState == session.ConnectionStateStopped {
		status = "skip"
	}
	return doctorCheck{Name: "owner", Status: status, Detail: ownerCheckDetail(sess, alive)}
}

func ownerCheckDetail(sess session.TunnelSession, alive bool) string {
	if alive {
		return "owner process is alive"
	}
	if sess.ConnectionState == session.ConnectionStateStopped {
		return "tunnel is stopped"
	}
	if sess.Mode == "daemon" {
		return "local daemon is not running"
	}
	return "recorded process is not running"
}

func localPortDoctorCheck(sess session.TunnelSession, reachable bool) doctorCheck {
	status := checkStatus(reachable)
	if sess.ConnectionState == session.ConnectionStateStopped {
		status = "skip"
	}
	return doctorCheck{Name: "local-port", Status: status, Detail: localPortCheckDetail(sess, reachable)}
}

func localPortCheckDetail(sess session.TunnelSession, reachable bool) string {
	if reachable {
		return "local target accepts TCP connections"
	}
	if strings.TrimSpace(sess.LocalPort) == "" {
		return "local port is missing from the session"
	}
	if sess.ConnectionState == session.ConnectionStateStopped {
		return "not checked because the tunnel is stopped"
	}
	return fmt.Sprintf("localhost:%s is not reachable", sess.LocalPort)
}

func remoteDoctorChecks(remote *k8s.TunnelDiagnostics) []doctorCheck {
	if remote == nil {
		return nil
	}
	deploymentStatus := checkStatus(remote.Deployment.Exists && remote.Deployment.ReadyReplicas > 0)
	if remote.Deployment.Exists && remote.Deployment.DesiredReplicas == 0 {
		deploymentStatus = "skip"
	}
	checks := []doctorCheck{
		{Name: "deployment", Status: deploymentStatus, Detail: fmt.Sprintf("%d/%d ready", remote.Deployment.ReadyReplicas, remote.Deployment.DesiredReplicas)},
		{Name: "service", Status: checkStatus(remote.Service.Exists), Detail: valueOr(strings.Join(remote.Service.Ports, ", "), "no ports reported")},
	}
	if remote.Ingress.Exists {
		checks = append(checks, doctorCheck{Name: "ingress", Status: "ok", Detail: strings.Join(remote.Ingress.Hosts, ", ")})
	} else {
		checks = append(checks, doctorCheck{Name: "ingress", Status: "warn", Detail: "missing"})
	}
	if remote.Certificate != nil {
		status := checkStatus(remote.Certificate.Exists && remote.Certificate.Ready)
		checks = append(checks, doctorCheck{Name: "certificate", Status: status, Detail: certificateDoctorDetail(remote.Certificate)})
	}
	return checks
}

func certificateDoctorDetail(cert *k8s.CertificateDiagnostics) string {
	if cert == nil || !cert.Exists {
		return "missing"
	}
	if cert.Ready {
		return "ready"
	}
	return "not ready"
}

func tunnelDoctorSuggestions(sess session.TunnelSession, payload *tunnelDoctorPayload) []string {
	suggestions := []string{}
	if payload.Status == "stopped" {
		suggestions = append(suggestions, fmt.Sprintf("run `sealtun start %s` to resume the tunnel", sess.TunnelID))
		return suggestions
	}
	if !payload.ProcessAlive && sess.Mode == "daemon" {
		suggestions = append(suggestions, "run `sealtun status` to check the daemon, then restart the tunnel if needed")
	}
	if !payload.LocalPortReachable && strings.TrimSpace(sess.LocalPort) != "" && payload.Status != "stopped" {
		suggestions = append(suggestions, fmt.Sprintf("start the local service on localhost:%s, then rerun `sealtun doctor %s`", sess.LocalPort, sess.TunnelID))
	}
	if payload.Remote == nil {
		suggestions = append(suggestions, fmt.Sprintf("run `sealtun inspect %s --remote` after login to see Kubernetes resource details", sess.TunnelID))
	}
	if sess.CustomDomain != "" {
		suggestions = append(suggestions, fmt.Sprintf("run `sealtun domain doctor %s` to verify DNS, Ingress, and certificate status", sess.TunnelID))
	}
	if len(suggestions) == 0 {
		suggestions = append(suggestions, "no immediate action suggested")
	}
	return suggestions
}
