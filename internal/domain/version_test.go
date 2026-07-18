package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckExpectedVersionReturnsTypedConflict(t *testing.T) {
	t.Parallel()
	if err := CheckExpectedVersion("content_group", "cg-1", 5, 5); err != nil {
		t.Fatalf("matching version returned error: %v", err)
	}

	err := CheckExpectedVersion("content_group", "cg-1", 4, 5)
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error %T is not VersionConflictError", err)
	}
	if conflict.ResourceType != "content_group" || conflict.ResourceID != "cg-1" || conflict.Expected != 4 || conflict.Actual != 5 {
		t.Fatalf("unexpected conflict: %#v", conflict)
	}
}

func TestNextVersionStartsAtOne(t *testing.T) {
	t.Parallel()
	if got := NextVersion(0); got != 1 {
		t.Fatalf("NextVersion(0) = %d, want 1", got)
	}
	if got := NextVersion(9); got != 10 {
		t.Fatalf("NextVersion(9) = %d, want 10", got)
	}
}

func TestRESTModelsUseSnakeCaseJSONFields(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(struct {
		Torrent TorrentInstance `json:"torrent"`
		Plan    DeletePlan      `json:"plan"`
	}{
		Torrent: TorrentInstance{
			ID: "t", DownloaderID: "d", ExternalKey: "h", ContentGroupID: "cg", DataGroupID: "dg",
		},
		Plan: DeletePlan{
			Executable: true,
			Steps:      []DeleteStep{{InstanceID: "t", DeleteData: true}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(payload)
	for _, field := range []string{
		`"downloader_id"`,
		`"external_key"`,
		`"content_group_id"`,
		`"data_group_id"`,
		`"delete_data"`,
	} {
		if !strings.Contains(jsonText, field) {
			t.Fatalf("JSON %s missing field %s", jsonText, field)
		}
	}
}
