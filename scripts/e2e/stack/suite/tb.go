// TB is the subset of testing.TB the suite phases and provisioner use. Both
// *testing.T (plain go test entry points like the e2eprovision-tagged
// targeted reruns) and Ginkgo's GinkgoT() satisfy it, so the same phase
// bodies run unchanged under the Ginkgo suite structure.
//
// testing.TB itself cannot be used: GinkgoT() returns an interface whose
// method set lacks the unexported private() marker, so it is not assignable
// to testing.TB even though its concrete type satisfies it.
//
// This file intentionally carries NO e2e build tag (pure type declaration).
package suite

// TB is the minimal test context for the stack suite.
type TB interface {
	Helper()
	Cleanup(func())
	Log(args ...any)
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	FailNow()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Skip(args ...any)
	Skipf(format string, args ...any)
}
