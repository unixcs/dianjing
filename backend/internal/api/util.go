package api

import (
	"encoding/json"
	"net/http"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

var _ = http.StatusOK
