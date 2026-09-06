package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage/gittest"
)

// preparedConflict leaves dir diverged from its remote on seed.txt —
// both sides edited it — with the remote already fetched and the merge
// computed but not applied. This is the seam M8b's resolution lives in:
// PrepareMerge has decided, and nothing has been written.
func preparedConflict(t *testing.T) (dir string, merge *PendingMerge) {
	t.Helper()

	dir, remote := newPublishedRepo(t)
	other := gittest.NewDevice(t, remote)
	other.WriteCommitPush(t, "seed.txt", "their version", "their edit")
	commitFile(t, dir, "seed.txt", "my version")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	merge, err := PrepareMerge(dir)
	if err != nil {
		t.Fatalf("PrepareMerge: %v", err)
	}
	return dir, merge
}

// TestPrepareMergeDecidesWithoutWriting is the half of the split that
// makes interactive resolution possible: a caller can learn what
// conflicted, then take as long as it likes to decide, and a caller that
// never calls Commit leaves the repository exactly as it found it. That
// is also what makes `abort` free — there is no partial state to undo.
func TestPrepareMergeDecidesWithoutWriting(t *testing.T) {
	dir, merge := preparedConflict(t)

	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := merge.Conflicts(); len(got) != 1 || got[0] != "seed.txt" {
		t.Fatalf("Conflicts() = %v, want [seed.txt]", got)
	}

	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) while only preparing a merge", before, after)
	}
	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "my version" {
		t.Errorf("the working tree was modified by PrepareMerge: %q", got)
	}
	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("PrepareMerge left the working tree dirty")
	}
}

// TestPendingMergeContentReadsBothSidesFromTheObjects: the remote's
// version of a conflicted path was never written to disk, so the only
// place to read it is the git object it came in as. Reading the local
// side from the same place rather than from the working tree keeps one
// source of truth for what "local" means.
func TestPendingMergeContentReadsBothSidesFromTheObjects(t *testing.T) {
	dir, merge := preparedConflict(t)

	local, present, err := merge.Content(LocalSide, "seed.txt")
	if err != nil {
		t.Fatalf("Content(LocalSide): %v", err)
	}
	if !present || string(local) != "my version" {
		t.Errorf("local content = %q (present=%v), want %q", local, present, "my version")
	}

	remote, present, err := merge.Content(RemoteSide, "seed.txt")
	if err != nil {
		t.Fatalf("Content(RemoteSide): %v", err)
	}
	if !present || string(remote) != "their version" {
		t.Errorf("remote content = %q (present=%v), want %q", remote, present, "their version")
	}

	// And reading it did not put it anywhere a later command could
	// mistake for a merged result.
	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "my version" {
		t.Errorf("reading the remote's version wrote it to the working tree: %q", got)
	}
}

// TestPendingMergeContentReportsAnAbsentSide is what makes a
// delete/modify conflict presentable as "one side removed this" rather
// than as an error with nothing to show.
func TestPendingMergeContentReportsAnAbsentSide(t *testing.T) {
	dir, remote := newPublishedRepo(t)

	other := gittest.NewDevice(t, remote)
	other.Remove(t, "seed.txt")
	other.Commit(t, "their deletion")
	other.Push(t)
	commitFile(t, dir, "seed.txt", "my edit of the file they deleted")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	merge, err := PrepareMerge(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, present, err := merge.Content(RemoteSide, "seed.txt"); err != nil || present {
		t.Errorf("Content(RemoteSide) present=%v err=%v, want absent and no error — the remote deleted it",
			present, err)
	}
	if _, present, err := merge.Content(LocalSide, "seed.txt"); err != nil || !present {
		t.Errorf("Content(LocalSide) present=%v err=%v, want the surviving version", present, err)
	}
}

// TestCommitRefusesAnUnansweredConflict: a missing choice is refused
// rather than defaulted. Defaulting to the local side would be
// last-write-wins — the outcome the whole sync model exists to refuse —
// and it must not be reachable by a caller that simply forgot a path.
func TestCommitRefusesAnUnansweredConflict(t *testing.T) {
	dir, merge := preparedConflict(t)

	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = merge.Commit("gage: merge origin", nil, nil)
	if err == nil {
		t.Fatal("Commit succeeded with an unanswered conflict, want it refused")
	}
	if !strings.Contains(err.Error(), "seed.txt") {
		t.Errorf("error = %v, want it to name the unanswered path", err)
	}

	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) on a refused Commit", before, after)
	}
}

// TestCommitAppliesEachChoice covers both answers to the same conflict,
// and that either way the result is a real two-parent commit.
func TestCommitAppliesEachChoice(t *testing.T) {
	for _, tc := range []struct {
		name string
		side MergeSide
		want string
	}{
		{"local", LocalSide, "my version"},
		{"remote", RemoteSide, "their version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, merge := preparedConflict(t)

			result, err := merge.Commit("gage: merge origin",
				map[string]MergeSide{"seed.txt": tc.side}, nil)
			if err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if !result.Merged {
				t.Fatalf("Commit returned Merged=false: %+v", result)
			}
			if got := readFile(t, filepath.Join(dir, "seed.txt")); got != tc.want {
				t.Errorf("seed.txt = %q, want %q", got, tc.want)
			}

			clean, err := IsClean(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !clean {
				t.Error("the merge commit left the working tree dirty")
			}
		})
	}
}

// TestCommitCreatesTheAddsItIsGiven: `keep both` writes the losing
// version of an entry under a fresh name, and it becomes part of the
// merge commit rather than a separate write the caller would then have
// to remember to undo.
func TestCommitCreatesTheAddsItIsGiven(t *testing.T) {
	dir, merge := preparedConflict(t)

	adds := map[string][]byte{"entries/new.age": []byte("the losing version")}
	if _, err := merge.Commit("gage: merge origin",
		map[string]MergeSide{"seed.txt": LocalSide}, adds); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := readFile(t, filepath.Join(dir, "entries", "new.age")); got != "the losing version" {
		t.Errorf("the added file = %q, want %q", got, "the losing version")
	}
	// Committed, not merely written: a file left untracked would leave
	// every later sync refusing over a dirty tree.
	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Error("an added file was written but not committed with the merge")
	}
}

// TestCommitRefusesAnAddThatWouldOverwrite: adds create files, they never
// replace one. A caller handing over a path that already exists has lost
// track of what it is doing, and silently overwriting an entry is the
// one thing this whole path exists to avoid.
func TestCommitRefusesAnAddThatWouldOverwrite(t *testing.T) {
	dir, merge := preparedConflict(t)

	adds := map[string][]byte{"seed.txt": []byte("clobber")}
	if _, err := merge.Commit("gage: merge origin",
		map[string]MergeSide{"seed.txt": LocalSide}, adds); err == nil {
		t.Fatal("Commit overwrote an existing file through adds, want it refused")
	}

	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "my version" {
		t.Errorf("seed.txt = %q, want it untouched by the refused add", got)
	}
}

// TestMergeCreatedFilesAre0600: git records every blob it stores here as
// 0644, and an entry file gage writes itself is 0600. How a file arrived
// must not decide who on the machine can read it, so both kinds of file
// a merge can *create* — one of Commit's adds, and one the remote added
// that this device has never seen — take gage's mode rather than git's.
func TestMergeCreatedFilesAre0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits aren't modeled on Windows")
	}

	dir, remote := newPublishedRepo(t)
	other := gittest.NewDevice(t, remote)
	other.Write(t, "seed.txt", "their version")
	other.Write(t, "entries/theirs.age", "an entry this device has never seen")
	other.Commit(t, "their edit and a new entry")
	other.Push(t)
	commitFile(t, dir, "seed.txt", "my version")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	merge, err := PrepareMerge(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := merge.Commit("gage: merge origin",
		map[string]MergeSide{"seed.txt": LocalSide},
		map[string][]byte{"entries/mine.age": []byte("the losing version")}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	for _, path := range []string{"entries/theirs.age", "entries/mine.age"} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != mergedFileMode {
			t.Errorf("%s mode = %04o, want %04o", path, perm, mergedFileMode)
		}
	}
}

// TestCommitRollsBackAPartialFailure: a merge that fails halfway through
// writing itself must leave neither the old tree nor the new one behind.
// Every later sync operation refuses over a dirty tree, so a half-applied
// merge would wedge the vault until a human cleaned it up by hand.
//
// The failure is forced by asking for an add underneath a path that is a
// *file*, which no directory can be created inside — a deterministic
// write failure on every platform, reached only after the remote's own
// new file has already been applied.
func TestCommitRollsBackAPartialFailure(t *testing.T) {
	dir, remote := newPublishedRepo(t)
	other := gittest.NewDevice(t, remote)
	// seed.txt is a *tracked* file only the remote changed, so applying
	// the merge overwrites it in place — the half of a partial merge the
	// hard reset is what restores. theirs.txt is the untracked half, the
	// one the rollback has to remove by name.
	other.Write(t, "seed.txt", "their version")
	other.Write(t, "theirs.txt", "only they touched this")
	other.Commit(t, "their edit and their new file")
	other.Push(t)
	commitFile(t, dir, "blocker", "a file, not a directory")
	if _, err := Fetch(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	merge, err := PrepareMerge(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(merge.Conflicts()) != 0 {
		t.Fatalf("Conflicts() = %v, want none — this merge fails while applying, not while deciding",
			merge.Conflicts())
	}

	before, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = merge.Commit("gage: merge origin", nil,
		map[string][]byte{"blocker/impossible.age": []byte("nowhere to put this")})
	if err == nil {
		t.Fatal("Commit succeeded writing under a path that is a file, want it to fail")
	}

	after, err := HeadHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("HEAD moved (%s -> %s) on a merge that failed to apply", before, after)
	}
	// Both halves of the partial merge are undone: the file it created,
	// removed by name, and the tracked file it overwrote, restored by the
	// reset. The reset has to happen even though removing the add's own
	// path fails — that path is under a file, so it cannot even be
	// statted — or a merge that failed this way would leave the remote's
	// version of a tracked file sitting in the tree.
	if _, err := os.Stat(filepath.Join(dir, "theirs.txt")); !os.IsNotExist(err) {
		t.Errorf("a failed merge left the file it created behind (stat err = %v)", err)
	}
	if got := readFile(t, filepath.Join(dir, "seed.txt")); got != "seed" {
		t.Errorf("seed.txt = %q, want it restored to %q — the rollback skipped its hard reset", got, "seed")
	}
	clean, err := IsClean(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		paths, _ := DirtyPaths(dir)
		t.Errorf("a failed merge left the working tree dirty: %v", paths)
	}
}

// TestMergeRemoteAndPrepareMergeCannotDrift: MergeRemote is now
// PrepareMerge plus Commit, so M8a's automatic path and M8b's
// interactive one decide identically by construction. This pins that
// they still agree about what conflicts.
func TestMergeRemoteAndPrepareMergeCannotDrift(t *testing.T) {
	dir, merge := preparedConflict(t)

	prepared := merge.Conflicts()
	result, err := MergeRemote(dir, "gage: merge origin")
	if err != nil {
		t.Fatalf("MergeRemote: %v", err)
	}
	if result.Merged {
		t.Fatal("MergeRemote merged a conflicting divergence")
	}
	if len(result.Conflicts) != len(prepared) {
		t.Fatalf("MergeRemote reported %v, PrepareMerge reported %v", result.Conflicts, prepared)
	}
	for i := range prepared {
		if result.Conflicts[i] != prepared[i] {
			t.Errorf("conflict %d: MergeRemote says %q, PrepareMerge says %q",
				i, result.Conflicts[i], prepared[i])
		}
	}
}
