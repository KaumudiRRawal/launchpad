// Package openapi embeds the API specification into the binary. Serving the
// document from the same build that answers the requests means a caller can
// never be handed a description of a different version of the API.
package openapi

import _ "embed"

//go:embed openapi.yaml
var Document []byte
