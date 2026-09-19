package gage

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
	"github.com/distillerylabs/gage/internal/gage/gitrepo"
	"github.com/distillerylabs/gage/internal/gage/recipients"
	"github.com/distillerylabs/gage/internal/gage/vaultconfig"
)

const (
	testRecipient1 = "age1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0savhh7m"
	testRecipient2 = "age1yubikey1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s9hkmc0"
)

func validSpec(t *testing.T, name string) CreateSpec {
	t.Helper()
	return CreateSpec{
		Name:       name,
		ID:         vaultconfig.NewID(),
		Path:       filepath.Join(t.TempDir(), name),
		Type:       TypeGit,
		Method:     MethodPassphrase,
		Device:     "laptop-1",
		Recipients: []string{testRecipient1},
	}
}

func TestCreateWritesFullSkeleton(t *testing.T) {
	spec := validSpec(t, "myvault")
	v, err := Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "myvault" || v.Path != spec.Path {
		t.Errorf("Create returned %+v, want Name=myvault Path=%s", v, spec.Path)
	}

	for _, rel := range []string{
		".gage/config.toml",
		".age-recipients",
		"entries",
		".gitignore",
		".gitattributes",
		".git",
	} {
		if _, err := os.Stat(filepath.Join(spec.Path, rel)); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	count, err := gitrepo.CommitCount(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("commit count = %d, want 1", count)
	}
	clean, err := gitrepo.IsClean(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("working tree not clean after Create")
	}
}

// committedFiles lists the paths actually present in HEAD's tree, so a
// test can assert what was *committed* rather than merely what exists on
// disk. go-git reports tree paths with forward slashes on every
// platform, so the expected values below are the same on Windows.
func committedFiles(t *testing.T, dir string) []string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	if err := tree.Files().ForEach(func(f *object.File) error {
		files = append(files, f.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// TestCreateCommitsTheSkeletonFiles asserts the initial commit's tree
// directly. TestCreateWritesFullSkeleton proves the files exist and the
// tree is clean, which does imply they were committed (go-git counts an
// untracked file as dirty — see gitrepo's own test) — but only via an
// unstated property of a dependency. This asserts it outright.
//
// entries/ is deliberately absent: git cannot track an empty directory,
// so a freshly-created vault commits four files and nothing else.
func TestCreateCommitsTheSkeletonFiles(t *testing.T) {
	spec := validSpec(t, "myvault")
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}

	got := committedFiles(t, spec.Path)
	want := []string{".age-recipients", ".gage/config.toml", ".gitattributes", ".gitignore"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("committed files = %v, want %v", got, want)
	}
}

func TestCreateGitattributesMarksRecipientFilesUnmergeable(t *testing.T) {
	spec := validSpec(t, "myvault")
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(spec.Path, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{".age-recipients -merge", ".gage/config.toml -merge"} {
		if !strings.Contains(content, want) {
			t.Errorf(".gitattributes = %q, want it to contain %q", content, want)
		}
	}
}

func TestCreateGitignoreHasOnlyOSCruft(t *testing.T) {
	spec := validSpec(t, "myvault")
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(spec.Path, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{".DS_Store", "Thumbs.db"} {
		if !strings.Contains(content, want) {
			t.Errorf(".gitignore = %q, want it to contain %q", content, want)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if line != ".DS_Store" && line != "Thumbs.db" {
			t.Errorf(".gitignore has an unexpected line %q; nothing gage-state-specific belongs here", line)
		}
	}
}

func TestCreateVaultConfigRoundTrip(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Recipients = []string{testRecipient1, testRecipient2}
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}

	f, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Vault.Type != TypeGit {
		t.Errorf("Vault.Type = %q, want %q", f.Vault.Type, TypeGit)
	}
	if f.Vault.FormatVersion != vaultconfig.CurrentFormatVersion {
		t.Errorf("Vault.FormatVersion = %d, want %d", f.Vault.FormatVersion, vaultconfig.CurrentFormatVersion)
	}
	if f.Method.Default != MethodPassphrase {
		t.Errorf("Method.Default = %q, want %q", f.Method.Default, MethodPassphrase)
	}
	if len(f.Recipients) != 2 {
		t.Fatalf("len(Recipients) = %d, want 2", len(f.Recipients))
	}
	if f.Recipients[0].Device != spec.Device || f.Recipients[0].Pubkey != testRecipient1 {
		t.Errorf("Recipients[0] = %+v, want device=%s pubkey=%s", f.Recipients[0], spec.Device, testRecipient1)
	}
}

func TestCreateRecipientsMatchAgeRecipientsFile(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Recipients = []string{testRecipient1, testRecipient2}
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}

	f, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	fileKeys, err := recipients.Read(filepath.Join(spec.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fileKeys) != len(f.Recipients) {
		t.Fatalf(".age-recipients has %d keys, .gage/config.toml has %d", len(fileKeys), len(f.Recipients))
	}
	for i, r := range f.Recipients {
		if fileKeys[i] != r.Pubkey {
			t.Errorf("key %d: .age-recipients=%q .gage/config.toml=%q", i, fileKeys[i], r.Pubkey)
		}
	}
}

func TestCreateIntoNonEmptyDirectoryFailsWithoutTouchingFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(existing, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec := validSpec(t, "myvault")
	spec.Path = dir
	if _, err := Create(spec); err == nil {
		t.Fatal("expected Create to fail on a non-empty directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("directory contents changed: %v", entries)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not touch" {
		t.Error("existing file content was modified")
	}
}

// requireNothingCreated asserts Create left no trace at its target path.
// Every rejection below is specified as failing "before creating any
// files", and an exit code alone can't tell a clean rejection apart from
// one that bailed out halfway through writing a vault.
func requireNothingCreated(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Create touched the filesystem at %s despite rejecting the spec (stat err = %v)", path, err)
	}
}

func TestCreateRejectsUnknownType(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Type = "s3"
	_, err := Create(spec)
	if err == nil {
		t.Fatal("expected an error for unknown type")
	}
	if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
	}
	if !strings.Contains(err.Error(), TypeGit) {
		t.Errorf("error doesn't list the accepted value %q: %v", TypeGit, err)
	}
	requireNothingCreated(t, spec.Path)
}

func TestCreateAcceptsExplicitGitType(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Type = TypeGit
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsUnknownMethod(t *testing.T) {
	for _, m := range []string{"ssh", "yubikey", "age-key", "secure-enclave", "plugin:foo", "bogus"} {
		t.Run(m, func(t *testing.T) {
			spec := validSpec(t, "myvault")
			spec.Method = m
			_, err := Create(spec)
			if err == nil {
				t.Fatalf("expected an error for method %q", m)
			}
			if exitcode.CodeOf(err) != exitcode.Usage {
				t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
			}
			if !strings.Contains(err.Error(), MethodPassphrase) {
				t.Errorf("error doesn't list the accepted value %q: %v", MethodPassphrase, err)
			}
			requireNothingCreated(t, spec.Path)
		})
	}
}

func TestCreateAcceptsExplicitPassphraseMethod(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Method = MethodPassphrase
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRequiresAtLeastOneRecipient(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Recipients = nil
	_, err := Create(spec)
	if err == nil {
		t.Fatal("expected an error with no recipients")
	}
	if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error doesn't name the missing flag: %v", err)
	}
	requireNothingCreated(t, spec.Path)
}

func TestCreateRepeatedRecipientsAllWritten(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Recipients = []string{testRecipient1, testRecipient2, testRecipient1}
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	keys, err := recipients.Read(filepath.Join(spec.Path, ".age-recipients"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Errorf("len(keys) = %d, want 3", len(keys))
	}
}

func TestCreateRejectsMalformedRecipient(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Recipients = []string{"not-a-valid-key"}
	_, err := Create(spec)
	if err == nil {
		t.Fatal("expected an error for a malformed recipient")
	}
	if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
	}
	requireNothingCreated(t, spec.Path)
}

func TestCreateRejectsInvalidDeviceName(t *testing.T) {
	for _, bad := range []string{"../../../etc/x", "/etc/passwd", `..\..\windows\x`, "UpperCase", ""} {
		t.Run(bad, func(t *testing.T) {
			spec := validSpec(t, "myvault")
			spec.Device = bad
			_, err := Create(spec)
			if err == nil {
				t.Fatalf("expected an error for device name %q", bad)
			}
			if exitcode.CodeOf(err) != exitcode.Usage {
				t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
			}
			requireNothingCreated(t, spec.Path)
		})
	}
}

func TestCreateDeviceNameLandsInRecipientAndIdentityPath(t *testing.T) {
	spec := validSpec(t, "myvault")
	spec.Device = "custom-device"
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}

	f, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Recipients[0].Device != "custom-device" {
		t.Errorf("Recipients[0].Device = %q, want %q", f.Recipients[0].Device, "custom-device")
	}

	idPath, err := IdentityFilePath(spec.ID, spec.Device)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(idPath, filepath.Join(spec.ID, "custom-device.age")) {
		t.Errorf("IdentityFilePath = %q, want it to end with %s", idPath, filepath.Join(spec.ID, "custom-device.age"))
	}
}

// TestCreateRecordsTheIDItWasHandedRatherThanMintingOne is the half of
// A20's ordering that is invisible from the config schema. `init` mints
// the id *before* it creates the identity, so that the identity is filed
// under the id the vault will carry. If Create minted a second id here
// the vault would still look perfectly fine — and this device's key
// would sit in a directory nothing ever looks up again.
func TestCreateRecordsTheIDItWasHandedRatherThanMintingOne(t *testing.T) {
	spec := validSpec(t, "myvault")
	v, err := Create(spec)
	if err != nil {
		t.Fatal(err)
	}

	f, err := vaultconfig.Read(filepath.Join(spec.Path, ".gage", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Vault.ID != spec.ID {
		t.Errorf("committed [vault].id = %q, want the id Create was handed (%q)", f.Vault.ID, spec.ID)
	}
	if v.ID != spec.ID {
		t.Errorf("Create returned a Vault with ID %q, want %q", v.ID, spec.ID)
	}
	if f.Vault.FormatVersion != 2 {
		t.Errorf("committed format_version = %d, want 2", f.Vault.FormatVersion)
	}
}

// TestCreateRefusesAnIDThatIsNotAUUID keeps Create honest as a library
// entry point a GUI could call directly: it validates every field itself
// rather than trusting cmd/gage to have done so, and the id is the field
// that becomes a path component under $GAGE_DATA.
func TestCreateRefusesAnIDThatIsNotAUUID(t *testing.T) {
	for _, bad := range []string{"", "personal", "../../etc/x", "not-a-uuid"} {
		t.Run(bad, func(t *testing.T) {
			spec := validSpec(t, "myvault")
			spec.ID = bad
			if _, err := Create(spec); err == nil {
				t.Fatalf("Create accepted id %q", bad)
			}
			if _, err := os.Stat(spec.Path); err == nil {
				t.Errorf("Create wrote %s despite refusing the id", spec.Path)
			}
		})
	}
}

func TestCreateWithRemoteSetsOrigin(t *testing.T) {
	remote := "/does/not/need/to/exist/for/set-remote.git"
	spec := validSpec(t, "myvault")
	spec.Remote = remote
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	got, err := gitrepo.RemoteURL(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != remote {
		t.Errorf("RemoteURL = %q, want %q", got, remote)
	}
}

func TestCreateWithoutRemoteLeavesVaultRemoteless(t *testing.T) {
	spec := validSpec(t, "myvault")
	if _, err := Create(spec); err != nil {
		t.Fatal(err)
	}
	got, err := gitrepo.RemoteURL(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("RemoteURL = %q, want empty (no --remote given)", got)
	}
}
