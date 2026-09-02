package pkgs

import "sort"

// Disagreement is what a lockfile claims versus what is on disk.
//
// These are not the same question and they routinely give different answers.
// A lockfile lists everything that *could* be needed — including packages for
// platforms you are not on — while node_modules holds what was actually
// installed. Most tools pick one silently. Reporting the gap is more useful
// than picking a side.
type Disagreement struct {
	// LockedCount is how many packages the lockfile names.
	LockedCount int
	// InstalledCount is how many are actually present in node_modules.
	InstalledCount int
	// LockedNotInstalled are named in the lockfile but absent on disk. Usually
	// optional or platform-specific dependencies, and usually harmless.
	LockedNotInstalled []string
	// InstalledNotLocked are on disk but not in the lockfile. These matter
	// more: something installed them outside the lockfile's knowledge.
	InstalledNotLocked []string
}

// Compare loads both sources for a repo and reports where they differ.
//
// Returns a nil Disagreement when there is no lockfile to compare against,
// which is not an error — plenty of repos have none.
func Compare(root string, locked *Graph) (*Disagreement, error) {
	onDisk := newGraph("node_modules")
	if err := walkNodeModules(root, onDisk); err != nil {
		return nil, err
	}

	d := &Disagreement{
		LockedCount:    len(locked.Packages),
		InstalledCount: len(onDisk.Packages),
	}
	for name := range locked.Packages {
		if _, ok := onDisk.Packages[name]; !ok {
			d.LockedNotInstalled = append(d.LockedNotInstalled, name)
		}
	}
	for name := range onDisk.Packages {
		if _, ok := locked.Packages[name]; !ok {
			d.InstalledNotLocked = append(d.InstalledNotLocked, name)
		}
	}
	sort.Strings(d.LockedNotInstalled)
	sort.Strings(d.InstalledNotLocked)
	return d, nil
}
