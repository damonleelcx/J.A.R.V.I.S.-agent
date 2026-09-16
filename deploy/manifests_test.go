// Package deploy holds the offline fences for FORGE's Kubernetes manifests and
// deploy scripts.
//
// # Why these are Go tests
//
// The manifests are committed and applied by hand, over SSM, by deploy/apply.sh.
// Nothing between a commit and that apply reads them: a ConfigMap that loses a
// value, or a NetworkPolicy that loses a rule, is valid YAML that kubectl applies
// happily, and the first sign is a pod that cannot reach something. These tests
// run in `go test ./...` with everything else, with no cluster.
//
// # Why no YAML library
//
// None is a dependency of this module, and these files are FORGE's own, in a
// layout it controls. The readers below understand that layout and refuse any
// line they do not recognise, so a reformatted file fails loudly here rather
// than being half-read into a green result.
package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Checkouts with core.autocrlf have CRLF; the layout checks below are about
	// lines, not line endings.
	return strings.ReplaceAll(string(b), "\r", "")
}

// uncommented drops blank lines and whole-line comments. The manifests explain
// themselves at length, and a comment that mentions a key must not count as
// setting it.
func uncommented(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

var dataLine = regexp.MustCompile(`^  ([A-Z][A-Z0-9_]*): "(.*)"$`)

// configMapData is forge-config's data section.
func configMapData(t *testing.T) map[string]string {
	t.Helper()
	data := map[string]string{}
	in := false
	for _, l := range uncommented(read(t, "k8s/20-config.yaml")) {
		if l == "data:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		m := dataLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("20-config.yaml: cannot read data line %q (every value is written KEY: \"value\")", l)
		}
		data[m[1]] = m[2]
	}
	if len(data) == 0 {
		t.Fatal("20-config.yaml: no data section found")
	}
	return data
}

// The values stage S3 created and nothing else: AWS itself (no endpoint), and
// the node's instance role (no keys). A key in a manifest would win over the role
// in the SDK's chain — and be a credential in git.
func TestConfigMap_NamesTheProductionBucketAndRegionAndNoEndpointOrKeys(t *testing.T) {
	data := configMapData(t)
	if got := data["FORGE_BLOB_BUCKET"]; got != "forge-geometry-373468206837" {
		t.Errorf("FORGE_BLOB_BUCKET = %q, want the bucket deploy/bootstrap-s3.sh made", got)
	}
	if got := data["FORGE_BLOB_REGION"]; got != "us-east-1" {
		t.Errorf("FORGE_BLOB_REGION = %q, want us-east-1", got)
	}

	manifests, err := filepath.Glob("k8s/*.yaml")
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no manifests found: %v", err)
	}
	for _, f := range manifests {
		for _, l := range uncommented(read(t, f)) {
			for _, forbidden := range []string{"FORGE_BLOB_ENDPOINT", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
				if strings.Contains(l, forbidden) {
					t.Errorf("%s sets %s (%q): production reaches AWS itself as the instance role", f, forbidden, strings.TrimSpace(l))
				}
			}
		}
	}
}

// A region with no endpoint is only useful if the loader agrees it is a whole
// configuration; both half-configurations are refused at boot, which on this
// deployment would be both pods crash-looping.
func TestConfigMap_TheBlobValuesAreAConfigurationTheLoaderAccepts(t *testing.T) {
	data := configMapData(t)
	t.Setenv("FORGE_BLOB_BUCKET", data["FORGE_BLOB_BUCKET"])
	t.Setenv("FORGE_BLOB_REGION", data["FORGE_BLOB_REGION"])
	t.Setenv("FORGE_BLOB_ENDPOINT", data["FORGE_BLOB_ENDPOINT"])
	cfg, _, err := config.Load(config.SectionNone)
	if err != nil {
		t.Fatalf("the ConfigMap's blob values do not load: %v", err)
	}
	if !cfg.Blob.Configured() || cfg.Blob.Endpoint != "" {
		t.Errorf("loaded %+v, want a configured store addressing AWS itself", cfg.Blob)
	}
}

// egressRule is one entry of the worker policy's egress list.
type egressRule struct {
	// peers holds each `to` entry's text on one line. A namespaceSelector and a
	// podSelector are ANDed only inside one entry, so matching is per entry.
	peers []string
	ports map[string]bool // "TCP/443"
}

func (r egressRule) hasPeer(parts ...string) bool {
	for _, p := range r.peers {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(p, part)
		}
		if all {
			return true
		}
	}
	return false
}

var portEntry = regexp.MustCompile(`^\{ protocol: (TCP|UDP), port: (\d+) \}$`)

// workerEgress reads 32-worker-egress.yaml into the lines before `egress:` and
// the rules after it.
func workerEgress(t *testing.T) (head []string, rules []egressRule) {
	t.Helper()
	const file = "k8s/32-worker-egress.yaml"
	inEgress, section := false, ""
	for _, l := range uncommented(read(t, file)) {
		if !inEgress {
			if l == "  egress:" {
				inEgress = true
			} else {
				head = append(head, l)
			}
			continue
		}
		switch {
		case l == "    - to:":
			rules = append(rules, egressRule{ports: map[string]bool{}})
			section = "to"
		case l == "      ports:" && len(rules) > 0:
			section = "ports"
		case strings.HasPrefix(l, "        - ") && len(rules) > 0:
			item := strings.TrimPrefix(l, "        - ")
			r := &rules[len(rules)-1]
			if section == "to" {
				r.peers = append(r.peers, item)
			} else if m := portEntry.FindStringSubmatch(item); m != nil {
				r.ports[m[1]+"/"+m[2]] = true
			} else {
				t.Fatalf("%s: cannot read port %q", file, item)
			}
		case strings.HasPrefix(l, "          ") && section == "to" && len(rules) > 0 && len(rules[len(rules)-1].peers) > 0:
			r := &rules[len(rules)-1]
			r.peers[len(r.peers)-1] += " " + strings.TrimSpace(l)
		default:
			t.Fatalf("%s: cannot read %q; this fence reads the layout the file is written in", file, l)
		}
	}
	if len(rules) == 0 {
		t.Fatalf("%s: no egress rules found", file)
	}
	return head, rules
}

// forge-worker and nothing else (forged is on the host network, where a policy
// does nothing), and Egress alone (Ingress with no rules would deny all inbound).
func TestWorkerEgress_SelectsTheWorkerAndConstrainsOnlyEgress(t *testing.T) {
	head, _ := workerEgress(t)
	joined := strings.Join(head, "\n")
	for _, want := range []string{
		"kind: NetworkPolicy",
		"  namespace: forge",
		"  podSelector:\n    matchLabels:\n      app.kubernetes.io/name: forge-worker\n  policyTypes: [Egress]",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("32-worker-egress.yaml lacks %q", want)
		}
	}
}

// Every destination the worker uses, and nothing it does not. An Egress policy
// denies what it does not list, so a missing rule is an outage on apply and a
// wider one is a hole nobody decided on.
func TestWorkerEgress_AllowsDNSPostgresIMDSAndHTTPSAndNothingElse(t *testing.T) {
	_, rules := workerEgress(t)

	for i, r := range rules {
		if len(r.ports) == 0 {
			t.Errorf("rule %d names no ports, so it allows every port to %v", i+1, r.peers)
		}
		if len(r.peers) == 0 {
			t.Errorf("rule %d names no destination, so it allows everywhere on %v", i+1, r.ports)
		}
		if r.ports["TCP/587"] {
			t.Errorf("rule %d allows mail (587); the worker sends no mail", i+1)
		}
	}

	want := []struct {
		what  string
		ports []string
		peer  []string
	}{
		{"DNS to kube-dns", []string{"UDP/53", "TCP/53"},
			[]string{"kubernetes.io/metadata.name: kube-system", "k8s-app: kube-dns"}},
		{"postgres in heros", []string{"TCP/5432"},
			[]string{"kubernetes.io/metadata.name: heros", "app.kubernetes.io/name: postgres"}},
		{"IMDS for the instance role", []string{"TCP/80"},
			[]string{"ipBlock: cidr: 169.254.169.254/32"}},
		{"HTTPS to public addresses (S3, the model)", []string{"TCP/443"},
			[]string{"ipBlock: cidr: 0.0.0.0/0", "- 10.0.0.0/8"}},
	}
	for _, w := range want {
		found := false
		for _, r := range rules {
			all := r.hasPeer(w.peer...)
			for _, p := range w.ports {
				all = all && r.ports[p]
			}
			found = found || all
		}
		if !found {
			t.Errorf("no rule allows %s: ports %v to one peer with %v", w.what, w.ports, w.peer)
		}
	}
	if len(rules) != len(want) {
		t.Errorf("%d egress rules, want %d; a new one needs its reason and this fence updated with it", len(rules), len(want))
	}
}

// Check 9 must run the round trip in BOTH pods: they reach IMDS and S3 by
// different paths, so one passing says nothing about the other.
func TestVerify_ChecksTheBlobRoundTripInsideBothPods(t *testing.T) {
	s := read(t, "verify.sh")
	start := strings.Index(s, `echo "=== 9. `)
	if start < 0 {
		t.Fatal("verify.sh has no check 9")
	}
	end := strings.Index(s, "\necho\n[ $FAIL -eq 0 ]")
	if end < start {
		t.Fatal("check 9 is not before verify.sh's summary, so it would never be reported")
	}
	sec := s[start:end]

	loop := regexp.MustCompile(`(?m)^for d in ([^;]+); do$`).FindStringSubmatch(sec)
	if loop == nil {
		t.Fatal("check 9 does not loop over the pods")
	}
	pods := map[string]bool{}
	for _, p := range strings.Fields(loop[1]) {
		pods[p] = true
	}
	for _, p := range []string{"forged", "forge-worker"} {
		if !pods[p] {
			t.Errorf("check 9 does not run in %s (runs in %v)", p, strings.Fields(loop[1]))
		}
	}
	for _, want := range []string{
		`-l app.kubernetes.io/name=$d`,
		`-c "$d" -- /usr/local/bin/forgectl blob check`,
		`BLOB-ROUNDTRIP-OK [0-9a-f]\{64\}`,
		`bad "`,
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("check 9 lacks %q", want)
		}
	}
}
