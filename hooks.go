package hglib

// Mercurial has no hooks directory. Hooks are values of the [hooks] section
// of hgrc: "changegroup = <command>", "pretxnchangegroup = <command>", and so
// on. These methods expose that section with the same shape as gitlib's hook
// helpers.
//
// A hook name may carry a suffix (for example "changegroup.ci"); Mercurial
// treats the whole key as the name of the hook.

// GetAllSetHooksName returns every hook configured in [hooks].
func (r *LocalHgRepository) GetAllSetHooksName() ([]string, error) {
	sec, ok := r.Config.GetSection("hooks")
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

// GetHook returns the command configured for a hook, or "" when the hook is
// not set.
func (r *LocalHgRepository) GetHook(name string) (string, error) {
	if r.Config == nil {
		return "", nil
	}
	v, ok := r.Config.Get("hooks", name)
	if !ok {
		return "", nil
	}
	return v, nil
}

// SaveHook writes a hook into the repository hgrc.
func (r *LocalHgRepository) SaveHook(name string, content string) error {
	path, err := hgrcPathFor(r)
	if err != nil {
		return err
	}
	if err := SetConfigValue(path, "hooks", name, content); err != nil {
		return err
	}
	return r.ReloadConfig()
}

// DeleteHook removes a hook from the repository hgrc.
func (r *LocalHgRepository) DeleteHook(name string) error {
	path, err := hgrcPathFor(r)
	if err != nil {
		return err
	}
	if err := UnsetConfigValue(path, "hooks", name); err != nil {
		return err
	}
	return r.ReloadConfig()
}
