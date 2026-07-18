package domain

import "fmt"

// VersionConflictError is returned when a REST mutation was prepared against
// an older aggregate version. Callers should translate it to HTTP 409.
type VersionConflictError struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Expected     uint64 `json:"expected"`
	Actual       uint64 `json:"actual"`
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf(
		"%s %q version conflict: expected %d, actual %d",
		e.ResourceType,
		e.ResourceID,
		e.Expected,
		e.Actual,
	)
}

// CheckExpectedVersion validates an optimistic-lock precondition.
func CheckExpectedVersion(resourceType, resourceID string, expected, actual uint64) error {
	if expected == actual {
		return nil
	}
	return &VersionConflictError{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Expected:     expected,
		Actual:       actual,
	}
}

// NextVersion increments a persisted aggregate version. Zero is reserved for
// records that have not yet been persisted.
func NextVersion(current uint64) uint64 {
	if current == 0 {
		return 1
	}
	return current + 1
}
