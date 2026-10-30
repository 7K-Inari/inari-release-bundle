package suite

// Suite runs pass Ginkgo flags to EVERY test binary matched by
// `go test ./... -args --ginkgo.label-filter=...` — including this one,
// which contains the provisioner unit tests but no Ginkgo specs and would
// otherwise die with "flag provided but not defined". Blank-import ginkgo so
// this binary accepts (and ignores) the flags; importing the package
// registers the --ginkgo.* flags on the command line.
import _ "github.com/onsi/ginkgo/v2"
