package rules

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultMaxSites is how many sites a user may own unless an operator sets another number.
const DefaultMaxSites = 10

const maxUserNameLen = 64

var labelRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidUserName checks the display name of a user: 1 to 64 characters, no control characters, not blank.
func ValidUserName(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > maxUserNameLen {
		return fmt.Errorf("invalid user name %q: use 1 to %d characters", name, maxUserNameLen)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("invalid user name %q: it must contain a visible character", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid user name %q: control characters are not allowed", name)
		}
	}
	return nil
}

// ValidLabel checks a token label: 1 to 64 characters of letters, digits, dot, underscore and hyphen.
func ValidLabel(label string) error {
	if !labelRe.MatchString(label) {
		return fmt.Errorf("invalid token label %q: use 1 to 64 letters, digits, dots, underscores or hyphens", label)
	}
	return nil
}
