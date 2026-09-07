package releasewatcher

import (
	"regexp"
	"strings"
)

var versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validVersion(value string) bool {
	parts := versionRE.FindStringSubmatch(value)
	if parts == nil {
		return false
	}
	for _, part := range strings.Split(parts[4], ".") {
		if allDigits(part) && len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}

// Numeric strings avoid overflow while following SemVer's unbounded identifiers.
func numericCompare(a, b string) int {
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

func compareVersions(a, b string) int {
	left, right := versionRE.FindStringSubmatch(a), versionRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := numericCompare(left[i], right[i]); c != 0 {
			return c
		}
	}
	if left[4] == right[4] {
		return 0
	}
	if left[4] == "" {
		return 1
	}
	if right[4] == "" {
		return -1
	}
	x, y := strings.Split(left[4], "."), strings.Split(right[4], ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		xn, yn := allDigits(x[i]), allDigits(y[i])
		if xn && yn {
			return numericCompare(x[i], y[i])
		}
		if xn {
			return -1
		}
		if yn {
			return 1
		}
		return strings.Compare(x[i], y[i])
	}
	if len(x) > len(y) {
		return 1
	}
	return -1
}
