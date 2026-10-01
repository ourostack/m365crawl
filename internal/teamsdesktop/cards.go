package teamsdesktop

import "strings"

// cardsText returns the readable text of the cards in a message's properties.cards value (a list,
// or a string holding a JSON list; each entry is {contentType, content} with content an object or
// a string holding one). The text of each card is joined with newlines in reading order. A card
// kind with no text (a Fluid embed, an announcement banner) contributes nothing.
//
// Adaptive Cards contribute their TextBlock and RichTextBlock text and FactSet facts ("title:
// value"), found through containers, column sets and the other nesting keys; input labels,
// images and actions are left out. Every other kind (hero, thumbnail, Office 365 connector) is
// read from its title, subtitle, summary and text, and from its sections' activity title,
// activity subtitle, text and facts.
func cardsText(v any) string {
	var lines []string
	for _, c := range items(v) {
		content := object(fld(c, "content"))
		if content == nil {
			continue
		}
		lines = appendCardLines(lines, content)
	}
	return strings.Join(lines, "\n")
}

// cardNesting are the Adaptive Card keys whose values hold more elements.
var cardNesting = []string{"body", "items", "columns", "rows", "cells"}

func appendCardLines(lines []string, el any) []string {
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			lines = append(lines, s)
		}
	}
	switch str(fld(el, "type")) {
	case "TextBlock":
		add(str(fld(el, "text")))
		return lines
	case "RichTextBlock":
		var b strings.Builder
		for _, in := range items(fld(el, "inlines")) {
			b.WriteString(firstNonEmpty(str(in), str(fld(in, "text"))))
		}
		add(b.String())
		return lines
	case "FactSet":
		return appendFacts(lines, fld(el, "facts"), "title")
	}
	for _, k := range []string{"title", "subtitle", "summary", "text"} {
		add(str(fld(el, k)))
	}
	for _, s := range items(fld(el, "sections")) {
		for _, k := range []string{"activityTitle", "activitySubtitle", "text"} {
			add(str(fld(s, k)))
		}
		lines = appendFacts(lines, fld(s, "facts"), "name")
	}
	for _, k := range cardNesting {
		for _, child := range items(fld(el, k)) {
			lines = appendCardLines(lines, child)
		}
	}
	return lines
}

// appendFacts adds one "name: value" line per fact; nameKey is "title" (Adaptive Card) or "name"
// (connector card). A fact with only one side keeps that side.
func appendFacts(lines []string, facts any, nameKey string) []string {
	for _, f := range items(facts) {
		name, value := strings.TrimSpace(str(fld(f, nameKey))), strings.TrimSpace(str(fld(f, "value")))
		switch {
		case name != "" && value != "":
			lines = append(lines, name+": "+value)
		case name != "" || value != "":
			lines = append(lines, name+value)
		}
	}
	return lines
}
