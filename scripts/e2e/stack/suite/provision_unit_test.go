//go:build e2e

// Unit tests for the pure helpers of the provisioner/seed stages. They run
// under the e2e tag (the suite package is e2e-only) but need no cluster.
package suite

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	// Verbatim stderr of `nats stream info INARI_OUTBOX` for a MISSING
	// stream under the pinned probe image (natsio/nats-box:0.20.0, nats CLI
	// v0.3.x) — the dominant expected-miss shape in this suite.
	pinnedCLI := errors.New("kubectl -n inari exec deploy/nats-box -- nats stream info INARI_OUTBOX --server nats:4222 --json: exit status 1\n" +
		"nats: error: could not pick a Stream to operate on: cannot pick a Stream without a terminal and no Stream name supplied")
	if !isStreamNotFound(pinnedCLI) {
		t.Fatalf("pinned nats-box missing-stream error must be classified as an expected miss")
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

// TestGitOpsPinParity guards the e2e↔gitops pin contract: the chart/package
// versions provision.go installs imperatively must equal the targetRevisions
// of the corresponding gitops Applications (the production install path).
// renovate.json has no manager matching gitops/*.yaml or provision.go today;
// if one is ever added it must cover BOTH sides of this test.
func TestGitOpsPinParity(t *testing.T) {
	opsDir := filepath.Join(stackDir(), "..", "..", "..", "gitops", "operators")
	for _, tc := range []struct {
		name string
		file string
		want []string // targetRevisions in spec source order
	}{
		{"cert-manager", "cert-manager.yaml", []string{certManagerChartVersion}},
		// external-dns is multi-source: helm chart, then the upstream CRD
		// manifest pinned at the chart's appVersion tag.
		{"external-dns", "external-dns.yaml", []string{externalDNSChartVersion, externalDNSAppVersion}},
		{"external-secrets", "eso.yaml", []string{esoChartVersion}},
		{"crossplane", "crossplane.yaml", []string{crossplaneChartVersion}},
		{"kro", "kro.yaml", []string{kroChartVersion}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := targetRevisions(t, filepath.Join(opsDir, tc.file))
			if len(got) != len(tc.want) {
				t.Fatalf("%s: got targetRevisions %v, want %v", tc.file, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("%s: targetRevision[%d] = %q, want %q (pin constant in provision.go)", tc.file, i, got[i], tc.want[i])
				}
			}
		})
	}
	t.Run("provider-keycloak", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(opsDir, "crossplane-provider-keycloak", "provider.yaml"))
		if err != nil {
			t.Fatalf("reading provider.yaml: %v", err)
		}
		want := "package: " + providerKeycloakPackage + ":" + providerKeycloakVersion
		if !strings.Contains(string(raw), want) {
			t.Fatalf("provider.yaml missing %q", want)
		}
	})
}

// targetRevisions returns every spec source's targetRevision in file order.
func targetRevisions(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	re := regexp.MustCompile(`targetRevision:\s*([^\s"']+)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	return out
}

// TestGitOpsSourceReposWhitelist asserts every Application source repoURL in
// gitops/{operators,platform,apps} is covered by the AppProject sourceRepos
// whitelist — ArgoCD rejects uncovered sources with "repository not
// permitted by project" at sync time. Codified after the kro registry move
// (ghcr.io/kro-run -> registry.k8s.io/kro) updated everything except the
// whitelist. ArgoCD glob semantics: '*' matches within a path segment,
// '**' crosses separators.
func TestGitOpsSourceReposWhitelist(t *testing.T) {
	root := filepath.Join(stackDir(), "..", "..", "..", "gitops")
	allowed := sourceReposEntries(t, filepath.Join(root, "argocd-project.yaml"))
	urlRe := regexp.MustCompile(`repoURL:\s*([^\s"']+)`)
	for _, dir := range []string{"operators", "platform", "apps"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("reading gitops/%s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatalf("reading %s/%s: %v", dir, e.Name(), err)
			}
			if !strings.Contains(string(raw), "kind: Application") {
				continue
			}
			for _, m := range urlRe.FindAllStringSubmatch(string(raw), -1) {
				if !sourceRepoAllowed(allowed, m[1]) {
					t.Errorf("%s/%s: repoURL %q not covered by argocd-project sourceRepos", dir, e.Name(), m[1])
				}
			}
		}
	}
}

// sourceReposEntries extracts the quoted sourceRepos entries from
// argocd-project.yaml (comment lines inside the block are skipped).
func sourceReposEntries(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	entryRe := regexp.MustCompile(`^\s+-\s+"([^"]+)"`)
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "sourceRepos:") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if m := entryRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
			continue
		}
		// A non-entry, non-comment line ends the block (e.g. "destinations:").
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			break
		}
	}
	if len(out) == 0 {
		t.Fatalf("no sourceRepos entries found in %s", path)
	}
	return out
}

// sourceRepoAllowed reports whether url matches any whitelist entry under
// ArgoCD glob semantics: '*' matches within a path segment, '**' crosses
// separators (zero or more segments).
func sourceRepoAllowed(allowed []string, url string) bool {
	for _, pat := range allowed {
		if argoGlobMatch(pat, url) {
			return true
		}
	}
	return false
}

// argoGlobMatch matches a URL against an ArgoCD sourceRepos glob. Segment
// recursion keeps '**' semantics honest: it consumes zero or more whole
// path segments while '*' stays segment-local.
func argoGlobMatch(pat, url string) bool {
	pp, up := strings.Split(pat, "/"), strings.Split(url, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(pp) {
			if pp[i] == "**" {
				for k := j; k <= len(up); k++ {
					if match(i+1, k) {
						return true
					}
				}
				return false
			}
			if j >= len(up) {
				return false
			}
			segRe := "^" + strings.ReplaceAll(regexp.QuoteMeta(pp[i]), `\*`, "[^/]*") + "$"
			if ok, _ := regexp.MatchString(segRe, up[j]); !ok {
				return false
			}
			i++
			j++
		}
		return j == len(up)
	}
	return match(0, 0)
}
