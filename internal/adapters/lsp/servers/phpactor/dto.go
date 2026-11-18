package lspphpactor

import "encoding/json"

// progressCreateParams holds the token of a window/workDoneProgress/create request.
type progressCreateParams struct {
	Token json.RawMessage `json:"token"`
}

// progressParams holds the fields of a $/progress notification that identify the indexing progress and its end.
type progressParams struct {
	Token json.RawMessage `json:"token"`
	Value progressValue   `json:"value"`
}

// progressValue holds the kind of a $/progress notification.
type progressValue struct {
	Kind string `json:"kind"`
}
