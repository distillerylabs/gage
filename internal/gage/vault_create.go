package gage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/denmark/gage/internal/gage/agekey"
	"github.com/denmark/gage/internal/gage/atomicfile"
	"github.com/denmark/gage/internal/gage/devicename"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
	"github.com/denmark/gage/internal/gage/vaultconfig"
)

// The only accepted values for CreateSpec.Type and CreateSpec.Method
// today. Both are validated against these single-element allowlists
// rather than hardcoded, so a second value later (a new vault type, a
// new identity method) is additive to the CLI surface instead of a
// breaking change — see "Vault types" and Q-METHOD-FLAG in the design
// doc.
const (
	TypeGit = "git"

	MethodPassphrase = "passphrase"
)

// AllowedTypes lists every vault type this build of gage accepts.
func AllowedTypes() []string { return []string{TypeGit} }

// AllowedMethods lists every identity method this build of gage accepts.
func AllowedMethods() []string { return []string{MethodPassphrase} }

// initialRecipientCommitMessage is InitAndCommit's message for the one
// commit `gage init` produces.
const initialRecipientCommitMessage = "gage: initialize vault"

// CreateSpec is everything Create needs to lay out a new vault: no
// identity, no crypto — just a name, type, method, target path, and the
// recipients' already-generated public keys as plain strings. Create
// stays identity-agnostic on purpose: generating this device's key is
// CreateIdentity's job, and `gage init` calls that first and hands the
// resulting public key in here like any other recipient.
type CreateSpec struct {
	// Name is the vault's name, recorded in .gage/config.toml. It does
	// not have to match Path's base name.
	Name string
	// Path is the vault's target directory. It must not exist, or must
	// exist and be empty — Create never writes into a non-empty
	// directory.
	Path string
	// Type is the vault's backing store. Validated against
	// AllowedTypes.
	Type string
	// Method is this vault's default identity method — a suggestion for
	// devices joining, not a constraint on any of them. Validated
	// against AllowedMethods. See "Decryption methods are per-device".
	Method string
	// Device names the first recipient (see Recipients below) as this
	// device's own. Validated against devicename.Valid.
	Device string
	// Recipients are already-generated age public keys, in the order
	// they should appear in .age-recipients and .gage/config.toml. Must
	// contain at least one, and the first must be this device's own —
	// that is what Device labels. Any beyond it — extra recipients such
	// as a recovery key, added via repeated --recipient — get a generic
	// "recipient-N" label, since there is no way to learn a real device
	// name for a bare public key handed in on the command line. Real
	// per-recipient device naming arrives with M9's `recipient add`.
	Recipients []string
	// Remote is an optional git remote ("origin") URL. Empty means the
	// vault starts local-only; see "Git-specific commands".
	Remote string
}

// Create lays out a brand-new vault's on-disk skeleton: the directory
// itself, .gage/config.toml, .age-recipients, .gitignore, .gitattributes
// (marking the two recipient-defining files unmergeable — see "On-disk
// layout"), a git init, and one commit containing all of it. It performs
// no encryption and takes no Identity — the vault it returns isn't
// unlocked, only created.
//
// Create validates every field itself rather than trusting a caller to
// have done so already: it's a library entry point a GUI could call
// directly, without going through cmd/gage's own flag validation.
func Create(spec CreateSpec) (*Vault, error) {
	if spec.Name == "" {
		return nil, exitcode.New(exitcode.Usage, "gage: vault name is required")
	}
	if !contains(AllowedTypes(), spec.Type) {
		return nil, exitcode.Newf(exitcode.Usage, "gage: unknown vault type %q; accepted: %v", spec.Type, AllowedTypes())
	}
	if !contains(AllowedMethods(), spec.Method) {
		return nil, exitcode.Newf(exitcode.Usage, "gage: unknown method %q; accepted: %v", spec.Method, AllowedMethods())
	}
	if !devicename.Valid(spec.Device) {
		return nil, exitcode.Newf(exitcode.Usage, "gage: device name %q is invalid", spec.Device)
	}
	if len(spec.Recipients) == 0 {
		return nil, exitcode.New(exitcode.Usage, "gage: a vault needs at least one recipient; the first must be this device's own public key")
	}
	for _, r := range spec.Recipients {
		if err := agekey.ValidateRecipient(r); err != nil {
			return nil, exitcode.Wrap(exitcode.Usage, err)
		}
	}
	if spec.Path == "" {
		return nil, exitcode.New(exitcode.Usage, "gage: target path is required")
	}

	if err := requireEmptyOrAbsent(spec.Path); err != nil {
		return nil, err
	}

	if err := writeSkeleton(spec); err != nil {
		return nil, err
	}

	if _, err := gitrepo.InitAndCommit(spec.Path, initialRecipientCommitMessage); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}

	if spec.Remote != "" {
		if err := gitrepo.SetRemote(spec.Path, spec.Remote); err != nil {
			return nil, exitcode.Wrap(exitcode.Internal, err)
		}
	}

	return &Vault{Name: spec.Name, Path: spec.Path}, nil
}

// requireEmptyOrAbsent fails if path exists and is non-empty, or exists
// and isn't a directory — Create must never write into a non-empty,
// non-gage directory. It touches no files itself; the check runs before
// any write.
func requireEmptyOrAbsent(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	if !info.IsDir() {
		return exitcode.Newf(exitcode.Conflict, "gage: %s already exists and is not a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	if len(entries) > 0 {
		return exitcode.Newf(exitcode.Conflict, "gage: %s already exists and is not empty", path)
	}
	return nil
}

// writeSkeleton creates spec.Path and every file Create promises, but
// none of the git plumbing — that's InitAndCommit's job, called
// separately so the two concerns (what's on disk vs. how it's
// versioned) stay easy to reason about independently.
func writeSkeleton(spec CreateSpec) error {
	// entries/ holds ciphertext but its *listing* still leaks how many
	// secrets exist and when each was last touched, so it gets the same
	// 0700 as $GAGE_DATA/identities/ (see identity.go) rather than the
	// more permissive 0750 the rest of the vault's committed config uses.
	if err := os.MkdirAll(filepath.Join(spec.Path, "entries"), 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}
	if err := os.MkdirAll(filepath.Join(spec.Path, ".gage"), 0o750); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	vf := vaultconfig.File{
		Vault: vaultconfig.VaultMeta{
			Name:          spec.Name,
			Type:          spec.Type,
			FormatVersion: vaultconfig.CurrentFormatVersion,
			Created:       time.Now().UTC().Format("2006-01-02"),
		},
		Method:     vaultconfig.Method{Default: spec.Method},
		Recipients: buildRecipients(spec.Device, spec.Recipients),
	}
	if err := vaultconfig.Write(filepath.Join(spec.Path, ".gage", "config.toml"), vf); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	if err := recipients.Write(filepath.Join(spec.Path, ".age-recipients"), spec.Recipients); err != nil {
		return exitcode.Wrap(exitcode.Internal, err)
	}

	if err := writePlainFile(filepath.Join(spec.Path, ".gitignore"), gitignoreContent); err != nil {
		return err
	}
	if err := writePlainFile(filepath.Join(spec.Path, ".gitattributes"), gitattributesContent); err != nil {
		return err
	}

	return nil
}

// gitignoreContent is the standard OS-cruft list — nothing
// gage-specific, since nothing gage itself writes into a vault's
// working tree isn't meant to be committed. See "On-disk layout"'s
// ".gitignore has deliberately little to do."
const gitignoreContent = ".DS_Store\nThumbs.db\n"

// gitattributesContent forces a real conflict on any divergence in
// either recipient-defining file, rather than letting git silently
// line-merge a recipient list neither device actually wrote. See
// "On-disk layout" and Q-SYNC-CONFLICT.
const gitattributesContent = ".age-recipients -merge\n.gage/config.toml -merge\n"

func writePlainFile(path, content string) error {
	if err := atomicfile.WriteFile(path, []byte(content), 0o644); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing %s: %w", path, err))
	}
	return nil
}

// buildRecipients labels the first key as device (this vault's
// initializing device) and every key after it with a generic,
// position-based label — see CreateSpec.Recipients for why M1 can't do
// better than that for extra keys handed in as bare strings.
func buildRecipients(device string, pubkeys []string) []vaultconfig.Recipient {
	out := make([]vaultconfig.Recipient, len(pubkeys))
	for i, pk := range pubkeys {
		label := device
		if i > 0 {
			label = fmt.Sprintf("recipient-%d", i+1)
		}
		out[i] = vaultconfig.Recipient{Device: label, Pubkey: pk}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
