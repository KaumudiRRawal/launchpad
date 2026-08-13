// Package migrations embeds the SQL schema migrations into the binary so the
// control plane can migrate itself on boot with no external tooling.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
