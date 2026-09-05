package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/denmark/gage/internal/gage/gittest"
	"github.com/denmark/gage/internal/gage/remoteauth"
	"github.com/denmark/gage/internal/gage/syncerr"
)

// isolateState points $GAGE_STATE at a temp dir, so a test's stored
// tokens never touch the developer's own.
func isolateState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GAGE_STATE", "")
}

// repoWithOrigin builds a real repository with one commit whose origin is
// url — the shape Fetch and Push actually operate on.
func repoWithOrigin(t *testing.T, url string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitAndCommit(dir, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := SetRemote(dir, url); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFetchAndPushCarryTheStoredTokenToGoGit is the test the bare-repo
// harness structurally cannot be: every other sync test syncs against a
// local path, where auth is legitimately nil, so none of them notices if
// the resolved credentials never reach go-git at all. Removing `Auth:`
// from FetchOptions and PushOptions left the whole suite green before
// this existed.
//
// gittest's recording transport is what makes it possible without a
// network: it looks like a remote host to gage's auth resolution, records
// what go-git was handed, and fails instead of connecting.
func TestFetchAndPushCarryTheStoredTokenToGoGit(t *testing.T) {
	isolateState(t)
	recorder := gittest.InstallRecordingTransport(t, errors.New("recorded; not connecting"))

	const host = "git.example.com"
	if err := remoteauth.Store(host, "tok_secret"); err != nil {
		t.Fatal(err)
	}
	dir := repoWithOrigin(t, gittest.URL(host, "me/vault.git"))

	// Both operations must present the token; a regression in either one
	// alone would be just as broken.
	if _, err := Fetch(context.Background(), dir); err == nil {
		t.Fatal("the recording transport did not fail the fetch, so nothing was exercised")
	}
	if _, err := Push(context.Background(), dir); err == nil {
		t.Fatal("the recording transport did not fail the push, so nothing was exercised")
	}

	for _, tc := range []struct {
		op   string
		get  func() (transport.AuthMethod, bool)
		want string
	}{
		{"fetch", recorder.FetchAuth, "tok_secret"},
		{"push", recorder.PushAuth, "tok_secret"},
	} {
		auth, ok := tc.get()
		if !ok {
			t.Errorf("%s never reached the transport", tc.op)
			continue
		}
		basic, isBasic := auth.(*http.BasicAuth)
		if !isBasic {
			t.Errorf("%s presented %T, want *http.BasicAuth carrying the stored token", tc.op, auth)
			continue
		}
		if basic.Password != tc.want {
			t.Errorf("%s presented password %q, want the stored token", tc.op, basic.Password)
		}
		if basic.Username == "" {
			t.Errorf("%s presented an empty username; go-git omits the header entirely for one", tc.op)
		}
	}

	// The token has to be looked up under the origin's own host, not
	// some other one — that is what "two vaults on the same host share
	// one token" rests on.
	for _, got := range recorder.Hosts() {
		if got != host {
			t.Errorf("the transport was addressed with host %q, want %q", got, host)
		}
	}
}

// TestFetchAndPushGoAnonymousWithNoStoredToken is the other half: no
// token stored is not an error, it is an anonymous attempt. A private
// remote refusing that produces the auth message; pre-emptively refusing
// to try would be worse.
func TestFetchAndPushGoAnonymousWithNoStoredToken(t *testing.T) {
	isolateState(t)
	recorder := gittest.InstallRecordingTransport(t, errors.New("recorded; not connecting"))

	dir := repoWithOrigin(t, gittest.URL("git.example.com", "me/vault.git"))
	if _, err := Fetch(context.Background(), dir); err == nil {
		t.Fatal("the recording transport did not fail the fetch")
	}

	auth, ok := recorder.FetchAuth()
	if !ok {
		t.Fatal("the fetch never reached the transport")
	}
	if auth != nil {
		t.Errorf("fetch presented %v with no token stored, want an anonymous attempt", auth)
	}
}

// TestPushAfterLogoutNamesTheHostAndTheCommand is the `gage auth logout`
// contract at the layer that produces the message: once the token is
// gone, the remote refuses, and what a human sees has to name the host
// and `gage auth login` rather than a bare transport failure.
func TestPushAfterLogoutNamesTheHostAndTheCommand(t *testing.T) {
	isolateState(t)
	gittest.InstallRecordingTransport(t, transport.ErrAuthenticationRequired)

	const host = "git.example.com"
	if err := remoteauth.Store(host, "tok_secret"); err != nil {
		t.Fatal(err)
	}
	// `gage auth logout` in library terms.
	if err := remoteauth.Remove(host); err != nil {
		t.Fatal(err)
	}

	dir := repoWithOrigin(t, gittest.URL(host, "me/vault.git"))
	_, err := Push(context.Background(), dir)
	if err == nil {
		t.Fatal("push against a remote refusing us succeeded")
	}
	if !errors.Is(err, syncerr.ErrAuth) {
		t.Errorf("push error = %v, want it classified as auth", err)
	}
	for _, want := range []string{host, "gage auth login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("push error = %q, want it to mention %q", err, want)
		}
	}
}

// TestNoTokenValueLeaksIntoASyncError pins the confidentiality half: the
// token is a bearer credential, so it must not appear in what gets
// printed, logged, or written to session history.
func TestNoTokenValueLeaksIntoASyncError(t *testing.T) {
	isolateState(t)
	gittest.InstallRecordingTransport(t, transport.ErrAuthorizationFailed)

	const secret = "tok_do_not_print_me"
	if err := remoteauth.Store("git.example.com", secret); err != nil {
		t.Fatal(err)
	}
	dir := repoWithOrigin(t, gittest.URL("git.example.com", "me/vault.git"))

	_, fetchErr := Fetch(context.Background(), dir)
	_, pushErr := Push(context.Background(), dir)
	for _, err := range []error{fetchErr, pushErr} {
		if err == nil {
			t.Fatal("expected the remote's refusal to surface as an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the token value appears in an error message: %q", err)
		}
	}
}
