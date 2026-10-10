package plan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
)

// normalizeRef parses an image reference the way Docker does and returns its
// familiar form with ":latest" filled in when no tag or digest was given.
// Pulling a bare repository name would otherwise mean "every tag".
func normalizeRef(input string) (string, error) {
	if input == "" {
		return "", errors.New("enter an image reference, e.g. alpine:3.20")
	}
	if strings.ContainsAny(input, " \t") {
		return "", errors.New("an image reference cannot contain spaces")
	}
	named, err := reference.ParseNormalizedNamed(input)
	if err != nil {
		return "", fmt.Errorf("not a valid image reference: %v", err)
	}
	return reference.FamiliarString(reference.TagNameOnly(named)), nil
}
