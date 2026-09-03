module shellf

go 1.26.0

// Pinned, not decorative: `go 1.26` alone lets a runner settle on 1.26.5, which carries
// GO-2026-5972 (unbounded recursion in encoding/asn1). shellf is on that trace — every
// SSH run parses a private key through ssh.ParsePrivateKey (#342).
toolchain go1.26.6

require golang.org/x/crypto v0.56.0

require golang.org/x/sys v0.47.0 // indirect
