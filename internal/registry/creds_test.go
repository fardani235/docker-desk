package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestResolveCredentialKeys(t *testing.T) {
	dir := writeConfig(t, fmt.Sprintf(`{"auths":{
		"https://index.docker.io/v1/":{"auth":%q},
		"ghcr.io":{"auth":%q},
		"registry.internal:5000":{"auth":%q},
		"https://other.example/v2/":{"auth":%q}
	}}`, b64("hubuser:hub:pass:word"), b64(":ghcr-token"), b64("int:intpw"), b64("o:opw")))
	for _, tc := range []struct{ ref, user, pass string }{
		{"alpine", "hubuser", "hub:pass:word"},
		{"docker.io/library/alpine:3", "hubuser", "hub:pass:word"},
		{"ghcr.io/acme/api", "", "ghcr-token"},
		{"registry.internal:5000/api", "int", "intpw"},
		{"other.example/x", "o", "opw"},
	} {
		r, _ := ParseRef(tc.ref)
		c, err := ResolveCredential(context.Background(), dir, r.Key)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if c.Username != tc.user || c.Password.Reveal() != tc.pass || c.Source != "auths" {
			t.Errorf("%s: got user %q pass %q from %s", tc.ref, c.Username, c.Password.Reveal(), c.Source)
		}
		if strings.Contains(fmt.Sprintf("%v %+v %#v", c, c, c), tc.pass) {
			t.Errorf("%s: credential formats its password", tc.ref)
		}
	}
	r, _ := ParseRef("quay.io/x/y")
	if c, err := ResolveCredential(context.Background(), dir, r.Key); err != nil || c.Present() {
		t.Errorf("unconfigured host: %+v %v", c, err)
	}
	if c, err := ResolveCredential(context.Background(), filepath.Join(dir, "absent"), r.Key); err != nil || c.Present() {
		t.Errorf("missing directory: %+v %v", c, err)
	}
}

func TestResolveCredentialTokens(t *testing.T) {
	dir := writeConfig(t, `{"auths":{"reg.example":{"identitytoken":"idtok"},"tok.example":{"registrytoken":"regtok"}}}`)
	c, _ := ResolveCredential(context.Background(), dir, "reg.example")
	if c.IdentityToken.Reveal() != "idtok" {
		t.Errorf("identitytoken ignored: %+v", c)
	}
	c, _ = ResolveCredential(context.Background(), dir, "tok.example")
	if c.RegistryToken.Reveal() != "regtok" {
		t.Errorf("registrytoken ignored: %+v", c)
	}
}

// fakeHelper installs docker-credential-<name> on PATH. It records what it
// read on standard input, and answers with answer and status.
func fakeHelper(t *testing.T, name, answer string, status int, sleep string) (stdin string) {
	t.Helper()
	bin := t.TempDir()
	stdin = filepath.Join(bin, "stdin")
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = get ] || exit 9\ncat > %q\n%s\nprintf '%%s' %q\nexit %d\n", stdin, sleep, answer, status)
	if err := os.WriteFile(filepath.Join(bin, "docker-credential-"+name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return stdin
}

func TestCredentialHelper(t *testing.T) {
	const pw = "helper-secret-9f3a"
	stdin := fakeHelper(t, "fake", `{"ServerURL":"x","Username":"op","Secret":"`+pw+`"}`, 0, "")
	dir := writeConfig(t, `{"credsStore":"fake","auths":{"https://index.docker.io/v1/":{}}}`)
	c, err := ResolveCredential(context.Background(), dir, DockerHubKey)
	if err != nil || c.Username != "op" || c.Password.Reveal() != pw || c.Source != "credsStore fake" {
		t.Fatalf("got %+v %v", c, err)
	}
	if got, _ := os.ReadFile(stdin); string(got) != DockerHubKey {
		t.Errorf("helper read %q on stdin, want the server key", got)
	}

	// credHelpers wins over credsStore, and "<token>" is an identity token.
	stdin = fakeHelper(t, "ecr", `{"Username":"<token>","Secret":"`+pw+`"}`, 0, "")
	dir = writeConfig(t, `{"credsStore":"absent","credHelpers":{"ghcr.io":"ecr"}}`)
	c, err = ResolveCredential(context.Background(), dir, "ghcr.io")
	if err != nil || c.IdentityToken.Reveal() != pw || c.Password.Present() {
		t.Fatalf("credHelpers: %+v %v", c, err)
	}
	if got, _ := os.ReadFile(stdin); string(got) != "ghcr.io" {
		t.Errorf("helper read %q", got)
	}
}

func TestCredentialHelperFailures(t *testing.T) {
	const leak = "LEAKED-helper-output-7c1e"
	old := HelperTimeout
	HelperTimeout = 300 * time.Millisecond
	t.Cleanup(func() { HelperTimeout = old })
	for _, tc := range []struct {
		name, helper, answer, sleep string
		status                      int
		want                        string
	}{
		{"nonzero", "fake", leak, "", 3, "exited with status 3"},
		{"garbage", "fake", leak, "", 0, "not JSON"},
		{"slow", "fake", leak, "sleep 5", 0, "did not answer"},
		{"missing", "nothere", "", "", 0, "not on PATH"},
		{"bad name", "../evil", "", "", 0, "not a plain program name"},
	} {
		fakeHelper(t, "fake", tc.answer, tc.status, tc.sleep)
		dir := writeConfig(t, `{"credsStore":"`+tc.helper+`"}`)
		start := time.Now()
		_, err := ResolveCredential(context.Background(), dir, "ghcr.io")
		if KindOf(err) != KindHelper {
			t.Fatalf("%s: %v, want KindHelper", tc.name, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: the helper's bound did not hold (%s)", tc.name, time.Since(start))
		}
		msg := fmt.Sprintf("%v %+v %#v", err, err, err)
		if strings.Contains(msg, leak) {
			t.Errorf("%s: helper output reached the error: %s", tc.name, msg)
		}
		if !strings.Contains(err.Error(), tc.want) || !Fallback(err) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// "credentials not found" is no credential.
	fakeHelper(t, "fake", "credentials not found in native keychain", 1, "")
	dir := writeConfig(t, `{"credsStore":"fake"}`)
	if c, err := ResolveCredential(context.Background(), dir, "ghcr.io"); err != nil || c.Present() {
		t.Errorf("not found: %+v %v", c, err)
	}
}

// hubSiblings is the Docker Hub legacy key beside the access-token and
// refresh-token entries Docker Desktop files under the same host, each with
// its own credential, so a wrong choice is visible as the wrong password.
const (
	hubPass     = "hub-legacy-pw-41d2"
	accessPass  = "hub-access-pw-8e07"
	refreshPass = "hub-refresh-pw-c39b"
	earlyPass   = "hub-early-pw-5a6f"
)

var siblingPasses = []string{hubPass, accessPass, refreshPass, earlyPass}

func hubSiblingsConfig(t *testing.T, extra string) string {
	t.Helper()
	return writeConfig(t, fmt.Sprintf(`{"auths":{
		"https://index.docker.io/v1/access-token":{"auth":%q},
		"https://index.docker.io/v1/":{"auth":%q},
		"https://index.docker.io/v1/refresh-token":{"auth":%q}%s
	}}`, b64("access:"+accessPass), b64("hubuser:"+hubPass), b64("refresh:"+refreshPass), extra))
}

// checkHubChoice resolves DockerHubKey n times and requires the legacy key's
// credential every time. Failures name the entry by its user, never by its
// password, so a wrong choice keeps every value out of the test output.
func checkHubChoice(t *testing.T, dir string, n int) {
	t.Helper()
	wrong := map[string]int{}
	for i := 0; i < n; i++ {
		c, err := ResolveCredential(context.Background(), dir, DockerHubKey)
		if err != nil {
			t.Fatalf("resolution %d: %v", i, err)
		}
		if c.Source != "auths" {
			t.Fatalf("resolution %d: source %q, want auths", i, c.Source)
		}
		if c.Username != "hubuser" || c.Password.Reveal() != hubPass {
			wrong[c.Username]++
		}
		assertNoValues(t, fmt.Sprintf("%v %+v %#v %s", c, c, c, c.Source))
	}
	if len(wrong) > 0 {
		t.Errorf("%d resolutions: a sibling entry was chosen over the server key (by user: %v)", n, wrong)
	}
	if got := CredentialSource(dir, DockerHubKey); got != "auths" {
		t.Errorf("CredentialSource = %q, want auths", got)
	}
}

// assertNoValues fails if any fixture credential appears in s, without
// printing s.
func assertNoValues(t *testing.T, s string) {
	t.Helper()
	for i, v := range siblingPasses {
		if strings.Contains(s, v) {
			t.Fatalf("fixture credential %d appears in formatted output", i)
		}
	}
}

func TestResolveCredentialExactKeyBeatsSiblings(t *testing.T) {
	// The three keys Docker Desktop writes: map iteration order picked a
	// sibling about a third of the time before the exact pass.
	checkHubChoice(t, hubSiblingsConfig(t, ""), 200)

	// A sibling whose key sorts ahead of the server key: neither map order
	// nor the fallback's sorted order may decide, only the exact pass.
	early := fmt.Sprintf(`,
		"http://index.docker.io/v1/access-token":{"auth":%q}`, b64("early:"+earlyPass))
	checkHubChoice(t, hubSiblingsConfig(t, early), 200)
}

// recordingHelpers installs docker-credential-<name> for each name in one
// directory on PATH. Each appends its own name to the returned log and
// answers with a credential whose user is that name.
func recordingHelpers(t *testing.T, names ...string) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "invoked")
	for _, n := range names {
		script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = get ] || exit 9\ncat > /dev/null\nprintf '%%s\\n' %q >> %q\nprintf '%%s' '{\"Username\":\"%s\",\"Secret\":\"helper-pw-%s\"}'\n", n, log, n, n)
		if err := os.WriteFile(filepath.Join(bin, "docker-credential-"+n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	return log
}

func TestCredHelpersExactKeyBeatsSiblings(t *testing.T) {
	log := recordingHelpers(t, "hub", "sibling", "early")
	dir := writeConfig(t, `{"credsStore":"absent","credHelpers":{
		"https://index.docker.io/v1/access-token":"sibling",
		"https://index.docker.io/v1/":"hub",
		"http://index.docker.io/v1/refresh-token":"early"
	}}`)
	const n = 50
	for i := 0; i < n; i++ {
		c, err := ResolveCredential(context.Background(), dir, DockerHubKey)
		if err != nil {
			t.Fatalf("resolution %d: %v", i, err)
		}
		if c.Source != "credHelpers hub" || c.Username != "hub" {
			t.Fatalf("resolution %d: source %q user %q, want the exact key's helper", i, c.Source, c.Username)
		}
		if s := fmt.Sprintf("%v %+v %#v %s", c, c, c, c.Source); strings.Contains(s, "helper-pw-") {
			t.Fatalf("resolution %d: a helper's secret appears in formatted output", i)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(b))
	if len(lines) != n {
		t.Fatalf("%d helper runs recorded, want %d", len(lines), n)
	}
	for i, name := range lines {
		if name != "hub" {
			t.Fatalf("run %d invoked docker-credential-%s, want docker-credential-hub", i, name)
		}
	}
	if got := CredentialSource(dir, DockerHubKey); got != "credHelpers hub" {
		t.Errorf("CredentialSource = %q, want credHelpers hub", got)
	}
}

func TestResolveCredentialLegacyFallback(t *testing.T) {
	// A host's only entry is filed under a key that is not the server key:
	// the host-name fallback still finds it.
	dir := writeConfig(t, fmt.Sprintf(`{"auths":{
		"index.docker.io":{"auth":%q},
		"https://ghcr.io":{"auth":%q}
	}}`, b64("legacy:"+hubPass), b64(":"+accessPass)))
	for _, tc := range []struct{ key, user, pass string }{
		{DockerHubKey, "legacy", hubPass},
		{"ghcr.io", "", accessPass},
	} {
		c, err := ResolveCredential(context.Background(), dir, tc.key)
		if err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if !c.Present() || c.Username != tc.user || c.Password.Reveal() != tc.pass {
			t.Errorf("%s: the legacy entry was not used (user %q, present %v)", tc.key, c.Username, c.Present())
		}
		if c.Source != "auths" {
			t.Errorf("%s: source %q, want auths", tc.key, c.Source)
		}
		assertNoValues(t, fmt.Sprintf("%v %+v %#v %s", c, c, c, c.Source))
		if got := CredentialSource(dir, tc.key); got != "auths" {
			t.Errorf("%s: CredentialSource = %q, want auths", tc.key, got)
		}
	}

	// The same fallback serves credHelpers.
	log := recordingHelpers(t, "legacy")
	hdir := writeConfig(t, `{"credHelpers":{"https://ghcr.io/":"legacy"}}`)
	if c, err := ResolveCredential(context.Background(), hdir, "ghcr.io"); err != nil || c.Source != "credHelpers legacy" {
		t.Errorf("credHelpers legacy key: source %q, err %v", c.Source, err)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "legacy" {
		t.Errorf("legacy helper runs: %q", b)
	}

	// No entry for the host at all is no credential, not an error, even
	// beside entries for other hosts and Docker Hub siblings.
	dir = hubSiblingsConfig(t, "")
	for _, key := range []string{"quay.io", "registry.internal:5000"} {
		c, err := ResolveCredential(context.Background(), dir, key)
		if err != nil || c.Present() || c.Source != "" {
			t.Errorf("%s: want no credential, got present %v source %q err %v", key, c.Present(), c.Source, err)
		}
		if got := CredentialSource(dir, key); got != "" {
			t.Errorf("%s: CredentialSource = %q, want none", key, got)
		}
	}
}

// A malformed entry under the exact key is an error, not a reason to use a
// sibling, and the error names no credential of any entry.
func TestResolveCredentialExactKeyMalformed(t *testing.T) {
	dir := writeConfig(t, fmt.Sprintf(`{"auths":{
		"https://index.docker.io/v1/access-token":{"auth":%q},
		"https://index.docker.io/v1/":{"auth":"%s!"},
		"https://index.docker.io/v1/refresh-token":{"auth":%q}
	}}`, b64("access:"+accessPass), hubPass, b64("refresh:"+refreshPass)))
	for i := 0; i < 50; i++ {
		c, err := ResolveCredential(context.Background(), dir, DockerHubKey)
		if err == nil || c.Present() {
			t.Fatalf("resolution %d: a sibling was used in place of the malformed server key (user %q)", i, c.Username)
		}
		if KindOf(err) != KindHelper || !strings.Contains(err.Error(), "not valid base64") {
			t.Fatalf("resolution %d: unexpected error kind or reason", i)
		}
		assertNoValues(t, fmt.Sprintf("%v %+v %#v", err, err, err))
	}
}
