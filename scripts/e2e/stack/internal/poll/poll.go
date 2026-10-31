// Package poll is the ONLY mechanism the suite uses to observe asynchronous
// state: never assert immediately after a mutation (OrgTeamSync intervals,
// outbox relay, FGA tuple writes, PEP TTLs all propagate with delay).
package poll

import (
	"fmt"
	"time"
)

// T is the minimal test context Eventually needs. Both *testing.T and
// Ginkgo's GinkgoT() (via the suite's TB interface) satisfy it; testing.TB
// itself is not usable because GinkgoT()'s interface type lacks the
// unexported private() marker.
type T interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Until runs cond every interval until it returns true or the timeout
// elapses. A non-nil error from cond aborts immediately (permanent failure);
// (false, nil) means "not yet".
func Until(timeout, interval time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var attempts int
	for {
		attempts++
		ok, err := cond()
		if err != nil {
			return fmt.Errorf("poll aborted after %d attempt(s): %w", attempts, err)
		}
		if ok {
			return nil
		}
		if time.Now().Add(interval).After(deadline) {
			return fmt.Errorf("condition not met within %s (%d attempts)", timeout, attempts)
		}
		time.Sleep(interval)
	}
}

// Eventually is Until bound to a test: it fails t when the condition never
// holds. msg describes the condition in words ("org_creator tuple present").
func Eventually(t T, timeout, interval time.Duration, cond func() (bool, error), msg string) {
	t.Helper()
	if err := Until(timeout, interval, cond); err != nil {
		t.Fatalf("eventually %s: %v", msg, err)
	}
}
