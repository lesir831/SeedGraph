package domain

import "testing"

func TestPathCategory(t *testing.T) {
	for _, test := range []struct{ path, want string }{
		{"/volume1/001/media/Movies/The.Whisper.Man.2026.2160p.NF.WEB-DL.H.265.HDR.DDP5.1.Atmos-HHWEB", "Movies"},
		{"/media/TV/Series/", "TV"},
		{"/media/音乐/album.flac", "音乐"},
		{`D:\media\Movies\movie.mkv`, "Movies"},
		{"/media/Movies/../TV/series", "TV"},
		{"/movie.mkv", ""},
		{`D:\movie.mkv`, ""},
		{"/", ""},
		{"", ""},
		{"relative/movie.mkv", ""},
	} {
		t.Run(test.path, func(t *testing.T) {
			if got := PathCategory(test.path); got != test.want {
				t.Fatalf("PathCategory(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
