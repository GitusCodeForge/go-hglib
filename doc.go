// Package hglib reads Mercurial repositories.
//
// The package works directly on the on-disk repository files: revlog
// indexes, the changelog, the manifest and filelogs are parsed without
// asking the hg executable. Operations that Mercurial does not expose as a
// storage format (diffing, annotation, committing into a repository) are
// delegated to the hg executable, in the same spirit as gitlib delegating
// those operations to git.
package hglib
