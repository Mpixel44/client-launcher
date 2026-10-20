package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func readServers(t *testing.T, path string) []nbtCompound {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := readNBT(b)
	if err != nil {
		t.Fatal(err)
	}
	var out []nbtCompound
	for _, it := range r.Value.(nbtCompound).get("servers").Value.(nbtList).Items {
		out = append(out, it.(nbtCompound))
	}
	return out
}

func TestServerListRoundTrip(t *testing.T) {
	b, _ := os.ReadFile("testdata/servers_hidden.dat")
	r, err := readNBT(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(writeNBT(r), b) {
		t.Fatal("round trip differs")
	}
}

func TestEnsureServerListed(t *testing.T) {
	dir := t.TempDir()
	gameRoot = dir
	p := &Pack{ID: "client", Name: "Nexus", Server: "minecraft.nexusdescartes.com"}
	inst := pInstance(p)
	os.MkdirAll(inst, 0o755)
	src, _ := os.ReadFile("testdata/servers_hidden.dat")
	os.WriteFile(filepath.Join(inst, "servers.dat"), src, 0o644)

	if err := ensureServerListed(p); err != nil {
		t.Fatal(err)
	}
	s := readServers(t, filepath.Join(inst, "servers.dat"))
	if len(s) != 2 {
		t.Fatalf("want 2 servers, got %d", len(s))
	}
	our := s[1]
	if our.str("ip") != "minecraft.nexusdescartes.com" || our.get("hidden").Value.(int8) != 0 || our.str("name") != "Nexus" {
		t.Fatalf("bad entry %+v", our)
	}
	if s[0].get("hidden").Value.(int8) != 1 {
		t.Fatal("other server touched")
	}
	// second run: nothing to change
	st1, _ := os.Stat(filepath.Join(inst, "servers.dat"))
	if err := ensureServerListed(p); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(filepath.Join(inst, "servers.dat"))
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("rewritten without change")
	}

	// no file at all
	os.Remove(filepath.Join(inst, "servers.dat"))
	if err := ensureServerListed(p); err != nil {
		t.Fatal(err)
	}
	s = readServers(t, filepath.Join(inst, "servers.dat"))
	if len(s) != 1 || s[0].str("ip") != "minecraft.nexusdescartes.com" || s[0].get("hidden").Value.(int8) != 0 {
		t.Fatalf("bad new file %+v", s)
	}
}
