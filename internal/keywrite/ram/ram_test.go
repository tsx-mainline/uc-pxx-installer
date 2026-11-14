//go:build install

package ram_test

import (
	"os"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/keywrite/ram"
	"uc-pxx-installer/internal/sshkey"
)

func TestRAM(t *testing.T) {
	k, err := sshkey.Parse([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp t\n"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "run", "sshkey")
	if k2, _, err := ram.LoadRAM(dir, "boot1", "tok"); k2 != nil || err != nil {
		t.Fatalf("none: %v %v", k2, err)
	}
	if err := ram.SaveRAM(dir, k, "boot1", "tok", "test"); err != nil {
		t.Fatal(err)
	}
	if k2, src, err := ram.LoadRAM(dir, "boot1", "tok"); err != nil || k2.Fingerprint != k.Fingerprint || src != "test" {
		t.Fatalf("load: %v %v %q", k2, err, src)
	}
	for _, c := range []struct{ boot, tok string }{{"boot2", "tok"}, {"boot1", "tok2"}} {
		if _, _, err := ram.LoadRAM(dir, c.boot, c.tok); err == nil {
			t.Fatalf("another boot or image: %+v", c)
		}
	}
	os.WriteFile(filepath.Join(dir, "key.pub"), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMcC4tUC7gadx+uXZrB0t3iBDXIMQAdId8iEmLb+8Z5N\n"), 0o600)
	if _, _, err := ram.LoadRAM(dir, "boot1", "tok"); err == nil {
		t.Fatal("a key.pub that key.json does not describe")
	}
	os.WriteFile(filepath.Join(dir, "extra"), nil, 0o600)
	if _, _, err := ram.LoadRAM(dir, "boot1", "tok"); err == nil {
		t.Fatal("an extra file")
	}
	if err := ram.ClearRAM(dir); err == nil {
		t.Fatal("ClearRAM removed a folder with an unknown file")
	}
	os.Remove(filepath.Join(dir, "extra"))
	if err := ram.ClearRAM(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("the folder stays")
	}
}
