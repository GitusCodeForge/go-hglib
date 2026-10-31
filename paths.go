package hglib

// Mercurial's equivalent of a Git remote is a named path in the [paths]
// section of hgrc. These helpers expose that section and perform pulls.

// GetAllRemote returns the names of the [paths] entries.
func (r *LocalHgRepository) GetAllRemote() ([]string, error) {
	sec, ok := r.Config.GetSection("paths")
	if !ok {
		return []string{}, nil
	}
	res := make([]string, 0, len(sec))
	for name := range sec {
		res = append(res, name)
	}
	sortStrings(res)
	return res, nil
}

// HasRemote reports whether [paths] contains name.
func (r *LocalHgRepository) HasRemote(name string) (bool, error) {
	if r.Config == nil {
		return false, nil
	}
	_, ok := r.Config.Get("paths", name)
	return ok, nil
}

// GetRemoteURL returns the URL or filesystem path of a [paths] entry.
func (r *LocalHgRepository) GetRemoteURL(name string) (string, error) {
	if r.Config == nil {
		return "", nil
	}
	v, _ := r.Config.Get("paths", name)
	return v, nil
}

// FetchRemote runs hg pull for a named path and refreshes the handle. Mercurial
// updates the repository store and its bookmarks; the working directory is not
// changed.
func (r *LocalHgRepository) FetchRemote(name string) error {
	url, err := r.GetRemoteURL(name)
	if err != nil {
		return err
	}
	if url == "" {
		// An empty path value means the default path, which hg pull uses when
		// no source is given.
		if _, err := runHgCommand(r.RootPath, "pull"); err != nil {
			return err
		}
	} else {
		if _, err := runHgCommand(r.RootPath, "pull", "--", url); err != nil {
			return err
		}
	}
	return r.Refresh()
}
