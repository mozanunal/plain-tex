package procenv

import "testing"

func TestMinimalDropsSecrets(t *testing.T) {
	t.Setenv("JWT_SECRET", "super-secret")
	t.Setenv("PATH", "/usr/bin")

	env := Minimal("TMPDIR=/tmp/compile")

	if Lookup(env, "JWT_SECRET") != "" {
		t.Fatalf("JWT_SECRET leaked into child environment: %v", env)
	}
	if Lookup(env, "PATH") != "/usr/bin" {
		t.Fatalf("PATH was not passed through: %v", env)
	}
	if Lookup(env, "TMPDIR") != "/tmp/compile" {
		t.Fatalf("extra variable was not applied: %v", env)
	}
}
