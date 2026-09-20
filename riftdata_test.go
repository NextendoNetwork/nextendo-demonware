package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPatchChallengeConfig(t *testing.T) {
	// number 346, start/broadcast 2024-02-05, end 2024-02-12, hash: as captured
	src := []byte{0x08, 0xda, 0x02, 0x10, 0xd0, 0x98, 0x85, 0xae, 0x06, 0x18, 0xd0, 0x98, 0x85, 0xae, 0x06,
		0x20, 0xd0, 0x8d, 0xaa, 0xae, 0x06, 0x28, 0xcc, 0xb5, 0x8a, 0xf4, 0xcd, 0x9d, 0xd3, 0xf0, 0x5c}
	got, err := patchChallengeConfig(src, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x08, 0x05, 0x10, 0x00, 0x18, 0x00, 0x20, 0xff, 0xff, 0xff, 0xff, 0x07,
		0x28, 0xcc, 0xb5, 0x8a, 0xf4, 0xcd, 0x9d, 0xd3, 0xf0, 0x5c}
	if string(got) != string(want) {
		t.Fatalf("got %x want %x", got, want)
	}
	if _, err := patchChallengeConfig([]byte{0x08}, 1); err == nil {
		t.Fatal("truncated config accepted")
	}
}

func TestRiftFileMapping(t *testing.T) {
	dir := t.TempDir()
	oldDir, oldCfg := riftDir, pubConfigPath
	riftDir, pubConfigPath = dir, filepath.Join(dir, "pubfiles.json")
	defer func() { riftDir, pubConfigPath = oldDir, oldCfg }()
	for _, n := range []string{"00", "01", "02"} {
		if err := os.WriteFile(filepath.Join(dir, "challengerift_"+n+".dat"), []byte("rift"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, ok := riftFile("challengerift_config.dat", time.Now()); ok {
		t.Fatal("served a config that does not exist")
	}
	if err := os.WriteFile(filepath.Join(dir, "challengerift_config.dat"), []byte{0x08, 0x01, 0x28, 0x07}, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, ok := riftFile("Challengerift_Config.dat", time.Now())
	if !ok || cfg[0] != 0x08 {
		t.Fatalf("config not served: %v %x", ok, cfg)
	}
	// unpadded numbers as the game asks; numbers past the last file wrap around
	for req, want := range map[string]string{
		"challengerift_0.dat": "rift00", "challengerift_1.dat": "rift01",
		"challengerift_3.dat": "rift00", "challengerift_346.dat": "rift01",
		"challengerift_02.dat": "rift02",
	} {
		data, _, ok := riftFile(req, time.Now())
		if !ok || string(data) != want {
			t.Errorf("%s -> %q ok=%v, want %q", req, data, ok, want)
		}
	}
	if _, _, ok := riftFile("Config.txt", time.Now()); ok {
		t.Fatal("non-rift file claimed")
	}
}

func TestRiftChallengeNumber(t *testing.T) {
	a := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	weekly := riftSettings{Mode: "weekly"}
	if riftChallengeNumber(weekly, a) == riftChallengeNumber(weekly, a.Add(riftWeek)) {
		t.Fatal("weekly mode did not advance after a week")
	}
	if riftChallengeNumber(riftSettings{Mode: "fixed", Number: 7}, a) != 7 {
		t.Fatal("fixed mode")
	}
}
