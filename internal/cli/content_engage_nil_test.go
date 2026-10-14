package cli

import (
	"context"
	"testing"
)

type nilEngageMap map[string]string

func (nilEngageMap) Navigate(context.Context, string) error                { return nil }
func (nilEngageMap) Host(context.Context) (string, error)                  { return "engage.cloud.microsoft", nil }
func (nilEngageMap) Eval(context.Context, string, any) error               { return nil }
func (nilEngageMap) AddInitScript(context.Context, string) (string, error) { return "synthetic", nil }
func (nilEngageMap) RemoveInitScript(context.Context, string) error        { return nil }

func TestEngageEarlyCapabilityRefusesTypedNilMap(t *testing.T) {
	var page nilEngageMap
	if _, err := earlyEngagePage(page); err == nil {
		t.Fatal("typed-nil map page admitted")
	}
}
