package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDemoWalkthroughActivatesKillSwitch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runDemo(
		[]string{"-target", "http://127.0.0.1:0", "-duration", "100ms"},
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("runDemo: %v\nstderr: %s", err, stderr.String())
	}
	for _, want := range []string{"demo dashboard: http://127.0.0.1:", "retry-loop", "KILL SWITCH"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestDemoRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "non HTTP URL", args: []string{"-target", "file:///tmp/demo"}},
		{name: "URL path", args: []string{"-target", "http://localhost:4000/demo"}},
		{name: "negative duration", args: []string{"-duration", "-1s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := runDemo(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
				t.Fatalf("runDemo(%q) succeeded", tt.args)
			}
		})
	}
}
