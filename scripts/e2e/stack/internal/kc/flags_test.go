package kc

// Suite runs pass Ginkgo flags to EVERY test binary matched by
// `go test ./... -args --ginkgo.label-filter=...` — including this one,
// which neither imports Ginkgo nor defines the flags and would otherwise die
// with "flag provided but not defined". Blank-import ginkgo so this binary
// accepts (and ignores) them; importing the package registers the --ginkgo.*
// flags on the command line.
import _ "github.com/onsi/ginkgo/v2"
