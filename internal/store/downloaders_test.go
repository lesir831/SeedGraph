package store

import (
	"context"
	"testing"
)

func TestCreateAndListDownloader(t *testing.T) {
	store := openTestStore(t)
	created, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name:               "qB NAS",
		Kind:               "qbittorrent",
		BaseURL:            "http://qb:8080",
		UsernameCiphertext: "encrypted-user",
		PasswordCiphertext: "encrypted-password",
		StorageName:        "media",
		Enabled:            true,
		PathMappings: []PathMapping{{
			SourcePrefix: "/downloads",
			TargetPrefix: "/media",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.PathMappings) != 1 {
		t.Fatalf("unexpected created downloader: %+v", created)
	}

	items, err := store.ListDownloaders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PasswordCiphertext != "encrypted-password" {
		t.Fatalf("unexpected downloaders: %+v", items)
	}
}
