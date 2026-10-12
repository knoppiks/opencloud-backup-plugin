//go:build testing

package decrypt

// kopiaTestingTag reports that this test binary was built with kopia's
// `testing` tag (`make test`), under which new repositories derive their key
// with kopia's fast, insecure test derivation instead of the one a release
// uses (#94).
const kopiaTestingTag = true
