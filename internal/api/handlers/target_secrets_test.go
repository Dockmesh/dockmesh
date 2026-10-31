package handlers

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

func TestTargetPrivateKeyIsWriteOnly(t *testing.T) {
	stored := map[string]any{"host": "h", "private_key": "KEY", "password": "pw"}

	out := redactTarget(&targets.StoredTarget{Type: "sftp", Config: stored}).Config.(map[string]any)
	if out["private_key"] != "" || out["private_key_set"] != true {
		t.Fatalf("private key leaked or unmarked: %v", out)
	}

	kept, err := keepConfig(map[string]any{"host": "h", "private_key": ""}, stored, backupTargetSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if kept.(map[string]any)["private_key"] != "KEY" {
		t.Fatalf("empty private_key did not keep the stored one: %v", kept)
	}
}
