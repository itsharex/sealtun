package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labring/sealtun/pkg/auth"
	"github.com/labring/sealtun/pkg/k8s"
	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
	"github.com/labring/sealtun/pkg/publicauth"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type applyFile struct {
	Version string        `json:"version" yaml:"version"`
	Tunnels []applyTunnel `json:"tunnels" yaml:"tunnels"`
}

type applyTunnel struct {
	Name          string             `json:"name" yaml:"name"`
	LocalPort     int                `json:"localPort" yaml:"localPort"`
	Port          int                `json:"port,omitempty" yaml:"port,omitempty"`
	Protocol      string             `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	Domain        string             `json:"domain,omitempty" yaml:"domain,omitempty"`
	TTL           string             `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	WaitDomain    bool               `json:"waitDomain,omitempty" yaml:"waitDomain,omitempty"`
	ReadyTimeout  string             `json:"readyTimeout,omitempty" yaml:"readyTimeout,omitempty"`
	DomainTimeout string             `json:"domainTimeout,omitempty" yaml:"domainTimeout,omitempty"`
	BasicAuth     *applyBasicAuth    `json:"basicAuth,omitempty" yaml:"basicAuth,omitempty"`
	AccessPolicy  *applyAccessPolicy `json:"accessPolicy,omitempty" yaml:"accessPolicy,omitempty"`
}

type applyBasicAuth struct {
	Credential  string `json:"credential,omitempty" yaml:"credential,omitempty"`
	Username    string `json:"username" yaml:"username"`
	Password    string `json:"password,omitempty" yaml:"password,omitempty"`
	PasswordEnv string `json:"passwordEnv,omitempty" yaml:"passwordEnv,omitempty"`
}

type diffResult struct {
	Name         string   `json:"name"`
	TunnelID     string   `json:"tunnelId"`
	Action       string   `json:"action"`
	Changes      []string `json:"changes,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	DesiredPort  string   `json:"desiredPort,omitempty"`
	CurrentPort  string   `json:"currentPort,omitempty"`
	DesiredHost  string   `json:"desiredHost,omitempty"`
	CurrentHost  string   `json:"currentHost,omitempty"`
	ExpiresAt    string   `json:"expiresAt,omitempty"`
	AccessPolicy bool     `json:"accessPolicy"`
	BasicAuth    bool     `json:"basicAuth"`
}

type applyResult struct {
	Name          string                 `json:"name"`
	TunnelID      string                 `json:"tunnelId"`
	Protocol      string                 `json:"protocol"`
	Host          string                 `json:"host"`
	SealosHost    string                 `json:"sealosHost,omitempty"`
	CustomDomain  string                 `json:"customDomain,omitempty"`
	PublicPort    int32                  `json:"publicPort,omitempty"`
	LocalPort     string                 `json:"localPort"`
	BasicAuth     bool                   `json:"basicAuth"`
	BasicAuthUser string                 `json:"basicAuthUser,omitempty"`
	AccessPolicy  bool                   `json:"accessPolicy"`
	ExpiresAt     string                 `json:"expiresAt,omitempty"`
	TemporaryURLs []string               `json:"temporaryUrls,omitempty"`
	Status        string                 `json:"status"`
	Warnings      []string               `json:"warnings,omitempty"`
	NewTunnel     bool                   `json:"-"`
	Previous      *session.TunnelSession `json:"-"`
}

type normalizedApplyTunnel struct {
	Name          string
	TunnelID      string
	LocalPort     string
	Protocol      string
	CustomDomain  string
	BasicAuth     *session.BasicAuthConfig
	BasicAuthPass string
	AccessPolicy  *session.AccessPolicy
	TTL           string
	ExpiresAt     string
	WaitDomain    bool
	ReadyTimeout  time.Duration
	DomainTimeout time.Duration
}

var applyFilePath string
var applyJSON bool
var applyDryRun bool
var diffFilePath string
var diffJSON bool

var applyNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,53}[a-z0-9])?$`)

const applyFileMaxBytes = 1 << 20

var applyCmd = &cobra.Command{
	Use:          "apply -f sealtun.yaml",
	Short:        "Apply declarative Sealtun tunnel configuration",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(applyFilePath) == "" {
			return fmt.Errorf("missing -f/--file")
		}
		results, err := runApply(cmd.Context(), applyFilePath, applyDryRun)
		if err != nil {
			return err
		}
		if applyJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(results)
		}
		printApplyResults(cmd, results, applyDryRun)
		return nil
	},
}

var diffCmd = &cobra.Command{
	Use:          "diff -f sealtun.yaml",
	Short:        "Show declarative Sealtun tunnel changes without applying them",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(diffFilePath) == "" {
			return fmt.Errorf("missing -f/--file")
		}
		results, err := runDiff(diffFilePath)
		if err != nil {
			return err
		}
		if diffJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(results)
		}
		printDiffResults(cmd, results)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(diffCmd)
	applyCmd.Flags().StringVarP(&applyFilePath, "file", "f", "", "Path to sealtun.yaml")
	applyCmd.Flags().BoolVar(&applyJSON, "json", false, "Output apply results as JSON")
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "Validate and show planned tunnels without changing local or cloud state")
	diffCmd.Flags().StringVarP(&diffFilePath, "file", "f", "", "Path to sealtun.yaml")
	diffCmd.Flags().BoolVar(&diffJSON, "json", false, "Output diff results as JSON")
}

func runApply(ctx context.Context, path string, dryRun bool) ([]applyResult, error) {
	config, err := loadApplyFile(path)
	if err != nil {
		return nil, err
	}
	return runApplyConfig(ctx, config, dryRun)
}

func runApplyContent(ctx context.Context, data []byte, dryRun bool) ([]applyResult, error) {
	config, err := loadApplyData("dashboard yaml", data)
	if err != nil {
		return nil, err
	}
	return runApplyConfig(ctx, config, dryRun)
}

func runApplyConfig(ctx context.Context, config *applyFile, dryRun bool) ([]applyResult, error) {
	if len(config.Tunnels) == 0 {
		return nil, fmt.Errorf("apply file has no tunnels")
	}
	if err := validateApplyTunnelNames(config.Tunnels); err != nil {
		return nil, err
	}
	if dryRun {
		results := make([]applyResult, 0, len(config.Tunnels))
		for _, item := range config.Tunnels {
			normalized, err := normalizeApplyTunnel(item)
			if err != nil {
				return results, err
			}
			results = append(results, applyResult{
				Name:          normalized.Name,
				TunnelID:      normalized.TunnelID,
				Protocol:      normalized.Protocol,
				LocalPort:     normalized.LocalPort,
				BasicAuth:     normalized.BasicAuth != nil && normalized.BasicAuth.Enabled,
				BasicAuthUser: basicAuthUsername(normalized.BasicAuth),
				AccessPolicy:  normalized.AccessPolicy != nil,
				ExpiresAt:     normalized.ExpiresAt,
				Status:        "planned",
			})
		}
		return results, nil
	}

	authData, err := auth.LoadAuthData()
	if err != nil {
		return nil, fmt.Errorf("not logged in. Please run 'sealtun login' first: %w", err)
	}
	root, err := auth.GetSealosDir()
	if err != nil {
		return nil, err
	}
	kubeconfigPath := filepath.Join(root, "kubeconfig")
	kubeconfig, err := auth.ActiveKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
	}
	client, err := k8s.NewClient(kubeconfigPath, authData)
	if err != nil {
		return nil, fmt.Errorf("failed to init k8s client: %w", err)
	}

	results := make([]applyResult, 0, len(config.Tunnels))
	for _, item := range config.Tunnels {
		result, err := applyOneTunnel(ctx, item, authData, client, kubeconfig, dryRun)
		if err != nil {
			rollbackApplyResults(client, results)
			return results, err
		}
		results = append(results, result)
	}
	if !dryRun {
		if err := ensureDaemonRunning(); err != nil {
			rollbackApplyResults(client, results)
			return results, fmt.Errorf("failed to start local daemon: %w", err)
		}
		for _, result := range results {
			if err := waitForDaemonSession(result.TunnelID, daemonConnectTimeout); err != nil {
				rollbackApplyResults(client, results)
				return results, err
			}
		}
	}
	return results, nil
}

func loadApplyFile(path string) (*applyFile, error) {
	file, err := os.Open(path) // #nosec G304 -- apply file path is provided by the user.
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, applyFileMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > applyFileMaxBytes {
		return nil, fmt.Errorf("apply file %s is too large; limit is %d bytes", path, applyFileMaxBytes)
	}
	return loadApplyData(path, data)
}

func loadApplyData(label string, data []byte) (*applyFile, error) {
	if len(data) > applyFileMaxBytes {
		return nil, fmt.Errorf("apply file %s is too large; limit is %d bytes", label, applyFileMaxBytes)
	}
	var config applyFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", label, err)
		}
		return nil, fmt.Errorf("parse %s: multiple YAML documents are not supported", label)
	}
	if config.Version == "" {
		config.Version = "v1"
	}
	if config.Version != "v1" {
		return nil, fmt.Errorf("unsupported apply file version %q", config.Version)
	}
	return &config, nil
}

func validateApplyTunnelNames(items []applyTunnel) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		tunnelID, err := applyTunnelID(item.Name)
		if err != nil {
			return err
		}
		if _, ok := seen[tunnelID]; ok {
			return fmt.Errorf("duplicate tunnel name %q in apply file", item.Name)
		}
		seen[tunnelID] = struct{}{}
	}
	return nil
}

func applyOneTunnel(ctx context.Context, item applyTunnel, authData *auth.AuthData, client *k8s.Client, kubeconfig string, dryRun bool) (result applyResult, err error) {
	normalized, err := normalizeApplyTunnel(item)
	if err != nil {
		return applyResult{}, err
	}

	result = applyResult{
		Name:          normalized.Name,
		TunnelID:      normalized.TunnelID,
		Protocol:      normalized.Protocol,
		LocalPort:     normalized.LocalPort,
		BasicAuth:     normalized.BasicAuth != nil && normalized.BasicAuth.Enabled,
		BasicAuthUser: basicAuthUsername(normalized.BasicAuth),
		AccessPolicy:  normalized.AccessPolicy != nil,
		ExpiresAt:     normalized.ExpiresAt,
		Status:        "planned",
	}
	secret := uuid.New().String()
	createdAt := ""
	alreadyExisted := false
	var existingSession *session.TunnelSession
	if !dryRun {
		existing, err := session.Get(normalized.TunnelID)
		if err == nil {
			alreadyExisted = true
			existingSession = existing
			currentNamespace := ""
			if client != nil {
				currentNamespace = client.Namespace()
			}
			if err := validateExistingApplySessionScope(*existing, authData, currentNamespace); err != nil {
				return result, err
			}
			if existing.Secret != "" {
				secret = existing.Secret
			}
			reuseExistingExpiration(&normalized, existing)
			reuseExistingBasicAuthHash(&normalized, existing.BasicAuth)
			createdAt = existing.CreatedAt
		} else if !os.IsNotExist(err) {
			return result, fmt.Errorf("tunnel %s: load existing session: %w", normalized.TunnelID, err)
		}
	}

	result.NewTunnel = !alreadyExisted
	result.Previous = existingSession
	if dryRun {
		return result, nil
	}

	desiredCustomDomain := normalized.CustomDomain
	customDomainVerified := false
	sealosHost := ""
	if existingSession != nil {
		sealosHost = sessionSealosHostForDomain(*existingSession, "")
	}
	if sealosHost == "" && client != nil {
		sealosHost = client.SealosHost(normalized.TunnelID)
	}
	if desiredCustomDomain != "" {
		if verifyErr := requireDomainCNAME(ctx, desiredCustomDomain, sealosHost); verifyErr != nil {
			if alreadyExisted {
				return result, fmt.Errorf("tunnel %s: custom domain DNS must be verified before updating an existing tunnel: %w", normalized.TunnelID, verifyErr)
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("custom domain not attached: %v", verifyErr))
			result.Warnings = append(result.Warnings, fmt.Sprintf("configure CNAME %s -> %s, then run `sealtun domain set %s %s`", desiredCustomDomain, sealosHost, normalized.TunnelID, desiredCustomDomain))
			desiredCustomDomain = ""
		} else {
			customDomainVerified = true
		}
	}
	if client == nil {
		return result, fmt.Errorf("tunnel %s: kubernetes client is unavailable", normalized.TunnelID)
	}

	remoteChanged := false
	defer func() {
		if err == nil || !remoteChanged {
			return
		}
		if result.NewTunnel {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), tunnelCleanupTimeout)
			defer cancel()
			_ = client.CleanupTunnel(cleanupCtx, normalized.TunnelID)
			_ = session.Delete(normalized.TunnelID)
			return
		}
		if existingSession != nil {
			if rollbackErr := rollbackExistingApplyTunnel(client, *existingSession); rollbackErr != nil {
				err = fmt.Errorf("%w; rollback of existing tunnel failed: %v", err, rollbackErr)
			}
		}
	}()

	options := k8s.TunnelOptions{}
	options.BasicAuth = basicAuthToK8s(normalized.BasicAuth)
	options.AccessPolicy = accessPolicyToK8s(normalized.AccessPolicy)
	if customDomainVerified {
		options.CustomDomain = desiredCustomDomain
		options.SealosHost = sealosHost
	}
	hosts, err := client.EnsureTunnelWithOptions(ctx, normalized.TunnelID, secret, normalized.Protocol, normalized.LocalPort, options)
	if err != nil {
		if alreadyExisted && existingSession != nil {
			if rollbackErr := rollbackExistingApplyTunnel(client, *existingSession); rollbackErr != nil {
				return result, fmt.Errorf("tunnel %s: provision on Sealos: %w; rollback of existing tunnel failed: %v", normalized.TunnelID, err, rollbackErr)
			}
		}
		return result, fmt.Errorf("tunnel %s: provision on Sealos: %w", normalized.TunnelID, err)
	}
	remoteChanged = true

	if alreadyExisted && existingSession != nil && existingSession.CustomDomain != "" && desiredCustomDomain == "" {
		clearedHosts, clearErr := client.WithNamespace(client.Namespace()).ClearCustomDomain(ctx, normalized.TunnelID, hosts.SealosHost)
		if clearErr != nil {
			return result, fmt.Errorf("tunnel %s: clear custom domain: %w", normalized.TunnelID, clearErr)
		}
		hosts = clearedHosts
	}

	waitCtx, cancel := context.WithTimeout(ctx, normalized.ReadyTimeout)
	err = client.WaitForReady(waitCtx, normalized.TunnelID)
	cancel()
	if err != nil {
		return result, fmt.Errorf("tunnel %s: wait for ready: %w", normalized.TunnelID, err)
	}

	record := buildApplySessionRecord(normalized, authData, client.Namespace(), kubeconfig, secret, hosts, createdAt)
	if err := session.Save(record); err != nil {
		return result, fmt.Errorf("tunnel %s: save session: %w", normalized.TunnelID, err)
	}

	if hosts.CustomDomain != "" && normalized.WaitDomain {
		verify, waitErr := waitForDomainReady(ctx, session.TunnelSession{
			TunnelID:     normalized.TunnelID,
			Host:         hosts.PublicHost,
			SealosHost:   hosts.SealosHost,
			CustomDomain: hosts.CustomDomain,
			Namespace:    client.Namespace(),
			Kubeconfig:   kubeconfig,
			Region:       authData.Region,
		}, normalized.DomainTimeout)
		if verify != nil && !verify.Ready {
			result.Warnings = append(result.Warnings, "custom domain is not fully ready yet")
		}
		if waitErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("custom domain readiness wait failed: %v", waitErr))
		}
	}

	result.Host = hosts.PublicHost
	result.SealosHost = hosts.SealosHost
	result.CustomDomain = hosts.CustomDomain
	result.PublicPort = hosts.PublicPort
	result.BasicAuth = normalized.BasicAuth != nil && normalized.BasicAuth.Enabled
	result.BasicAuthUser = basicAuthUsername(normalized.BasicAuth)
	result.AccessPolicy = normalized.AccessPolicy != nil
	result.ExpiresAt = normalized.ExpiresAt
	result.TemporaryURLs = applyTemporaryAccessURLs(hosts.PublicHost, item.AccessPolicy)
	result.Status = "applied"
	remoteChanged = false
	return result, nil
}

func buildApplySessionRecord(normalized normalizedApplyTunnel, authData *auth.AuthData, namespace, kubeconfig, secret string, hosts k8s.TunnelHosts, createdAt string) session.TunnelSession {
	region := ""
	if authData != nil {
		region = authData.Region
	}
	return session.TunnelSession{
		TunnelID:        normalized.TunnelID,
		Region:          region,
		Namespace:       namespace,
		Kubeconfig:      kubeconfig,
		Protocol:        normalized.Protocol,
		Host:            hosts.PublicHost,
		SealosHost:      hosts.SealosHost,
		CustomDomain:    hosts.CustomDomain,
		PublicPort:      hosts.PublicPort,
		LocalPort:       normalized.LocalPort,
		Secret:          secret,
		BasicAuth:       normalized.BasicAuth,
		AccessPolicy:    normalized.AccessPolicy,
		TTL:             normalized.TTL,
		ExpiresAt:       normalized.ExpiresAt,
		Mode:            "daemon",
		PID:             0,
		ConnectionState: session.ConnectionStatePending,
		CreatedAt:       createdAt,
		Resources:       []string{fmt.Sprintf("sealtun-%s", normalized.TunnelID)},
	}
}

func validateExistingApplySessionScope(existing session.TunnelSession, authData *auth.AuthData, currentNamespace string) error {
	currentRegion := ""
	if authData != nil {
		currentRegion = authData.Region
	}
	if existing.Region == "" || currentRegion == "" {
		return fmt.Errorf("tunnel %s already exists but region metadata is incomplete; run `sealtun inspect %s` and clean it up before apply", existing.TunnelID, existing.TunnelID)
	}
	if existing.Region != currentRegion {
		return fmt.Errorf("tunnel %s already belongs to region %s; current region is %s", existing.TunnelID, existing.Region, currentRegion)
	}
	if currentNamespace != "" {
		if existing.Namespace == "" {
			return fmt.Errorf("tunnel %s already exists but namespace metadata is incomplete; clean it up before apply", existing.TunnelID)
		}
		if existing.Namespace != currentNamespace {
			return fmt.Errorf("tunnel %s already belongs to namespace %s; current namespace is %s", existing.TunnelID, existing.Namespace, currentNamespace)
		}
	}
	if strings.TrimSpace(existing.Secret) == "" {
		return fmt.Errorf("tunnel %s already exists but its local secret is unavailable; stop or cleanup the old session before apply", existing.TunnelID)
	}
	return nil
}

func restoreExistingApplyTunnel(client *k8s.Client, previous session.TunnelSession) error {
	if client == nil {
		return nil
	}
	if strings.TrimSpace(previous.Secret) == "" || strings.TrimSpace(previous.LocalPort) == "" {
		return fmt.Errorf("previous session %s is missing secret or local port", previous.TunnelID)
	}
	protocol := previous.Protocol
	if protocol == "" {
		protocol = "https"
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), tunnelCleanupTimeout)
	defer cancel()
	_, err := client.WithNamespace(previous.Namespace).EnsureTunnelWithOptions(cleanupCtx, previous.TunnelID, previous.Secret, protocol, previous.LocalPort, k8s.TunnelOptions{
		CustomDomain: previous.CustomDomain,
		SealosHost:   previous.SealosHost,
		BasicAuth:    basicAuthToK8s(previous.BasicAuth),
		AccessPolicy: accessPolicyToK8s(previous.AccessPolicy),
	})
	return err
}

func rollbackExistingApplyTunnel(client *k8s.Client, previous session.TunnelSession) error {
	var firstErr error
	if err := restoreExistingApplyTunnel(client, previous); err != nil {
		firstErr = err
	}
	if err := session.Save(previous); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func rollbackApplyResults(client *k8s.Client, results []applyResult) {
	for i := len(results) - 1; i >= 0; i-- {
		result := results[i]
		if result.TunnelID == "" {
			continue
		}
		if result.NewTunnel {
			if client != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), tunnelCleanupTimeout)
				_ = client.CleanupTunnel(cleanupCtx, result.TunnelID)
				cancel()
			}
			_ = session.Delete(result.TunnelID)
			continue
		}
		if result.Previous != nil {
			_ = rollbackExistingApplyTunnel(client, *result.Previous)
		}
	}
}

func normalizeApplyTunnel(item applyTunnel) (normalizedApplyTunnel, error) {
	tunnelID, err := applyTunnelID(item.Name)
	if err != nil {
		return normalizedApplyTunnel{}, err
	}
	port := item.LocalPort
	if port == 0 {
		port = item.Port
	}
	localPort := strconv.Itoa(port)
	if err := validateLocalPort(localPort); err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: %w", tunnelID, err)
	}
	protocol := item.Protocol
	if protocol == "" {
		protocol = "https"
	}
	if err := validateProtocol(protocol); err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: %w", tunnelID, err)
	}
	protocol = tunnelprotocol.Normalize(protocol)
	customDomain, err := validateCustomDomain(item.Domain)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: %w", tunnelID, err)
	}
	effectiveReadyTimeout, err := parseApplyDuration(item.ReadyTimeout, readyTimeout)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s readyTimeout: %w", tunnelID, err)
	}
	effectiveDomainTimeout, err := parseApplyDuration(item.DomainTimeout, domainWaitTimeout)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s domainTimeout: %w", tunnelID, err)
	}
	basicAuth, basicAuthPass, err := resolveApplyBasicAuth(item.BasicAuth)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: %w", tunnelID, err)
	}
	now := nowUTC()
	accessPolicy, err := resolveApplyAccessPolicy(item.AccessPolicy, now, getenv)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s accessPolicy: %w", tunnelID, err)
	}
	if !tunnelprotocol.IsHTTP(protocol) {
		if customDomain != "" || item.WaitDomain {
			return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: domain and waitDomain are only supported for https tunnels", tunnelID)
		}
		if basicAuth != nil {
			return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: basicAuth is only supported for https tunnels", tunnelID)
		}
		if accessPolicy != nil {
			return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s: accessPolicy is only supported for https tunnels", tunnelID)
		}
	}
	ttl := strings.TrimSpace(item.TTL)
	expiresAt, err := resolveApplyTunnelExpiresAt(ttl, now)
	if err != nil {
		return normalizedApplyTunnel{}, fmt.Errorf("tunnel %s ttl: %w", tunnelID, err)
	}
	return normalizedApplyTunnel{
		Name:          item.Name,
		TunnelID:      tunnelID,
		LocalPort:     localPort,
		Protocol:      protocol,
		CustomDomain:  customDomain,
		BasicAuth:     basicAuth,
		BasicAuthPass: basicAuthPass,
		AccessPolicy:  accessPolicy,
		TTL:           ttl,
		ExpiresAt:     expiresAt,
		WaitDomain:    item.WaitDomain,
		ReadyTimeout:  effectiveReadyTimeout,
		DomainTimeout: effectiveDomainTimeout,
	}, nil
}

func resolveApplyTunnelExpiresAt(ttl string, now time.Time) (string, error) {
	if strings.TrimSpace(ttl) == "" {
		return "", nil
	}
	duration, err := time.ParseDuration(ttl)
	if err != nil {
		return "", err
	}
	if duration <= 0 {
		return "", fmt.Errorf("must be greater than 0")
	}
	return now.Add(duration).UTC().Format(time.RFC3339), nil
}

func resolveApplyBasicAuth(config *applyBasicAuth) (*session.BasicAuthConfig, string, error) {
	if config == nil {
		return nil, "", nil
	}
	input := basicAuthInput{
		Credential:  config.Credential,
		Username:    config.Username,
		Password:    config.Password,
		PasswordEnv: config.PasswordEnv,
	}
	username, password, ok, err := resolveBasicAuthCredentials(input, os.Getenv)
	if err != nil || !ok {
		return nil, "", err
	}
	basicAuth, err := newSessionBasicAuth(username, password)
	if err != nil {
		return nil, "", err
	}
	return basicAuth, password, nil
}

func reuseExistingBasicAuthHash(normalized *normalizedApplyTunnel, existing *session.BasicAuthConfig) {
	if normalized == nil || normalized.BasicAuth == nil || existing == nil || !existing.Enabled {
		return
	}
	existingHash := basicAuthPasswordHash(existing)
	if existingHash == "" || normalized.BasicAuthPass == "" || existing.Username != normalized.BasicAuth.Username {
		return
	}
	if !publicauth.Check(publicauth.BasicAuth{Username: existing.Username, PasswordHash: existingHash}, normalized.BasicAuth.Username, normalized.BasicAuthPass) {
		return
	}
	if existing.PasswordHash == "" {
		normalized.BasicAuth.PasswordSHA256 = ""
		return
	}
	normalized.BasicAuth.PasswordHash = existingHash
	normalized.BasicAuth.PasswordSHA256 = ""
}

func reuseExistingExpiration(normalized *normalizedApplyTunnel, existing *session.TunnelSession) {
	if normalized == nil || existing == nil {
		return
	}
	if normalized.TTL != "" && existing.TTL == normalized.TTL && !sessionExpired(*existing, nowUTC()) && existing.ExpiresAt != "" {
		normalized.ExpiresAt = existing.ExpiresAt
	}
	reuseExistingTemporaryTokenExpirations(normalized.AccessPolicy, existing.AccessPolicy)
}

func reuseExistingTemporaryTokenExpirations(desired, existing *session.AccessPolicy) {
	if desired == nil || existing == nil || len(desired.TemporaryTokens) == 0 || len(existing.TemporaryTokens) == 0 {
		return
	}
	existingByKey := map[string]session.TemporaryToken{}
	for _, token := range existing.TemporaryTokens {
		if token.TTL == "" || token.ExpiresAt == "" {
			continue
		}
		if expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt); err != nil || !nowUTC().Before(expiresAt) {
			continue
		}
		existingByKey[temporaryTokenIdentity(token)] = token
	}
	for i := range desired.TemporaryTokens {
		token := &desired.TemporaryTokens[i]
		if token.TTL == "" {
			continue
		}
		if existingToken, ok := existingByKey[temporaryTokenIdentity(*token)]; ok {
			token.ExpiresAt = existingToken.ExpiresAt
		}
	}
}

func temporaryTokenIdentity(token session.TemporaryToken) string {
	return strings.Join([]string{token.Name, token.TokenHash, token.TTL}, "\x00")
}

func basicAuthUsername(config *session.BasicAuthConfig) string {
	if config == nil || !config.Enabled {
		return ""
	}
	return config.Username
}

func applyTunnelID(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("tunnel name is required")
	}
	if name != strings.ToLower(name) || !applyNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid tunnel name %q: use lowercase DNS-compatible names, e.g. web or api-dev", name)
	}
	return name, nil
}

func parseApplyDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("must be greater than 0")
	}
	return duration, nil
}

func printApplyResults(cmd *cobra.Command, results []applyResult, dryRun bool) {
	out := cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintln(out, "Sealtun Apply Plan")
	} else {
		fmt.Fprintln(out, "Sealtun Apply Results")
	}
	for _, result := range results {
		endpoint := endpointDisplay(result.Protocol, result.Host, result.SealosHost, result.PublicPort)
		fmt.Fprintf(out, "  - %s (%s): %s localhost:%s", result.Name, result.TunnelID, result.Status, result.LocalPort)
		if result.Protocol == tunnelprotocol.SSH && endpoint.Command != "" {
			fmt.Fprintf(out, " -> %s", endpoint.Command)
		} else if result.Protocol == tunnelprotocol.TCP && endpoint.Port != 0 {
			fmt.Fprintf(out, " -> %s", endpointLabel(result.Protocol, result.Host, result.SealosHost, result.PublicPort))
		} else if endpoint.URL != "" {
			fmt.Fprintf(out, " -> %s", endpoint.URL)
		}
		fmt.Fprintln(out)
		if result.Protocol != "" {
			fmt.Fprintf(out, "    Protocol: %s\n", result.Protocol)
		}
		if result.SealosHost != "" {
			fmt.Fprintf(out, "    Sealos host: %s\n", result.SealosHost)
		}
		if result.CustomDomain != "" {
			fmt.Fprintf(out, "    Custom domain: %s\n", result.CustomDomain)
		}
		if result.Protocol == tunnelprotocol.SSH {
			if endpoint.Host != "" {
				fmt.Fprintf(out, "    Public SSH host: %s\n", endpoint.Host)
			}
			if endpoint.Port != 0 {
				fmt.Fprintf(out, "    Public SSH port: %d\n", endpoint.Port)
			}
			if endpoint.Command != "" {
				fmt.Fprintf(out, "    SSH command: %s\n", endpoint.Command)
			}
		} else if result.Protocol == tunnelprotocol.TCP {
			if endpoint.Host != "" {
				fmt.Fprintf(out, "    Public TCP host: %s\n", endpoint.Host)
			}
			if endpoint.Port != 0 {
				fmt.Fprintf(out, "    Public TCP port: %d\n", endpoint.Port)
				fmt.Fprintf(out, "    Public TCP endpoint: %s\n", endpointLabel(result.Protocol, result.Host, result.SealosHost, result.PublicPort))
			}
		} else if endpoint.URL != "" {
			fmt.Fprintf(out, "    Public URL: %s\n", endpoint.URL)
		}
		if result.BasicAuth {
			fmt.Fprintf(out, "    Basic Auth: enabled")
			if result.BasicAuthUser != "" {
				fmt.Fprintf(out, " (user: %s)", result.BasicAuthUser)
			}
			fmt.Fprintln(out)
		}
		if result.AccessPolicy {
			fmt.Fprintln(out, "    Access policy: enabled")
		}
		if result.ExpiresAt != "" {
			fmt.Fprintf(out, "    Expires at: %s\n", result.ExpiresAt)
		}
		for _, link := range result.TemporaryURLs {
			fmt.Fprintf(out, "    Temporary access URL: %s\n", link)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(out, "    Warning: %s\n", warning)
		}
	}
}

func applyTemporaryAccessURLs(host string, config *applyAccessPolicy) []string {
	if config == nil || host == "" {
		return nil
	}
	links := make([]string, 0, len(config.TemporaryLinks))
	for _, item := range config.TemporaryLinks {
		if item.Token == "" {
			continue
		}
		if link := temporaryAccessURL(host, item.Token); link != "" {
			links = append(links, link)
		}
	}
	return links
}

func runDiff(path string) ([]diffResult, error) {
	config, err := loadApplyFile(path)
	if err != nil {
		return nil, err
	}
	return runDiffConfig(config)
}

func runDiffContent(data []byte) ([]diffResult, error) {
	config, err := loadApplyData("dashboard yaml", data)
	if err != nil {
		return nil, err
	}
	return runDiffConfig(config)
}

func runDiffConfig(config *applyFile) ([]diffResult, error) {
	return runDiffConfigWithSessionLookup(config, session.Get)
}

func runDiffConfigWithSessionLookup(config *applyFile, lookup func(string) (*session.TunnelSession, error)) ([]diffResult, error) {
	if len(config.Tunnels) == 0 {
		return nil, fmt.Errorf("apply file has no tunnels")
	}
	if err := validateApplyTunnelNames(config.Tunnels); err != nil {
		return nil, err
	}
	results := make([]diffResult, 0, len(config.Tunnels))
	for _, item := range config.Tunnels {
		normalized, err := normalizeApplyTunnel(item)
		if err != nil {
			return results, err
		}
		result := diffResult{
			Name:         normalized.Name,
			TunnelID:     normalized.TunnelID,
			DesiredPort:  normalized.LocalPort,
			DesiredHost:  normalized.CustomDomain,
			ExpiresAt:    normalized.ExpiresAt,
			AccessPolicy: normalized.AccessPolicy != nil,
			BasicAuth:    normalized.BasicAuth != nil && normalized.BasicAuth.Enabled,
		}
		existing, err := lookup(normalized.TunnelID)
		if err == nil {
			reuseExistingExpiration(&normalized, existing)
			result.ExpiresAt = normalized.ExpiresAt
			result.AccessPolicy = normalized.AccessPolicy != nil
			result.CurrentPort = existing.LocalPort
			result.CurrentHost = existing.CustomDomain
			result.Action = "no-op"
			if existing.LocalPort != normalized.LocalPort {
				result.Changes = append(result.Changes, fmt.Sprintf("localPort: %s -> %s", valueOr(existing.LocalPort, "-"), normalized.LocalPort))
			}
			if valueOr(existing.Protocol, "https") != normalized.Protocol {
				result.Changes = append(result.Changes, fmt.Sprintf("protocol: %s -> %s", valueOr(existing.Protocol, "-"), normalized.Protocol))
			}
			if existing.CustomDomain != normalized.CustomDomain {
				result.Changes = append(result.Changes, fmt.Sprintf("domain: %s -> %s", valueOr(existing.CustomDomain, "-"), valueOr(normalized.CustomDomain, "-")))
			}
			if basicAuthChanged(existing.BasicAuth, normalized.BasicAuth, normalized.BasicAuthPass) {
				result.Changes = append(result.Changes, "basicAuth")
			}
			if accessPolicyChanged(existing.AccessPolicy, normalized.AccessPolicy) {
				result.Changes = append(result.Changes, "accessPolicy")
			}
			if existing.ExpiresAt != normalized.ExpiresAt {
				result.Changes = append(result.Changes, fmt.Sprintf("ttl/expiresAt: %s -> %s", valueOr(existing.ExpiresAt, "-"), valueOr(normalized.ExpiresAt, "-")))
			}
			if len(result.Changes) > 0 {
				result.Action = "update"
			}
			results = append(results, result)
			continue
		}
		if diffTreatsMissingSessionAsCreate(err) {
			result.Action = "create"
			result.Changes = append(result.Changes, "create tunnel")
			results = append(results, result)
			continue
		}
		return results, fmt.Errorf("tunnel %s: load existing session: %w", normalized.TunnelID, err)
	}
	return results, nil
}

func diffTreatsMissingSessionAsCreate(err error) bool {
	if os.IsNotExist(err) {
		return true
	}
	return strings.Contains(err.Error(), "config directory") && strings.Contains(err.Error(), "no such file or directory")
}

func accessPolicyChanged(current, desired *session.AccessPolicy) bool {
	currentJSON, _ := json.Marshal(accessPolicyToRuntime(current))
	desiredJSON, _ := json.Marshal(accessPolicyToRuntime(desired))
	return string(currentJSON) != string(desiredJSON)
}

func basicAuthChanged(current, desired *session.BasicAuthConfig, desiredPassword string) bool {
	currentEnabled := current != nil && current.Enabled
	desiredEnabled := desired != nil && desired.Enabled
	if currentEnabled != desiredEnabled {
		return true
	}
	if !currentEnabled && !desiredEnabled {
		return false
	}
	if basicAuthUsername(current) != basicAuthUsername(desired) {
		return true
	}
	if desiredPassword == "" {
		return basicAuthPasswordHash(current) != basicAuthPasswordHash(desired)
	}
	return !publicauth.Check(publicauth.BasicAuth{
		Username:     current.Username,
		PasswordHash: basicAuthPasswordHash(current),
	}, desired.Username, desiredPassword)
}

func printDiffResults(cmd *cobra.Command, results []diffResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Sealtun Diff")
	for _, result := range results {
		fmt.Fprintf(out, "  - %s (%s): %s", result.Name, result.TunnelID, result.Action)
		if result.DesiredPort != "" {
			fmt.Fprintf(out, " localhost:%s", result.DesiredPort)
		}
		fmt.Fprintln(out)
		for _, change := range result.Changes {
			fmt.Fprintf(out, "    ~ %s\n", change)
		}
		if result.AccessPolicy {
			fmt.Fprintln(out, "    Access policy: enabled")
		}
		if result.BasicAuth {
			fmt.Fprintln(out, "    Basic Auth: enabled")
		}
		if result.ExpiresAt != "" {
			fmt.Fprintf(out, "    Expires at: %s\n", result.ExpiresAt)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(out, "    Warning: %s\n", warning)
		}
	}
}
