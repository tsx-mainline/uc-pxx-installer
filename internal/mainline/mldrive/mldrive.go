// Package mldrive reads a mainline bundle (package mainline) from the
// UC-PXX-INST drive, an opened FAT volume (package fatfs), in place, and gives
// the identity of a bundle folder on a file system. It only reads.
package mldrive

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/mainline"
)

// Ident is the identity of each file of a DirSource bundle (lstat: size,
// modification and change time, device, inode), for a plan to bind: the
// form of drivestate.Ident, one per contract file.
func Ident(dir string) (string, error) {
	var parts []string
	for _, n := range []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile} {
		id, err := drivestate.Stat(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		parts = append(parts, n+" "+id.String())
	}
	return strings.Join(parts, "; "), nil
}

type driveSource struct{ fs *fatfs.FS }

// DriveSource is the folder uc-pxx-mainline/ of the UC-PXX-INST drive (an
// opened FAT volume, read in place). The volume must be clean (the dirty
// bits) and pass the read-only check of every directory and chain, as the
// firmware file of the drive does (fatfs.FindFirmware).
func DriveSource(fs *fatfs.FS) (mainline.Source, error) {
	if d := fs.Dirty(); len(d) > 0 {
		return nil, fmt.Errorf("the UC-PXX-INST drive was not ejected cleanly (%s): copy the mainline folder again, then eject the drive", strings.Join(d, "; "))
	}
	if p := fs.Check(); len(p) > 0 {
		return nil, fmt.Errorf("the file system of the UC-PXX-INST drive has errors (%s): copy the mainline folder again", strings.Join(p, "; "))
	}
	return &driveSource{fs: fs}, nil
}

func (d *driveSource) Describe() string {
	return "the folder " + mainline.Dir + " of the UC-PXX-INST drive"
}

func (d *driveSource) Names() (files, others []string, err error) {
	es, err := d.fs.ReadDir("/" + mainline.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("the UC-PXX-INST drive has no readable folder %s: %v", mainline.Dir, err)
	}
	for _, e := range es {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		if e.Dir || e.Attr&0x08 != 0 { // a folder or a volume label
			others = append(others, e.Name)
		} else {
			files = append(files, e.Name)
		}
	}
	return files, others, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func (d *driveSource) Open(name string) (mainline.File, io.Closer, error) {
	if name == "" || strings.ContainsRune(name, '/') {
		return nil, nil, fmt.Errorf("bad name %q", name)
	}
	f, err := d.fs.Open("/" + mainline.Dir + "/" + name)
	if err != nil {
		return nil, nil, err
	}
	return f, nopCloser{}, nil
}

// TopFolder is what the top folder of the UC-PXX-INST drive holds for the
// choice of the install mode: a folder named uc-pxx-mainline (the name
// compared without case, as FAT does), and the names of the firmware files
// (*.bin, fatfs.IsFirmwareName).
type TopFolder struct {
	Bundle bool
	Bins   []string
}

// Top reads the top folder of the drive.
func Top(fs *fatfs.FS) (TopFolder, error) {
	var t TopFolder
	es, err := fs.ReadDir("/")
	if err != nil {
		return t, err
	}
	for _, e := range es {
		switch {
		case e.Dir && strings.EqualFold(e.Name, mainline.Dir):
			t.Bundle = true
		case !e.Dir && e.Attr&0x08 == 0 && fatfs.IsFirmwareName(e.Name):
			t.Bins = append(t.Bins, e.Name)
		}
	}
	return t, nil
}

// TopOfImage opens the drive file read-only (fatfs.OpenImage) and reads its
// top folder.
func TopOfImage(image string) (TopFolder, error) {
	fs, err := fatfs.OpenImage(image)
	if err != nil {
		return TopFolder{}, err
	}
	defer fs.Close()
	return Top(fs)
}
