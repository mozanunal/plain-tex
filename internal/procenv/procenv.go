// Package procenv builds the environment for programs poly-txt runs on behalf of
// users. Child processes must never inherit the server's own environment, which
// holds JWT_SECRET: a compiler or git reading /proc/self/environ would leak it.
package procenv

import (
	"os"
	"strings"
)

var passthroughKeys = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR",
	"XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME",
	"TECTONIC_CACHE_DIR",
	"TYPST_FONT_PATHS", "TYPST_PACKAGE_CACHE_PATH", "TYPST_PACKAGE_PATH",
	"FONTCONFIG_PATH", "FONTCONFIG_FILE",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SOURCE_DATE_EPOCH",
}

// Minimal returns the allowlisted variables from the current environment,
// followed by extra, which may override them.
func Minimal(extra ...string) []string {
	env := make([]string, 0, len(passthroughKeys)+len(extra))
	for _, key := range passthroughKeys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, extra...)
}

// Lookup returns the value of key in an environment list built by Minimal,
// honoring the last assignment the way exec does.
func Lookup(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if name, rest, ok := strings.Cut(entry, "="); ok && name == key {
			value = rest
		}
	}
	return value
}
