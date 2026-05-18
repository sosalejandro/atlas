package diagnose

import "errors"

// ErrEmptySymptom is returned by Diagnose when the caller passes an empty
// or whitespace-only symptom string. It is a sentinel — callers test with
// errors.Is(err, diagnose.ErrEmptySymptom).
//
// The alternative — returning (nil, nil) on empty input — was rejected
// because it makes the "no matches" and "no symptom" cases indistinguishable
// at the call site, and the caller almost always wants to surface the
// usage error differently.
var ErrEmptySymptom = errors.New("diagnose: symptom is empty")
