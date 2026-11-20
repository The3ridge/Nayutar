package keyspace

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid is wrapped by every validation, encoding, and parsing error in this package.
var ErrInvalid = errors.New("keyspace: invalid")

// CoreGroup is the reserved group name for built-in kinds.
const CoreGroup = "core"

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func isHexLower(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
}

func validateElement(s, field string) error {
	if len(s) < 1 {
		return fmt.Errorf("%w: %s element must not be empty", ErrInvalid, field)
	}
	for i := range len(s) {
		b := s[i]
		if isAlnum(b) {
			continue
		}
		if b == '-' && i > 0 && i < len(s)-1 {
			continue
		}
		return fmt.Errorf("%w: %s invalid byte %q at index %d", ErrInvalid, field, b, i)
	}
	return nil
}

func validateLabel(s, field string) error {
	if len(s) < 1 || len(s) > 63 {
		return fmt.Errorf("%w: %s length %d must be 1..63", ErrInvalid, field, len(s))
	}
	return validateElement(s, field)
}

func validateSubdomain(s, field string) (int, error) {
	if len(s) < 1 || len(s) > 253 {
		return 0, fmt.Errorf("%w: %s length %d must be 1..253", ErrInvalid, field, len(s))
	}
	elemStart := 0
	elems := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			if err := validateElement(s[elemStart:i], field); err != nil {
				return 0, err
			}
			elems++
			elemStart = i + 1
		}
	}
	return elems, nil
}

// ValidateTenant reports whether s is a valid tenant label.
func ValidateTenant(s string) error {
	return validateLabel(s, "tenant")
}

// ValidateGroup reports whether s is CoreGroup or a subdomain with at least two elements.
func ValidateGroup(s string) error {
	if s == CoreGroup {
		return nil
	}
	elems, err := validateSubdomain(s, "group")
	if err != nil {
		return err
	}
	if elems < 2 {
		return fmt.Errorf("%w: group %q must be %q or contain a dot", ErrInvalid, s, CoreGroup)
	}
	return nil
}

// ValidateKind reports whether s is a valid kind label.
func ValidateKind(s string) error {
	return validateLabel(s, "kind")
}

// ValidateName reports whether s is a valid object name subdomain.
func ValidateName(s string) error {
	_, err := validateSubdomain(s, "name")
	return err
}

// ValidateNode reports whether s is a valid node name subdomain.
func ValidateNode(s string) error {
	_, err := validateSubdomain(s, "node")
	return err
}

// ValidateIdentityHash reports whether s is 64 lowercase hexadecimal bytes.
func ValidateIdentityHash(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("%w: identity hash length %d must be 64", ErrInvalid, len(s))
	}
	for i := range len(s) {
		if !isHexLower(s[i]) {
			return fmt.Errorf("%w: identity hash invalid byte %q at index %d", ErrInvalid, s[i], i)
		}
	}
	return nil
}

// ValidateVersion reports whether s is a valid schema version label.
func ValidateVersion(s string) error {
	return validateLabel(s, "version")
}

// QualifiedKind formats group and kind as <kind> when group is CoreGroup, or <kind>.<group> otherwise.
func QualifiedKind(group, kind string) (string, error) {
	if err := ValidateGroup(group); err != nil {
		return "", err
	}
	if err := ValidateKind(kind); err != nil {
		return "", err
	}
	if group == CoreGroup {
		return kind, nil
	}
	return kind + "." + group, nil
}

// ParseQualifiedKind splits a qualified kind into its group and kind.
func ParseQualifiedKind(s string) (group, kind string, err error) {
	kindPart, groupPart, ok := strings.Cut(s, ".")
	if !ok {
		if err := ValidateKind(s); err != nil {
			return "", "", err
		}
		return CoreGroup, s, nil
	}
	if err := ValidateKind(kindPart); err != nil {
		return "", "", err
	}
	elems, err := validateSubdomain(groupPart, "group")
	if err != nil {
		return "", "", err
	}
	if elems < 2 {
		return "", "", fmt.Errorf("%w: qualified kind group %q must contain a dot", ErrInvalid, groupPart)
	}
	return groupPart, kindPart, nil
}
