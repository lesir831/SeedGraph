package domain

import "path"

// PathCategory is the parent directory of a canonical content path. It works
// for both single-file and directory torrents without a media-name whitelist.
func PathCategory(contentPath string) string {
	cleaned, err := cleanAbsolutePath(contentPath)
	if err != nil {
		return ""
	}
	parent := path.Dir(cleaned)
	if parent == "/" || parent == "." || (len(parent) == 2 && parent[1] == ':') {
		return ""
	}
	return path.Base(parent)
}
