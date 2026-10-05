package gitclient

import (
	"errors"
	"net/url"
	"strings"
)

var errUnsupportedRemote = errors.New("remote URL must be an https://, http://, ssh://, or user@host:path address")

// ValidateRemoteURL accepts only network remotes. Local paths and file:// URLs
// would let a user clone any repository on the server's disk, including other
// projects, and transports like ext:: run arbitrary commands.
func ValidateRemoteURL(remoteURL string) error {
	value := strings.TrimSpace(remoteURL)
	if value == "" {
		return errors.New("remote URL is required")
	}
	if strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n") {
		return errUnsupportedRemote
	}

	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || strings.HasPrefix(parsed.Host, "-") {
			return errUnsupportedRemote
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https", "http", "ssh":
			return nil
		default:
			return errUnsupportedRemote
		}
	}

	return validateSCPLikeRemote(value)
}

// validateSCPLikeRemote checks the user@host:path form git treats as SSH.
func validateSCPLikeRemote(value string) error {
	at := strings.Index(value, "@")
	colon := strings.Index(value, ":")
	if at <= 0 || colon <= at+1 || colon == len(value)-1 {
		return errUnsupportedRemote
	}
	host := value[at+1 : colon]
	if strings.HasPrefix(host, "-") || strings.Contains(host, "/") {
		return errUnsupportedRemote
	}
	return nil
}
