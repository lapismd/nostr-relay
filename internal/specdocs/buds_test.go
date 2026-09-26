package specdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBUDSnapshotsAndInventory(t *testing.T) {
	root := filepath.Join("..", "..", "spec", "src")
	budRoot := filepath.Join(root, "buds")
	var pin struct {
		Repository string            `json:"repository"`
		Commit     string            `json:"commit"`
		Files      map[string]string `json:"files"`
	}
	if err := json.Unmarshal([]byte(readSpecFile(t, filepath.Join(budRoot, "pin.json"))), &pin); err != nil {
		t.Fatal(err)
	}
	if pin.Repository != "hzrd149/blossom" || len(pin.Commit) != 40 || len(pin.Files) != 13 {
		t.Fatal("BUD pin must identify the source and all 13 snapshots")
	}
	index := readSpecFile(t, filepath.Join(budRoot, "index.md"))
	summary := readSpecFile(t, filepath.Join(root, "SUMMARY.md"))
	for number := 0; number <= 12; number++ {
		name := fmt.Sprintf("%02d.md", number)
		upstream := filepath.ToSlash(filepath.Join("upstream", name))
		content, err := os.ReadFile(filepath.Join(budRoot, upstream))
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(content)
		if hex.EncodeToString(actual[:]) != pin.Files[upstream] {
			t.Errorf("BUD-%02d does not match pinned source checksum", number)
		}
		page := readSpecFile(t, filepath.Join(budRoot, name))
		for _, required := range []string{
			"{{#include " + upstream + "}}",
			"Lapis implementation audit:",
			"Status: **",
			"## Lapis implementation",
			"| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |",
		} {
			if !strings.Contains(page, required) {
				t.Errorf("BUD-%02d missing %q", number, required)
			}
		}
		if !strings.Contains(index, "("+name+")") || !strings.Contains(summary, "(buds/"+name+")") {
			t.Errorf("BUD-%02d missing from reference inventory or book navigation", number)
		}
	}
}
