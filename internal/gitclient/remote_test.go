package gitclient

import "testing"

func TestValidateRemoteURL(t *testing.T) {
	t.Parallel()

	accepted := []string{
		"https://github.com/org/repo.git",
		"http://gitea.lab.local/org/repo.git",
		"ssh://git@github.com/org/repo.git",
		"git@github.com:org/repo.git",
	}
	for _, remote := range accepted {
		if err := ValidateRemoteURL(remote); err != nil {
			t.Errorf("ValidateRemoteURL(%q) returned error: %v", remote, err)
		}
	}

	rejected := []string{
		"",
		"/data/projects/other-project",
		"../other-project",
		"file:///data/projects/other-project",
		"ext::sh -c touch% /tmp/pwned",
		"--upload-pack=touch /tmp/pwned",
		"ssh://-oProxyCommand=evil/repo",
		"git@-oProxyCommand=evil:repo",
		"git@host:",
		"host:repo",
	}
	for _, remote := range rejected {
		if err := ValidateRemoteURL(remote); err == nil {
			t.Errorf("ValidateRemoteURL(%q) accepted an unsupported remote", remote)
		}
	}
}
