package sharepointcontent

import (
	_ "embed"
	"encoding/json"
)

//go:embed network.js
var networkJS string

//go:embed capture.js
var captureJS string

func ScriptExpr(request admittedRequest) string {
	args, _ := json.Marshal(request)
	return "(" + captureJS + ")(" + string(args) + ",(" + networkJS + "))"
}
