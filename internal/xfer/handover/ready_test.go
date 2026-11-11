package handover

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline/mltest"
)

func hexsum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ready writes a hand-over in a new folder: the record and the three files.
func ready(t *testing.T) (dir string, man []byte, pin string) {
	t.Helper()
	res := mltest.Fill(77, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	b := mltest.NewBundle("tok-handover", 9000, 1<<20, ch, res, 38029312)
	man = b.ManifestJSON()
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, ReadyDir), 0o700)
	for n, d := range map[string][]byte{ManifestName: man, TemplateName: b.Template, RootfsName: b.Rootfs} {
		os.WriteFile(filepath.Join(dir, ReadyDir, n), d, 0o600)
	}
	rec := Record{Version: RecordVersion, ManifestSHA256: hexsum(man), Files: []ReadyFile{{ManifestName, int64(len(man)), hexsum(man)},
		{TemplateName, int64(len(b.Template)), hexsum(b.Template)}, {RootfsName, int64(len(b.Rootfs)), hexsum(b.Rootfs)}}}
	j, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, ReadyRecord), j, 0o600)
	return dir, man, hexsum(man)
}

func TestCheckReadyOwnerOfEachFile(t *testing.T) {
	dir, _, pin := ready(t)
	if _, err := CheckReady(dir, os.Getuid(), []string{pin}); err != nil {
		t.Fatal(err)
	}
	defer func(f func(os.FileInfo) (int, bool)) { ownerOf = f }(ownerOf)
	orig := ownerOf
	ownerOf = func(fi os.FileInfo) (int, bool) {
		u, ok := orig(fi)
		if fi.Name() == RootfsName {
			u++ // a file of another user in the receiver's folder
		}
		return u, ok
	}
	if _, err := CheckReady(dir, os.Getuid(), []string{pin}); err == nil || !strings.Contains(err.Error(), "not the receiver's file") {
		t.Fatalf("a file of another user: %v", err)
	}
}

// The manifest's bytes must be the record's, also when the change keeps the
// size and leaves a manifest that parses (the sha256 check alone catches it).
func TestCheckReadyManifestBytes(t *testing.T) {
	dir, man, pin := ready(t)
	i := bytes.Index(man, []byte("\n  "))
	if i < 0 {
		t.Fatal("no indent")
	}
	m2 := append([]byte(nil), man...)
	m2[i+1] = '\t' // whitespace for whitespace: the same size, still JSON
	os.WriteFile(filepath.Join(dir, ReadyDir, ManifestName), m2, 0o600)
	if _, err := CheckReady(dir, -1, []string{pin}); err == nil || !strings.Contains(err.Error(), "not the one of the record") {
		t.Fatalf("a changed manifest of the same size: %v", err)
	}
}
