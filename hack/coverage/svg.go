// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"html"
	"strings"
)

// Colors work on a white and on a dark page: each bar carries its own
// track, borders and text colors, so it does not depend on the page theme.
const (
	colOfficial = "#2f81f7" // blue
	colOurs     = "#2da44e" // green
	colNone     = "#d0d7de" // light grey
	colBorder   = "#8c959f"
	colTextDark = "#1f2328"
	colTextOn   = "#ffffff"
	fontFamily  = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"
)

const (
	barWidth  = 300
	barHeight = 22
)

// barSVG is a stacked horizontal bar, official then ours beyond then
// uncovered, of fixed width; with the Terraform estimate (hasTF), a thin
// purple bar under it, its L1 floor marked by a tick.
func barSVG(title string, s svcRow, tf tfCounts, hasTF bool) []byte {
	height := barHeight
	if hasTF {
		height += tfGap + tfHeight
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", barWidth, height, barWidth, height, html.EscapeString(title))
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	segments(&b, s.attrOfficial, s.attrOursBeyond, s.attrUncov, 0, 0, barWidth, barHeight, 12)
	if hasTF {
		tfBar(&b, tf, 0, barHeight+tfGap, barWidth, tfHeight, 0)
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

// The thin Terraform bar under a service's bar.
const (
	tfGap    = 3
	tfHeight = 8
)

// segments draws the three parts of the bar at (x, y), w by h.
func segments(b *strings.Builder, official, ours, none, x, y, w, h, fontPx int) {
	type part struct {
		n    int
		fill string
		text string
	}
	parts := []part{
		{official, colOfficial, colTextOn},
		{ours, colOurs, colTextOn},
		{none, colNone, colTextDark},
	}
	fmt.Fprintf(b, `<rect x="%d" y="%d" width="%d" height="%d" rx="3" fill="%s"/>`+"\n", x, y, w, h, colNone)
	// Widths in tenths of a pixel, so the parts add up to the bar.
	acc := 0
	total := official + ours + none
	cum := 0
	for i, p := range parts {
		if total == 0 || p.n == 0 {
			continue
		}
		start := acc
		cum += p.n
		end := w * 10 * cum / total
		if i == len(parts)-1 {
			end = w * 10
		}
		acc = end
		pw := end - start
		fmt.Fprintf(b, `<rect x="%s" y="%d" width="%s" height="%d" fill="%s"/>`+"\n", tenths(x*10+start), y, tenths(pw), h, p.fill)
		label := pct1(p.n, total) + "%"
		if pw/10 >= 8*len(label)+6 {
			fmt.Fprintf(b, `<text x="%s" y="%d" font-family="%s" font-size="%d" font-weight="600" fill="%s" text-anchor="middle" dominant-baseline="central">%s</text>`+"\n",
				tenths(x*10+start+pw/2), y+h/2+1, fontFamily, fontPx, p.text, label)
		}
	}
	fmt.Fprintf(b, `<rect x="%d.5" y="%d.5" width="%d" height="%d" rx="3" fill="none" stroke="%s"/>`+"\n", x, y, w-1, h-1, colBorder)
}

func tenths(n int) string {
	if n%10 == 0 {
		return fmt.Sprintf("%d", n/10)
	}
	return fmt.Sprintf("%d.%d", n/10, n%10)
}

func barTitle(name string, s svcRow, tf tfCounts, hasTF bool) string {
	t := fmt.Sprintf("%s: official %s%%, ours beyond official %s%%, uncovered %s%% of %d attributes",
		name, pct1(s.attrOfficial, s.attrTotal), pct1(s.attrOursBeyond, s.attrTotal), pct1(s.attrUncov, s.attrTotal), s.attrTotal)
	if hasTF {
		t += "; lower bar: " + tfTitle(tf)
	}
	return t
}

// totalSVG is the total card: two bars, attributes and resources (a few very
// large resources dominate the attribute count), with a legend, on its own
// white card so the legend reads on a dark page too.
func totalSVG(s svcRow, officialName string, tf tfCounts) []byte {
	const w, h, pad, labelH = 640, 26, 10, 18
	height := pad + 3*(labelH+h+8) + 22 + 22 + pad - 8
	var b strings.Builder
	title := html.EscapeString(barTitle("All services", s, tfCounts{}, false) + "; " + tfTitle(tf) + "; resources: " + fmt.Sprintf("official %s%%, ours beyond official %s%%, uncovered %s%% of %d",
		pct1(s.resOfficial, s.resTotal), pct1(s.resOursBeyond, s.resTotal), pct1(s.resTotal-s.resOfficial-s.resOursBeyond, s.resTotal), s.resTotal))
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", w+2*pad, height, w+2*pad, height, title)
	fmt.Fprintf(&b, "<title>%s</title>\n", title)
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="6" fill="#ffffff" stroke="#d0d7de"/>`+"\n", w+2*pad-1, height-1)
	rows := []struct {
		label                string
		official, ours, none int
	}{
		{fmt.Sprintf("Attributes (%s)", commas(s.attrTotal)), s.attrOfficial, s.attrOursBeyond, s.attrUncov},
		{fmt.Sprintf("Resources (%s)", commas(s.resTotal)), s.resOfficial, s.resOursBeyond, s.resTotal - s.resOfficial - s.resOursBeyond},
	}
	y := pad
	for _, r := range rows {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" font-weight="600" fill="%s" dominant-baseline="central">%s</text>`+"\n", pad, y+labelH/2, fontFamily, colTextDark, html.EscapeString(r.label))
		segments(&b, r.official, r.ours, r.none, pad, y+labelH, w, h, 13)
		y += labelH + h + 8
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" font-weight="600" fill="%s" dominant-baseline="central">%s</text>`+"\n", pad, y+labelH/2, fontFamily, colTextDark,
		html.EscapeString(fmt.Sprintf("Terraform aws provider, attributes (%s): an estimate, not a measure of a controller", commas(tf.total))))
	tfBar(&b, tf, pad, y+labelH, w, h, 13)
	y += labelH + h + 8
	items := []struct{ fill, text string }{
		{colOfficial, officialName},
		{colOurs, "ours beyond " + officialName},
		{colNone, "uncovered"},
	}
	x := pad
	for _, it := range items {
		fmt.Fprintf(&b, `<rect x="%d.5" y="%d.5" width="11" height="11" rx="2" fill="%s" stroke="%s"/>`+"\n", x, y+1, it.fill, colBorder)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="%s" dominant-baseline="central">%s</text>`+"\n", x+17, y+7, fontFamily, colTextDark, html.EscapeString(it.text))
		x += 17 + 7*len(it.text) + 18
	}
	tfLegend(&b, pad, y+23)
	b.WriteString("</svg>\n")
	return []byte(b.String())
}
