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
