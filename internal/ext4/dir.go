package ext4

import (
	"fmt"
	"path"
	"strings"
)

// ReadDirNoFollow lists the names in the directory p ("." and ".." left
// out), at most max of them. It never follows a symbolic link: a component
// of p (the last one too) that is a link, a file or anything but a directory
// is an error. found is false, with no error, when a component of p is not
// in its directory. It only reads (uc-pxx-step2 reads the hold folder of the
// running root file system with it, beneath a bind mount: MAINLINE-RC2-LS
// review A-1).
func (fs *FS) ReadDirNoFollow(p string, max int) (names []string, found bool, err error) {
	cur, err := fs.inode(rootIno)
	if err != nil {
		return nil, false, err
	}
	if cur.mode&modeTypeMask != modeDir {
		return nil, false, fmt.Errorf("the root is not a directory")
	}
	at := "/"
	for _, part := range strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/") {
		if part == "" {
			continue
		}
		ents, err := fs.dirEntries(cur)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %v", at, err)
		}
		ino := uint32(0)
		for _, e := range ents {
			if e.name == part {
				ino = e.ino
				break
			}
		}
		at = path.Join(at, part)
		if ino == 0 {
			return nil, false, nil
		}
		if cur, err = fs.inode(ino); err != nil {
			return nil, false, fmt.Errorf("%s: %v", at, err)
		}
		if t := cur.mode & modeTypeMask; t != modeDir {
			return nil, false, fmt.Errorf("%s is not a directory (%s)", at, typeName(t))
		}
	}
	ents, err := fs.dirEntries(cur)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %v", at, err)
	}
	if len(ents) > max {
		return nil, false, fmt.Errorf("%s: more than %d entries", at, max)
	}
	for _, e := range ents {
		names = append(names, e.name)
	}
	return names, true, nil
}

func typeName(t uint16) string {
	switch t {
	case modeFile:
		return "a regular file"
	case modeLink:
		return "a symbolic link"
	}
	return fmt.Sprintf("file type %#o", t)
}
