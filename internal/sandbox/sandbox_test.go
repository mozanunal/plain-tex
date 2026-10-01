package sandbox

import (
	"context"
	"reflect"
	"testing"
)

func TestCommandRoundTripsThroughHelperArgs(t *testing.T) {
	t.Parallel()

	spec := Spec{
		ReadWrite:   []string{"/data/projects/p1"},
		ReadOnly:    []string{"/usr", "/data/cache"},
		CPUSeconds:  120,
		MemoryBytes: 2 << 30,
	}

	cmd, err := Command(context.Background(), "/usr/local/bin/poly-txt", spec, []string{"PATH=/usr/bin"}, "tectonic", "main.tex")
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !IsHelperInvocation(cmd.Args) {
		t.Fatalf("built command is not a helper invocation: %v", cmd.Args)
	}

	gotSpec, name, rest, err := parseHelperArgs(cmd.Args)
	if err != nil {
		t.Fatalf("parseHelperArgs returned error: %v", err)
	}
	if name != "tectonic" {
		t.Fatalf("target name=%q want tectonic", name)
	}
	if !reflect.DeepEqual(rest, []string{"main.tex"}) {
		t.Fatalf("target args=%v want [main.tex]", rest)
	}
	if !reflect.DeepEqual(gotSpec, spec) {
		t.Fatalf("spec round-trip mismatch:\n got %+v\nwant %+v", gotSpec, spec)
	}
}

func TestParseHelperArgsRejectsMalformed(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"poly-txt"},
		{"poly-txt", helperCommand},
		{"poly-txt", helperCommand, "not-base64!!", "--", "tectonic"},
		{"poly-txt", "serve", "x", "--", "tectonic"},
	} {
		if _, _, _, err := parseHelperArgs(args); err == nil {
			t.Errorf("parseHelperArgs(%v) accepted a malformed invocation", args)
		}
	}
}
