package engagecontent

import "context"

// Test-only exports let browser integration tests live outside this package:
// browser's archive policy now legitimately depends on the Engage store.
var InitScriptForTest = initScript
var HomeStateForTest = homeStateExpr
var HomeClickForTest = homeClickExpr
var DecodeForTest = decode

type ScriptResultForTest = scriptResult

func SetCollectionForTest(fn func(context.Context) error) func() {
	previous := waitCollection
	waitCollection = fn
	return func() { waitCollection = previous }
}
