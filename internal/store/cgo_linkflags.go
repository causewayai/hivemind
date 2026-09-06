package store

// The vendored sqlite-vec-go-bindings cgo package
// (github.com/asg017/sqlite-vec-go-bindings/cgo) calls libm functions
// (sqrt, sqrtf) but does not declare "#cgo LDFLAGS: -lm" itself. On macOS
// those symbols ship inside libSystem and get linked in automatically; on
// Linux, glibc keeps them in a separate libm.so that must be requested
// explicitly, so linking fails there with "undefined reference to `sqrt'"
// unless something in the final binary's cgo graph asks for -lm. This file
// supplies that flag for every binary/test that imports this package.

// #cgo LDFLAGS: -lm
import "C"
