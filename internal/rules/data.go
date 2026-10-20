package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Defaults and server-wide ceilings for a site's data. An owner may set a site's limits anywhere up to a ceiling.
const (
	KVCollection = "_kv" // the reserved collection that holds key-value entries

	DefaultMaxCollections = 5
	DefaultMaxDocs        = 500
	DefaultMaxDocSize     = 4096

	CeilingMaxCollections = 100
	CeilingMaxDocs        = 20000
	CeilingMaxDocSize     = 4096

	DefaultPageSize = 20
	MaxPageSize     = 100

	// SortTextPrefix is how many characters of a text value a sort compares, which also keeps cursors small.
	SortTextPrefix = 256

	maxSortPathLen = 128
	maxKeyLen      = 128
)

var (
	collectionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	keyRe        = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	keyPrefixRe  = regexp.MustCompile(`^[A-Za-z0-9._:-]{0,128}$`)
	sortSegment  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// ValidCollection checks a collection name: 1 to 64 characters of a-z, 0-9, _ and -, not starting with _.
// Names starting with _ are reserved for the platform (_kv).
func ValidCollection(name string) error {
	if !collectionRe.MatchString(name) {
		return fmt.Errorf("invalid collection name %q: use 1 to 64 lowercase letters, digits, underscores or hyphens, not starting with an underscore", name)
	}
	return nil
}

// ValidKey checks a key-value key: 1 to 128 characters of letters, digits and . _ : -
func ValidKey(key string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("invalid key %q: use 1 to %d letters, digits, dots, underscores, colons or hyphens", key, maxKeyLen)
	}
	return nil
}

// ValidKeyPrefix checks a listing prefix, which may be empty.
func ValidKeyPrefix(prefix string) error {
	if !keyPrefixRe.MatchString(prefix) {
		return fmt.Errorf("invalid prefix %q: use up to %d letters, digits, dots, underscores, colons or hyphens", prefix, maxKeyLen)
	}
	return nil
}

// ValidSortPath checks a path into a document, such as title or when.start: dot-separated names made of
// letters, digits and underscores.
func ValidSortPath(path string) error {
	if path == "" || len(path) > maxSortPathLen {
		return fmt.Errorf("invalid sort path %q: use 1 to %d characters", path, maxSortPathLen)
	}
	for _, seg := range strings.Split(path, ".") {
		if !sortSegment.MatchString(seg) {
			return fmt.Errorf("invalid sort path %q: use names of letters, digits and underscores separated by dots, like when.start", path)
		}
	}
	return nil
}

// JSONPath turns a valid sort path into the path SQLite's JSON functions take: when.start becomes $.when.start.
func JSONPath(sortPath string) string { return "$." + sortPath }

// AboveCeilingError says a limit is higher than the server allows. The API reports it as above_ceiling.
type AboveCeilingError struct {
	Field   string
	Value   int
	Ceiling int
}

func (e *AboveCeilingError) Error() string {
	return fmt.Sprintf("%s %d is above the server ceiling of %d", e.Field, e.Value, e.Ceiling)
}

func checkLimit(field string, value, min, ceiling int) error {
	switch {
	case value < min:
		return fmt.Errorf("%s must be %d or more; got %d", field, min, value)
	case value > ceiling:
		return &AboveCeilingError{Field: field, Value: value, Ceiling: ceiling}
	}
	return nil
}

// The field names are the ones the settings API uses.
func CheckMaxCollections(n int) error {
	return checkLimit("maxCollections", n, 0, CeilingMaxCollections)
}
func CheckMaxDocs(n int) error    { return checkLimit("maxDocs", n, 0, CeilingMaxDocs) }
func CheckMaxDocSize(n int) error { return checkLimit("maxDocSize", n, 1, CeilingMaxDocSize) }

// ParseLimit reads the limit parameter of a listing: empty means DefaultPageSize, otherwise 1 to MaxPageSize.
func ParseLimit(s string) (int, error) {
	if s == "" {
		return DefaultPageSize, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxPageSize {
		return 0, fmt.Errorf("limit must be a whole number from 1 to %d; got %q", MaxPageSize, s)
	}
	return n, nil
}

// ParseOrder reads the order parameter of a listing and reports whether it is descending.
func ParseOrder(s string) (desc bool, err error) {
	switch s {
	case "", "asc":
		return false, nil
	case "desc":
		return true, nil
	}
	return false, fmt.Errorf("order must be asc or desc; got %q", s)
}
