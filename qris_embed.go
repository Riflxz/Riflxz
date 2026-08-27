package main

import _ "embed"

// QrisImage — QRIS donasi di-embed ke binary (go:embed) supaya tidak perlu
// file terpisah di server. Sumber: qris.png (PNG asli, alpha di-flatten
// putih). Build: `go build -o YuukiBot .`
//
//go:embed qris.png
var QrisImage []byte
