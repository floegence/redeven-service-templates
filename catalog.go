package servicetemplates

import (
	_ "embed"
	"strings"
)

const Version = "v0.6.2"

//go:embed dist/catalog.bundle.json
var bundle []byte

//go:embed dist/catalog.bundle.manifest.json
var manifest []byte

//go:embed dist/catalog.bundle.sha256
var bundleSHA256 string

// Bundle returns an independent copy of the verified catalog bundle.
func Bundle() []byte {
	return append([]byte(nil), bundle...)
}

// Manifest returns an independent copy of the catalog build manifest.
func Manifest() []byte {
	return append([]byte(nil), manifest...)
}

// BundleSHA256 returns the lowercase hexadecimal digest of Bundle.
func BundleSHA256() string {
	return strings.TrimSpace(bundleSHA256)
}
