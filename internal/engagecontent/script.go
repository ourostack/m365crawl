package engagecontent

import _ "embed"

//go:embed capture.js
var captureJS string

//go:embed projection.js
var projectionJS string

var initScript = "(" + captureJS + ")(" + projectionJS + ");"

const stopReadExpr = `window.__m365crawlEngageCapture ? window.__m365crawlEngageCapture.stop() : ({Fatal:"observer_missing"})`
