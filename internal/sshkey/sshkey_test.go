package sshkey_test

import (
	"encoding/json"
	"os"
	"testing"

	"uc-pxx-installer/internal/sshkey"
)

// The page's vectors (web/test/unit/sshkey-vectors.json, fingerprints by
// ssh-keygen): the Go rule must agree with the page's on every one.
func TestVectors(t *testing.T) {
	b, err := os.ReadFile("../../web/test/unit/sshkey-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Rule struct {
			MaxBytes   int    `json:"max_bytes"`
			RSAMinBits int    `json:"rsa_min_bits"`
			RSAMaxBits int    `json:"rsa_max_bits"`
			FileName   string `json:"file_name"`
		} `json:"rule"`
		Vectors []struct {
			Name        string `json:"name"`
			Input       string `json:"input"`
			Valid       bool   `json:"valid"`
			Type        string `json:"type"`
			Bits        int    `json:"bits"`
			Fingerprint string `json:"fingerprint"`
			Line        string `json:"line"`
			File        string `json:"file"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Rule.MaxBytes != sshkey.MaxBytes || d.Rule.RSAMinBits != sshkey.RSAMinBits || d.Rule.RSAMaxBits != sshkey.RSAMaxBits ||
		d.Rule.FileName != sshkey.FileName {
		t.Fatalf("the page's rule constants %+v differ", d.Rule)
	}
	valid := 0
	for _, v := range d.Vectors {
		k, err := sshkey.Parse([]byte(v.Input))
		if v.Valid != (err == nil) {
			t.Errorf("%s: valid %v, Go says %v", v.Name, v.Valid, err)
			continue
		}
		if !v.Valid {
			continue
		}
		valid++
		if k.Type != v.Type || k.Bits != v.Bits || k.Fingerprint != v.Fingerprint || k.Line != v.Line || string(k.File()) != v.File {
			t.Errorf("%s: %+v, want %s %d %s %q", v.Name, k, v.Type, v.Bits, v.Fingerprint, v.Line)
		}
	}
	if valid < 5 || len(d.Vectors) < 40 {
		t.Fatalf("only %d vectors (%d valid)", len(d.Vectors), valid)
	}
}

func TestPanelConf(t *testing.T) {
	in := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\r\n"
	k, err := sshkey.Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(k.PanelConf()) != "SSH_AUTHORIZED_KEY=\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\"\n" ||
		string(sshkey.SetupStamp()) != "2\n" {
		t.Fatalf("%q", k.PanelConf())
	}
}
