// Package manifests embeds the firmware manifests into the installer binary.
// A manifest lists the sha256 of every partition image of one official
// firmware. The installer writes an image only when its sha256 is listed
// here under the same partition name. Build a manifest with
// tools/fw-manifest.py. A sha256 is a fingerprint, not a key.
package manifests

import "embed"

//go:embed *.json
var FS embed.FS
