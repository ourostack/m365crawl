package sharepointcontent

// Bridges keep browser-backed script contracts outside the backend package: the archive
// now consumes this backend, and browser ownership depends on the archive's lock.
var TestCaptureJS = captureJS
var TestNetworkJS = networkJS
var TestAdmit = admit
var TestDecode = decode
var TestNativeResult = nativeResult
type TestAdmittedRequest = admittedRequest
type TestScriptResult = scriptResult
