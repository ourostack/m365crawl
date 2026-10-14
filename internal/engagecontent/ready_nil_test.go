package engagecontent

import (
	"context"
	"testing"
)

type nilReadyMap map[string]string

func (nilReadyMap) Navigate(context.Context, string) error                { return nil }
func (nilReadyMap) Host(context.Context) (string, error)                  { return "engage.cloud.microsoft", nil }
func (nilReadyMap) Eval(context.Context, string, any) error               { return nil }
func (nilReadyMap) AddInitScript(context.Context, string) (string, error) { return "synthetic", nil }
func (nilReadyMap) RemoveInitScript(context.Context, string) error        { return nil }

func TestEngageHomeReadyRefusesTypedNilMap(t *testing.T) {
	var page nilReadyMap
	if _, err := HomeReady(context.Background(), page); err == nil {
		t.Fatal("typed-nil map driver accepted")
	}
}
