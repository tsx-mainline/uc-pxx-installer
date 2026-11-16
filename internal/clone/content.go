package clone

// LiveContent is the content check of a running root: the content manifest
// of the tree under root (the live root "/" of the unit, or a test root),
// one file system only (a file system mounted on a kept path refuses; a
// mount on an excluded path is not entered), with the exclusion list
// exclude. It reads only. Step 2's pass 1 of a clone (slot B's live root
// against ref_b) and uc-pxx-provision (the end of provisioning: B against
// ref_b) both run it, so the two checks are one code (DESIGN-R23 §4.6).
func LiveContent(root string, exclude []string) (*Result, error) {
	x, err := NewExcluder(exclude)
	if err != nil {
		return nil, err
	}
	src, err := NewOSSource(root)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	return Build(src, x, Options{OneFS: true})
}
