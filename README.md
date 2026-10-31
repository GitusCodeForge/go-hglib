# go-hglib

`go-hglib` reads Mercurial repositories directly from their on-disk files. It
is the Mercurial counterpart of the `pkg/gitlib` package in
[Gitus](https://github.com/GitusCodeForge/Gitus): the changelog, manifests,
filelogs and revlog indexes are parsed in Go, so browsing history does not
spawn one `hg` process per object.

Operations that are not a storage format are delegated to the `hg`
executable, just as `gitlib` delegates diffing, blame and merging to `git`.

## Quick start

```go
import "github.com/GitusCodeForge/go-hglib"

repo, err := hglib.NewLocalHgRepository("/srv/hg/example")
if err != nil {
    log.Fatal(err)
}
defer repo.Close()

tip, err := repo.GetTip()
if err != nil {
    log.Fatal(err)
}
fmt.Println("tip:", tip.Id, tip.CommitMessage)

// Named branches, bookmarks and tags.
branches, err := repo.GetAllBranchList()
bookmarks, err := repo.GetAllBookmarkList()
tags, err := repo.GetAllTagList()
fmt.Println(branches, bookmarks, tags)

// A directory listing is a view over the flat manifest.
tree, err := repo.GetTree(tip, "src")
for _, item := range tree.ObjectList {
    fmt.Println(item.Name, item.Mode, item.IsDir)
}

// Read a file at a changeset.
blob, err := repo.GetFile(tip, "src/main.go")
fmt.Println(string(blob.Data))

// Or walk from a tree.
obj, err := repo.ResolveTreePath(tree, "sub/file.txt")
fmt.Println(obj.ObjectId())
```

### History and last-commit listings

```go
history, err := repo.GetCommitHistoryN("tip", 50)
fmt.Println(len(history))

// For every direct entry of a directory, the newest changeset that touched
// it. This is one walk of the history, not one hg process per entry.
last, err := repo.ResolveLastCommitAtDir(tip, "")
for name, node := range last {
    fmt.Println(name, node)
}
```

### Diffs and annotation

```go
diff, err := repo.GetDiffRev(tip.Rev)          // hg diff --git -c <rev>
blame, err := repo.Blame(tip, "src/main.go")   // hg annotate -T json
```

### Committing from a web interface

Mercurial cannot commit into a store without a working directory. The
`AddFileToRepo*` helpers make a temporary clone at the branch head, commit
there and push the result back. The repository's own working directory is not
touched.

```go
node, err := repo.AddFileToRepoString(
    "default", "README.md",
    "Jane Doe", "jane@example.com",
    "Jane Doe", "jane@example.com",
    "add README",
    "# Hello\n",
)
```

Merging works the same way:

```go
check, err := repo.CheckBranchMergeConflict("feature", "default")
if check.Successful {
    err = repo.Merge("feature", "default", "Jane Doe", "jane@example.com")
}
```

## What is implemented

* Repository discovery from a working directory or a `.hg` directory.
* Modern store layout: `share-safe`, `store`, `fncache`, `dotencode`,
  `sharedpath`.
* Revlog version 0 (the pre-2006 format, 76-byte entries with node-id parents)
  and version 1 indexes, both inline (`00manifest.i` usually) and split
  index/data files (`.i` / `.d`).
* Revlog version 2 (`0xDEAD`) and changelog version 2 (`0xD34D`): docket
  files, UUID-named `.idx` / `.dat` / `.sda` files, 96-byte index entries and
  the plain / default / inline chunk compression modes.
* `fileindex-v1` enumeration, including paths whose store name is hashed.
* The experimental `plain-encode` store layout.
* `generaldelta` and non-`generaldelta` delta chains. Mercurial's own patch
  format is applied directly, so no `hg` process is needed to reconstruct a
  revision.
* Chunk compression: zstd (the current default), zlib, bzip2 and the
  uncompressed `u` / NUL-header forms.
* Changelog entries: manifest node, user, date, extra metadata, named branch,
  changed files, description.
* Manifests: flat path-to-filelog-node listings, `x` / `l` / `t` flags and
  directory views.
* Filelogs, including the `\1\n` copy-metadata header and copy source/rev.
* Tags from `.hgtags` and `.hg/localtags`. The `.hgtags` used is the one at the
  tip, walking first parents if that changeset does not contain the file;
  Mercurial has no single server-side tag view, so tags that only exist on an
  unmerged side branch are not merged in.
* Bookmarks from `.hg/bookmarks`.
* Named branch heads, using Mercurial's `branch2` cache when it is current and
  scanning the changelog otherwise.
* `dirstate` version 1 (working directory parents and entries).
* hgrc parsing, including `%include`, `%unset` and line continuations, plus
  atomic single-key updates.
* Revision hashing (`hashrevisionsha1`), including the sorted-parent rule.
* `ResolveLastCommitAtDir` memoization, in memory and under
  `.hg/hglib/lastcommit` so a restarted process does not walk the history
  again. `ClearLastCommitStore` removes the persistent entries.

## Not implemented

* `sidedata` contents. The `.sda` file and its offset/length fields are
  recognised, but the payload is not parsed. This mainly matters for the
  experimental `exp-copies-sidedata-changeset` feature, where copy
  source/rev information lives in sidedata instead of the filelog header;
  `BlobObject.CopySource` can therefore be empty for such repositories.
  Version 2 itself is experimental: Mercurial's own help calls it
  "in-development" and enabling it requires a configuration value literally
  named `enable-unstable-format-and-corrupt-my-data`. Unknown requirement
  sub-versions are rejected rather than guessed at.
* `dirstate` version 2 (`dirstate-v2`).
* Mercurial's wire protocol (bundle2, `getbundle`, …). This package is for
  local storage.
* Narrow repositories are read from `data/` with a `meta/` fallback, but the
  narrow manifest format itself is not implemented.
* Git-style remotes. Mercurial named paths are ordinary configuration; use
  `hg pull`/`hg push` directly if you need them.

## API map from gitlib

| gitlib | go-hglib |
| --- | --- |
| `LocalGitRepository` | `LocalHgRepository` |
| `NewLocalGitRepository` | `NewLocalHgRepository` / `InitLocalHgRepository` |
| `CommitObject` | `CommitObject` (a changeset; `TreeObjId` is the manifest node) |
| `TreeObject` | `TreeObject` (a directory view over the flat manifest) |
| `BlobObject` | `BlobObject` (a filelog revision) |
| `Branch` | `Branch` (named branch; may have several heads) |
| `Tag` | `Tag` (from `.hgtags` / `.hg/localtags`) |
| — | `Bookmark` |
| `GetAllBranchList` | `GetAllBranchList` |
| `GetAllTagList` | `GetAllTagList` |
| `GetCommitHistoryN` | `GetCommitHistoryN` |
| `ReadObject` | `GetCommit`, `GetManifest`, `GetFile`, `ReadBlob` |
| `ResolveTreePath` | `ResolveTreePath` |
| `GetDiff` | `GetDiff` / `GetDiffRev` |
| `Blame` | `Blame` / `BlameAtRev` |
| `ResolveLastCommitAtDir` | `ResolveLastCommitAtDir` |
| `AddFileToRepoString` | `AddFileToRepoString` |
| `AddFileToRepoReader` | `AddFileToRepoReader` |
| `AddMultipleFileToRepoString` | `AddMultipleFileToRepoString` |
| `Merge` | `Merge` (branch names, not a remote) |
| `CheckBranchMergeConflict` | `CheckBranchMergeConflict` (two branch names) |
| hooks directory | `[hooks]` in hgrc (`GetHook`, `SaveHook`, `DeleteHook`) |

Unlike Git, Mercurial has no global object store: a node id alone does not say
whether it names a changeset, a manifest or a file revision. The API therefore
asks for the revlog explicitly (`GetCommit`, `GetManifest`, `GetFile`) instead
of offering one `ReadObject`.

## Refreshing a long-lived handle

`LocalHgRepository` keeps revlog indexes in memory. After another process
pushes into the repository, call `Refresh` to reopen the changelog and
manifest:

```go
if err := repo.Refresh(); err != nil { ... }
```

Existing revision numbers keep their meaning; new revisions become visible.

## Tests

The tests create real repositories with the `hg` executable and compare the
parsed result against `hg log`, `hg manifest --debug`, `hg cat`, `hg diff`,
`hg annotate` and `hg resolve`. The suite also creates version 2 and
changelog-v2 repositories with Mercurial's experimental configurations. On
builds without the Rust file-index extension, those repositories require
`storage.fileindex.slow-path=allow`; the `hg` helpers in this package add that
config automatically, and the tests do the same for their own `hg` calls.

```sh
go test ./...
```

If your `/tmp` is mounted `noexec` (common on hardened systems), point Go's
temporary directory at a directory that allows execution:

```sh
mkdir -p .gotmp
GOTMPDIR=$PWD/.gotmp go test ./...
```
