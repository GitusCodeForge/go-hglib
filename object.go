package hglib

import "errors"

var ErrObjectNotFound = errors.New("object not found")

// HgObjectType is the kind of a Mercurial object. Mercurial does not have a
// single global object store like Git: changesets live in the changelog,
// directory listings live in the manifest, and file contents live in
// per-file revlogs. The node id alone therefore does not say which revlog an
// object belongs to.
type HgObjectType int

const (
	INVALID HgObjectType = iota
	CHANGESET
	MANIFEST
	FILERev
)

func (t HgObjectType) String() string {
	switch t {
	case CHANGESET:
		return "CHANGESET"
	case MANIFEST:
		return "MANIFEST"
	case FILERev:
		return "FILERev"
	default:
		return "INVALID"
	}
}

// HgObject is implemented by changesets, manifests and file revisions.
type HgObject interface {
	Type() HgObjectType
	ObjectId() string
	RawData() []byte
}

func IsCommitObject(o HgObject) bool {
	_, ok := o.(*CommitObject)
	return ok
}

func IsManifestObject(o HgObject) bool {
	_, ok := o.(*ManifestObject)
	return ok
}

func IsBlobObject(o HgObject) bool {
	_, ok := o.(*BlobObject)
	return ok
}
