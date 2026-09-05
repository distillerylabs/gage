package gittest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
)

// RecordingScheme is the URL scheme RecordingTransport installs itself
// under. A remote spelled with it looks like a network remote to
// everything upstream of the transport — gage's auth resolution included
// — while never opening a socket.
const RecordingScheme = "gagetest"

// RecordingTransport is a go-git transport that records the credentials
// go-git was handed and then fails, without touching the network.
//
// It exists because gage's realistic sync tests all run against local
// bare repositories, where auth is legitimately nil — so nothing in that
// harness can tell whether a stored token actually reaches go-git.
// Deleting `Auth:` from gage's FetchOptions and PushOptions used to leave
// the whole suite green. This closes that hole: a test points a vault at
// a gagetest:// remote, performs a fetch or a push, and asserts on what
// arrived here.
//
// It is safe for a test to fail the operation, because the operation
// failing is the point — what is under test is the argument, not the
// transfer.
type RecordingTransport struct {
	mu sync.Mutex

	// fail is what every session constructor returns, so a test can
	// choose which classification the caller sees.
	fail error

	uploadAuth   []transport.AuthMethod
	receiveAuth  []transport.AuthMethod
	uploadHosts  []string
	receiveHosts []string
}

// InstallRecordingTransport registers a RecordingTransport for
// RecordingScheme and removes it when the test ends.
//
// Every session it hands out fails with failWith, which a test picks to
// steer classification: transport.ErrAuthenticationRequired to exercise
// the auth path, or any other error to simply stop before the network.
//
// go-git's protocol registry is process-global, so a test using this must
// not run in parallel with another one that does.
func InstallRecordingTransport(t testing.TB, failWith error) *RecordingTransport {
	t.Helper()

	rt := &RecordingTransport{fail: failWith}
	client.InstallProtocol(RecordingScheme, rt)
	t.Cleanup(func() { client.InstallProtocol(RecordingScheme, nil) })
	return rt
}

// URL builds a remote URL on this transport for the given host and path,
// e.g. URL("github.com", "me/vault.git").
func URL(host, repoPath string) string {
	return fmt.Sprintf("%s://%s/%s", RecordingScheme, host, repoPath)
}

// NewUploadPackSession records a fetch's credentials, then fails.
func (r *RecordingTransport) NewUploadPackSession(
	ep *transport.Endpoint, auth transport.AuthMethod,
) (transport.UploadPackSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.uploadAuth = append(r.uploadAuth, auth)
	r.uploadHosts = append(r.uploadHosts, ep.Host)
	return nil, r.fail
}

// NewReceivePackSession records a push's credentials, then fails.
func (r *RecordingTransport) NewReceivePackSession(
	ep *transport.Endpoint, auth transport.AuthMethod,
) (transport.ReceivePackSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.receiveAuth = append(r.receiveAuth, auth)
	r.receiveHosts = append(r.receiveHosts, ep.Host)
	return nil, r.fail
}

// FetchAuth returns the credentials the most recent fetch presented, and
// whether a fetch reached the transport at all. A nil AuthMethod with ok
// true means go-git was asked to fetch anonymously.
func (r *RecordingTransport) FetchAuth() (auth transport.AuthMethod, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.uploadAuth) == 0 {
		return nil, false
	}
	return r.uploadAuth[len(r.uploadAuth)-1], true
}

// PushAuth returns the credentials the most recent push presented, and
// whether a push reached the transport at all.
func (r *RecordingTransport) PushAuth() (auth transport.AuthMethod, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.receiveAuth) == 0 {
		return nil, false
	}
	return r.receiveAuth[len(r.receiveAuth)-1], true
}

// Hosts returns every host this transport was addressed with, fetches
// first — so a test can assert a token was looked up under the origin's
// own host and not some other one.
func (r *RecordingTransport) Hosts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.uploadHosts)+len(r.receiveHosts))
	out = append(out, r.uploadHosts...)
	return append(out, r.receiveHosts...)
}
