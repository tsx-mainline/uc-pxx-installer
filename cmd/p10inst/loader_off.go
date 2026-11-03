//go:build install

package main

import (
	"errors"

	"uc-pxx-installer/internal/ui"
)

// The install build has no restart into the download mode (loader.go): the
// status UI of an install image shows no "Download mode" page.

func loaderPage(string) *ui.LoaderPage { return nil }

func loaderFromUI(string) error {
	return errors.New("the install build has no restart into the download mode")
}
