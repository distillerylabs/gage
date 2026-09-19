// Package remoteauth resolves how gage authenticates to a git remote, and
// stores the tokens it does that with.
//
// Per Q-GIT-AUTH the supported path is HTTPS with a user-supplied token,
// held per *host* — two vaults on the same host share one token — under
// $GAGE_STATE/tokens/<host> at 0600. That root is documented as
// disposable, machine-local state, which is exactly what a token is:
// re-acquirable by logging in again, unlike an identity file. SSH remotes
// are best-effort through ssh-agent; go-git never reads ~/.ssh/config, so
// a remote that depends on a host alias is refused with a message saying
// so rather than a generic connection failure.
package remoteauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/distillerylabs/gage/internal/gage/atomicfile"
	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

var (
	// ErrNoToken means no token is stored for the host in question.
	ErrNoToken = errors.New("gage: no token is stored for this host")

	// ErrInsecureToken means a stored token file is readable by more than
	// its owner. A token is a bearer credential, so a loosened mode is
	// refused rather than used — the same posture identity files take.
	ErrInsecureToken = errors.New("gage: token file permissions are too permissive")

	// ErrSSHConfigAlias means a remote is SSH-spelled against a host that
	// only ~/.ssh/config could resolve. go-git never reads that file, so
	// the remote cannot work and says why — see "Remote authentication"
	// in the design doc.
	ErrSSHConfigAlias = errors.New("gage: this remote depends on a ~/.ssh/config host alias, which gage does not read")
)

// tokenFileMode is the mode a stored token is created with, and the
// loosest mode Load will accept.
const tokenFileMode = 0o600

// TokensDir is $GAGE_STATE/tokens, where per-host tokens live.
func TokensDir() (string, error) {
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "tokens"), nil
}

// TokenPath returns the file a host's token is stored in.
func TokenPath(host string) (string, error) {
	if err := checkHost(host); err != nil {
		return "", err
	}
	dir, err := TokensDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host), nil
}

// checkHost rejects a host that wouldn't stay a single path component.
// The host reaches this package from a remote URL, which is committed,
// user-editable config — so "github.com/../../etc" must be refused rather
// than helpfully resolved, the same rule Q-DEVICE-NAME applies to device
// names.
func checkHost(host string) error {
	bad := ""
	switch {
	case host == "":
		bad = "it is empty"
	case host == "." || host == "..":
		bad = "it is a relative path element"
	case strings.ContainsAny(host, `/\`):
		bad = "it contains a path separator"
	case strings.ContainsRune(host, 0):
		bad = "it contains a NUL byte"
	case host != filepath.Clean(host):
		bad = "it is not a single, already-clean path component"
	}
	if bad == "" {
		return nil
	}
	return exitcode.Newf(exitcode.Usage, "gage: host %q cannot be used as a path component: %s", host, bad)
}

// Store writes host's token at 0600, creating $GAGE_STATE/tokens if this
// is the first one on this machine.
func Store(host, token string) error {
	if strings.TrimSpace(token) == "" {
		return exitcode.New(exitcode.Usage, "gage: an empty token is not accepted")
	}
	path, err := TokenPath(host)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating token directory: %w", err))
	}
	if err := atomicfile.WriteFile(path, []byte(token), tokenFileMode); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: storing token: %w", err))
	}
	return nil
}

// Load reads host's token, refusing a file that others can read.
func Load(host string) (string, error) {
	path, err := TokenPath(host)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrNoToken, host)
		}
		return "", exitcode.Wrap(exitcode.Internal, err)
	}
	// Windows doesn't model these bits — a file there reads back as 0666
	// regardless of its real ACL — so enforcing them would refuse every
	// token on that platform for a property it can't express.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", exitcode.Wrap(exitcode.LockedOrAuth,
			fmt.Errorf("%w: %s is %04o, want %04o; fix it or run `gage auth login --host %s` again",
				ErrInsecureToken, path, info.Mode().Perm(), tokenFileMode, host))
	}
	// #nosec G304 -- path comes from TokenPath, which validates host as a
	// single safe path component first.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", exitcode.Wrap(exitcode.Internal, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%w: %s (the stored file is empty)", ErrNoToken, host)
	}
	return token, nil
}

// Remove forgets host's token. Removing one that isn't there is not an
// error — `gage auth logout` twice should not fail the second time.
func Remove(host string) error {
	path, err := TokenPath(host)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	return nil
}

// Hosts lists every host with a stored token, sorted — what `gage auth
// status` reports.
func Hosts() ([]string, error) {
	dir, err := TokensDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}
	hosts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hosts = append(hosts, e.Name())
	}
	sort.Strings(hosts)
	return hosts, nil
}

// Host reports the host a remote URL names — the key its token is stored
// under. It understands both URL-spelled remotes
// (https://github.com/u/r.git) and scp-spelled ones (git@github.com:u/r.git),
// because go-git's own endpoint parser does.
//
// A local path remote (what tests sync against, and what a USB-drive
// vault would use) has no host at all and reports "".
func Host(remoteURL string) (string, error) {
	ep, err := transport.NewEndpoint(remoteURL)
	if err != nil {
		return "", exitcode.Newf(exitcode.Usage, "gage: cannot parse remote %q: %v", remoteURL, err)
	}
	if ep.Protocol == "file" {
		return "", nil
	}
	return ep.Host, nil
}

// Method resolves the go-git auth for a remote URL: a stored token over
// HTTPS, best-effort ssh-agent over SSH, and nothing at all for a local
// path. A nil AuthMethod means "attempt this unauthenticated", which is
// what go-git wants for an anonymous or local remote.
func Method(remoteURL string) (transport.AuthMethod, error) {
	ep, err := transport.NewEndpoint(remoteURL)
	if err != nil {
		return nil, exitcode.Newf(exitcode.Usage, "gage: cannot parse remote %q: %v", remoteURL, err)
	}

	switch ep.Protocol {
	case "file":
		return nil, nil
	case "ssh":
		return sshMethod(ep)
	default:
		return httpMethod(ep)
	}
}

// httpMethod pairs a stored token with go-git's BasicAuth. A missing
// token isn't an error here: an anonymous remote is legitimate, and a
// private one refusing the anonymous attempt produces a 401 that
// classifies as syncerr.ErrAuth — carrying AuthHint's pointer at `gage
// auth login`, which is a better message than pre-emptively refusing to
// try.
func httpMethod(ep *transport.Endpoint) (transport.AuthMethod, error) {
	token, err := Load(ep.Host)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return nil, nil
		}
		return nil, err
	}
	// The username is ignored by every host gage targets when the
	// password is a token, but it cannot be empty or go-git's BasicAuth
	// refuses to send the header at all.
	return &http.BasicAuth{Username: "gage", Password: token}, nil
}

// sshMethod returns ssh-agent auth for an SSH remote, or refuses one that
// only ~/.ssh/config could resolve.
func sshMethod(ep *transport.Endpoint) (transport.AuthMethod, error) {
	if isSSHConfigAlias(ep.Host) {
		return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf(
			"%w: %q is not a resolvable host name. Re-spell the remote with the host's real "+
				"name, or (recommended) use its HTTPS URL with `gage auth login`",
			ErrSSHConfigAlias, ep.Host))
	}
	auth, err := ssh.NewSSHAgentAuth(ep.User)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.LockedOrAuth, fmt.Errorf(
			"gage: this remote is SSH-spelled and gage could not reach ssh-agent (%v). "+
				"SSH support is best-effort; use the remote's HTTPS URL with `gage auth login` instead", err))
	}
	return auth, nil
}

// isSSHConfigAlias guesses whether an SSH host is really an ~/.ssh/config
// alias rather than a name that resolves on its own.
//
// The heuristic is that it contains no dot: every real git host is a
// fully-qualified name (github.com, git.example.internal), while an alias
// is a bare word ("internal", "work") chosen for typing convenience. This
// is deliberately a *shape* check rather than a lookup — resolving the
// name would need DNS, and reading ~/.ssh/config to answer the question
// would mean parsing the very file gage promises not to interpret. Being
// wrong here costs a clear, actionable error message on a remote that
// would have failed anyway, which is the right side to err on.
func isSSHConfigAlias(host string) bool {
	return host != "" && !strings.Contains(host, ".")
}

// TokenHost reports the host a remote would authenticate a token to, or
// "" if the remote isn't a token candidate at all: a local path (no host
// to hold a token) or an SSH remote (best-effort ssh-agent, per
// Q-GIT-AUTH — a token has no role there). It mirrors Method's protocol
// dispatch without loading or storing anything, so a caller can decide
// whether to solicit a token *before* attempting a connection, e.g.
// `gage init --remote` prompting once up front instead of failing and
// pointing at `gage auth login` for a retry.
func TokenHost(remoteURL string) (string, error) {
	ep, err := transport.NewEndpoint(remoteURL)
	if err != nil {
		return "", exitcode.Newf(exitcode.Usage, "gage: cannot parse remote %q: %v", remoteURL, err)
	}
	if ep.Protocol == "file" || ep.Protocol == "ssh" {
		return "", nil
	}
	return ep.Host, nil
}

// AuthHint is the sentence gage appends when a remote refuses access, so
// an expired or absent token names the host and the command that fixes it
// rather than surfacing a bare 403.
//
// A remote with no host is a local path (a directory, a mounted drive),
// where no token exists to be wrong — suggesting `gage auth login` there
// would send someone to fix something that isn't the problem.
func AuthHint(host string) string {
	if host == "" {
		return "check that the repository exists at that path and is readable"
	}
	return fmt.Sprintf(
		"run `gage auth login --host %s` to store a token for %s (an existing one may have expired or been revoked)",
		host, host)
}
