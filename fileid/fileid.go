// Package fileid is the opaque file-identity registry that lets the
// hunk-revert feature (PRD: prd-hunk-revert.md) keep marks attached to
// a logical file across renames. A FileID is allocated the first time
// a path is resolved and persists across subsequent rename events, so
// a mark recorded on "foo.txt" stays attached to the same FileID even
// after that file becomes "bar.txt" mid-history.
//
// The package is TUI-agnostic and does no IO. It is the identity layer
// that sits between gitcmd's rename detection (the source of rename
// events) and revertstate (the consumer that keys marks by FileID).
package fileid

// FileID is the opaque identifier the registry hands out for a logical
// file. Zero is reserved for "not allocated"; valid IDs are positive.
type FileID uint64

// Registry maps paths to FileIDs and tracks rename history so that a
// FileID survives any number of renames and can still be looked up by
// any of its prior path-names. The zero value is not ready for use;
// call New.
type Registry struct {
	next     FileID
	pathToID map[string]FileID
	idToPath map[FileID]string
}

// New constructs an empty Registry.
func New() *Registry {
	return &Registry{
		pathToID: map[string]FileID{},
		idToPath: map[FileID]string{},
	}
}

// Resolve returns the FileID for path, allocating a fresh one on the
// first call and returning the existing ID on subsequent calls. An
// empty path returns the zero FileID and is otherwise ignored.
func (r *Registry) Resolve(path string) FileID {
	if path == "" {
		return 0
	}
	if id, ok := r.pathToID[path]; ok {
		return id
	}
	r.next++
	id := r.next
	r.pathToID[path] = id
	r.idToPath[id] = path
	return id
}

// Path returns the current path for id, or "" if id is not allocated.
// "Current" means the most recent path the ID has been assigned via
// Resolve or ApplyRename.
func (r *Registry) Path(id FileID) string {
	return r.idToPath[id]
}

// ApplyRename records that old was renamed to new. The FileID currently
// at old (allocating one if necessary) becomes the FileID at new. The
// old path remains resolvable to the same ID so that historical marks
// keyed by the old path can still be looked up.
//
// If new already has its own FileID that differs from old's, the two
// FileIDs are merged: every path previously mapped to new's ID is
// reassigned to old's, and new's ID is retired. This matches the PRD's
// rename-collision semantics where two paths that converge in history
// should share one identity.
func (r *Registry) ApplyRename(old, new string) {
	if old == "" || new == "" || old == new {
		return
	}
	id, ok := r.pathToID[old]
	if !ok {
		r.next++
		id = r.next
		r.pathToID[old] = id
	}
	if existing, has := r.pathToID[new]; has && existing != id {
		for p, pid := range r.pathToID {
			if pid == existing {
				r.pathToID[p] = id
			}
		}
		delete(r.idToPath, existing)
	}
	r.pathToID[new] = id
	r.idToPath[id] = new
}

// UndoRename is the inverse of ApplyRename: the FileID currently at new
// is moved back to old, and new's mapping is cleared. Used when an
// in-flight rebase drops or reorders the commit that introduced a
// rename, so the registry's view of "current path" stays consistent
// with the rewritten history.
//
// If new has no FileID, UndoRename is a no-op. If old was never the
// path for this FileID, UndoRename still installs the requested
// mapping — the caller is the authority on what the rewritten history
// looks like.
func (r *Registry) UndoRename(new, old string) {
	if old == "" || new == "" || old == new {
		return
	}
	id, ok := r.pathToID[new]
	if !ok {
		return
	}
	delete(r.pathToID, new)
	r.pathToID[old] = id
	r.idToPath[id] = old
}

// Event is one entry in the rename/touch sequence consumed by Seed.
// A pure rename has both OldPath and Path set; an add or modify has
// OldPath empty and Path set to the touched path. A deletion is
// represented the same way as a modify (Path set, OldPath empty);
// the registry does not distinguish, since it never forgets IDs.
type Event struct {
	OldPath string
	Path    string
}

// Seed walks events in order and replays them against the registry:
// renames call ApplyRename, plain touches call Resolve. Used at log
// load time to fold the already-detected rename relationships from
// `git diff -M` / NumStat into the registry so that any path the user
// has seen in any commit resolves to the same FileID it would have
// resolved to during walk-time.
//
// Events with both fields empty are skipped.
func (r *Registry) Seed(events []Event) {
	for _, e := range events {
		switch {
		case e.OldPath != "" && e.Path != "":
			r.ApplyRename(e.OldPath, e.Path)
		case e.Path != "":
			r.Resolve(e.Path)
		}
	}
}
