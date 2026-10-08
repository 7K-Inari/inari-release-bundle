//go:build e2e

// Unit tests for the pure helpers of the provisioner/seed stages. They run
// under the e2e tag (the suite package is e2e-only) but need no cluster.
package suite

import (
	"errors"
	"strings"
	"testing"
)

func TestSplitImage(t *testing.T) {
	for _, tc := range []struct {
		image    string
		wantRepo string
		wantTag  string
	}{
		{"inari/server:e2e", "inari/server", "e2e"},
		{"ghcr.io/7k-inari/inari-server:v1.2.3", "ghcr.io/7k-inari/inari-server", "v1.2.3"},
		// Registry port: the colon before the last slash is NOT a tag.
		{"localhost:5000/inari/server:e2e", "localhost:5000/inari/server", "e2e"},
		// Tag-less images default to latest (docker semantics); a bare
		// LastIndex(":") used to panic or mis-split here.
		{"inari/server", "inari/server", "latest"},
		{"localhost:5000/inari/server", "localhost:5000/inari/server", "latest"},
	} {
		t.Run(tc.image, func(t *testing.T) {
			repo, tag := splitImage(tc.image)
			if repo != tc.wantRepo || tag != tc.wantTag {
				t.Fatalf("splitImage(%q) = (%q, %q), want (%q, %q)",
					tc.image, repo, tag, tc.wantRepo, tc.wantTag)
			}
		})
	}
}

func TestKindConfigYAML(t *testing.T) {
	t.Run("with node image", func(t *testing.T) {
		cfg := kindConfigYAML("/tmp/git", "kindest/node:v1.32.0@sha256:abc")
		for _, want := range []string{
			"kind: Cluster",
			"image: kindest/node:v1.32.0@sha256:abc",
			"hostPath: /tmp/git",
			"containerPath: /git",
		} {
			if !strings.Contains(cfg, want) {
				t.Fatalf("kindConfigYAML missing %q:\n%s", want, cfg)
			}
		}
	})
	t.Run("without node image", func(t *testing.T) {
		cfg := kindConfigYAML("/tmp/git", "")
		if strings.Contains(cfg, "image:") {
			t.Fatalf("empty node image must not render an image line:\n%s", cfg)
		}
	})
}

func TestParseOutboxStreamInfo(t *testing.T) {
	for _, tc := range []struct {
		name         string
		out          string
		wantReplicas int
		wantFormed   bool
		wantDetail   string
	}{
		{
			name:         "formed R=1",
			out:          `{"config":{"num_replicas":1,"subjects":["inari.outbox.>"]}}`,
			wantReplicas: 1,
			wantFormed:   true,
		},
		{
			name:         "formed R=3",
			out:          `{"config":{"num_replicas":3,"subjects":["inari.outbox.>"]}}`,
			wantReplicas: 3,
			wantFormed:   true,
		},
		{
			name:         "replica mismatch is reported",
			out:          `{"config":{"num_replicas":1,"subjects":["inari.outbox.>"]}}`,
			wantReplicas: 3,
			wantFormed:   false,
			wantDetail:   "num_replicas=1 (want 3)",
		},
		{
			name:         "subject mismatch is reported",
			out:          `{"config":{"num_replicas":1,"subjects":["other.>"]}}`,
			wantReplicas: 1,
			wantFormed:   false,
			wantDetail:   "missing subject inari.outbox.>",
		},
		{
			name:         "unparseable output is reported",
			out:          `No Streams defined`,
			wantReplicas: 1,
			wantFormed:   false,
			wantDetail:   "unparseable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			formed, detail := parseOutboxStreamInfo(tc.out, tc.wantReplicas)
			if formed != tc.wantFormed {
				t.Fatalf("parseOutboxStreamInfo formed = %v, want %v (detail %q)", formed, tc.wantFormed, detail)
			}
			if tc.wantDetail != "" && !strings.Contains(detail, tc.wantDetail) {
				t.Fatalf("detail %q missing %q", detail, tc.wantDetail)
			}
		})
	}
}

func TestIsStreamNotFound(t *testing.T) {
	notFound := errors.New("kubectl -n inari exec deploy/nats-box -- nats stream info INARI_OUTBOX --server nats:4222 --json: exit status 1\n" +
		"nats: error: could not lookup Stream INARI_OUTBOX: stream not found (10059)")
	if !isStreamNotFound(notFound) {
		t.Fatalf("stream-not-found exec error must be classified as an expected miss")
	}
	broken := errors.New("exit status 1\nerror: context deadline exceeded: connection refused")
	if isStreamNotFound(broken) {
		t.Fatalf("generic probe failure must NOT be classified as stream-not-found")
	}
	if isStreamNotFound(nil) {
		t.Fatalf("nil error must not be classified as stream-not-found")
	}
}

func TestAudContains(t *testing.T) {
	for _, tc := range []struct {
		name string
		aud  any
		want bool
	}{
		{"scalar match", "inari-server", true},
		{"scalar mismatch", "other", false},
		{"array contains", []any{"account", "inari-server"}, true},
		{"array missing", []any{"account"}, false},
		{"empty array", []any{}, false},
		{"nil claim", nil, false},
		{"wrong type", 42, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := audContains(tc.aud, "inari-server"); got != tc.want {
				t.Fatalf("audContains(%v) = %v, want %v", tc.aud, got, tc.want)
			}
		})
	}
}
