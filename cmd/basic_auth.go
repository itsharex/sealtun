package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/publicauth"
	"github.com/labring/sealtun/pkg/session"
)

type basicAuthInput struct {
	Credential  string
	Username    string
	Password    string
	PasswordEnv string
}

func resolveBasicAuth(input basicAuthInput, lookupEnv func(string) string) (*session.BasicAuthConfig, error) {
	username, password, ok, err := resolveBasicAuthCredentials(input, lookupEnv)
	if err != nil || !ok {
		return nil, err
	}
	return newSessionBasicAuth(username, password)
}

func resolveBasicAuthCredentials(input basicAuthInput, lookupEnv func(string) string) (string, string, bool, error) {
	if input.Credential != "" {
		if input.Username != "" || input.Password != "" || input.PasswordEnv != "" {
			return "", "", false, fmt.Errorf("basic auth credential cannot be combined with username, password, or passwordEnv")
		}
		username, password, ok := strings.Cut(input.Credential, ":")
		if !ok {
			return "", "", false, fmt.Errorf("basic auth credential must use username:password")
		}
		return username, password, true, nil
	}

	if input.Username == "" && input.Password == "" && input.PasswordEnv == "" {
		return "", "", false, nil
	}
	if input.Username == "" {
		return "", "", false, fmt.Errorf("basic auth username is required when configuring Basic Auth")
	}
	if input.Password != "" && input.PasswordEnv != "" {
		return "", "", false, fmt.Errorf("basic auth password and passwordEnv cannot be used together")
	}
	password := input.Password
	if input.PasswordEnv != "" {
		password = lookupEnv(input.PasswordEnv)
		if password == "" {
			return "", "", false, fmt.Errorf("basic auth password environment variable %s is empty or unset", input.PasswordEnv)
		}
	}
	return input.Username, password, true, nil
}

// warnPlaintextPasswordFlag prints a one-time warning to stderr when a Basic
// Auth password is provided directly on the command line (via --basic-auth or
// --basic-auth-password) instead of the safer --basic-auth-password-env form.
// Command-line values are visible in the process table and shell history.
func warnPlaintextPasswordFlag(credential, password string) {
	if credential != "" || password != "" {
		fmt.Fprintln(os.Stderr, "Warning: a Basic Auth password was passed on the command line; it may be visible in the process list and shell history. Prefer --basic-auth-password-env.")
	}
}

func newSessionBasicAuth(username, password string) (*session.BasicAuthConfig, error) {
	config, err := publicauth.NewBasicAuth(username, password)
	if err != nil {
		return nil, err
	}
	return &session.BasicAuthConfig{
		Enabled:      true,
		Username:     config.Username,
		PasswordHash: config.PasswordHash,
	}, nil
}

func basicAuthToK8s(config *session.BasicAuthConfig) *k8s.BasicAuthOptions {
	if config == nil || !config.Enabled {
		return nil
	}
	return &k8s.BasicAuthOptions{
		Username:     config.Username,
		PasswordHash: basicAuthPasswordHash(config),
	}
}

func basicAuthPasswordHash(config *session.BasicAuthConfig) string {
	if config == nil {
		return ""
	}
	if config.PasswordHash != "" {
		return config.PasswordHash
	}
	return config.PasswordSHA256
}
