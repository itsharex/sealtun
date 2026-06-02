package cmd

import (
	"fmt"
	"strconv"
	"strings"

	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
)

func exposePreviewCommand(localPort int, protocol, domain string, waitDomain bool) string {
	args := []string{"sealtun", "expose", strconv.Itoa(localPort)}
	protocol = tunnelprotocol.Normalize(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = tunnelprotocol.HTTPS
	}
	if protocol != tunnelprotocol.HTTPS {
		args = append(args, "--protocol", protocol)
	}
	if strings.TrimSpace(domain) != "" {
		args = append(args, "--domain", strings.TrimSpace(domain))
		if waitDomain {
			args = append(args, "--wait-domain")
		}
	}
	for i := range args {
		args[i] = shellQuoteArg(args[i])
	}
	return strings.Join(args, " ")
}

func shellQuoteArg(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return (r < 'A' || r > 'Z') &&
			(r < 'a' || r > 'z') &&
			(r < '0' || r > '9') &&
			!strings.ContainsRune("@%_+=:,./-", r)
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func commandForTunnelAction(action, tunnelID string) string {
	switch action {
	case "start", "stop", "cleanup":
		return fmt.Sprintf("sealtun %s %s", action, shellQuoteArg(tunnelID))
	default:
		return ""
	}
}
