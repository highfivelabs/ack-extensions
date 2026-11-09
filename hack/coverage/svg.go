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
// uncovered, of fixed width.
func barSVG(title string, s svcRow) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", barWidth, barHeight, barWidth, barHeight, html.EscapeString(title))
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	segments(&b, s, 0, 0, barWidth, barHeight, 12)
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

// segments draws the three parts of the bar at (x, y), w by h.
func segments(b *strings.Builder, s svcRow, x, y, w, h, fontPx int) {
	type part struct {
		n    int
		fill string
		text string
	}
	parts := []part{
		{s.attrOfficial, colOfficial, colTextOn},
		{s.attrOursBeyond, colOurs, colTextOn},
		{s.attrUncov, colNone, colTextDark},
	}
	fmt.Fprintf(b, `<rect x="%d" y="%d" width="%d" height="%d" rx="3" fill="%s"/>`+"\n", x, y, w, h, colNone)
	// Widths in tenths of a pixel, so the parts add up to the bar.
	acc := 0
	total := s.attrTotal
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

func barTitle(name string, s svcRow) string {
	return fmt.Sprintf("%s: official %s%%, ours beyond official %s%%, uncovered %s%% of %d attributes",
		name, pct1(s.attrOfficial, s.attrTotal), pct1(s.attrOursBeyond, s.attrTotal), pct1(s.attrUncov, s.attrTotal), s.attrTotal)
}

// totalSVG is the large total bar with a legend, on its own white card so
// the legend reads on a dark page too.
func totalSVG(s svcRow, officialName string) []byte {
	const w, h, pad = 640, 30, 10
	var b strings.Builder
	title := html.EscapeString(barTitle("All services", s))
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", w+2*pad, h+2*pad+26, w+2*pad, h+2*pad+26, title)
	fmt.Fprintf(&b, "<title>%s</title>\n", title)
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="6" fill="#ffffff" stroke="#d0d7de"/>`+"\n", w+2*pad-1, h+2*pad+25)
	segments(&b, s, pad, pad, w, h, 14)
	items := []struct{ fill, text string }{
		{colOfficial, fmt.Sprintf("%s %s%%", officialName, pct1(s.attrOfficial, s.attrTotal))},
		{colOurs, fmt.Sprintf("ours beyond %s %s%%", officialName, pct1(s.attrOursBeyond, s.attrTotal))},
		{colNone, fmt.Sprintf("uncovered %s%%", pct1(s.attrUncov, s.attrTotal))},
	}
	x := pad
	for _, it := range items {
		fmt.Fprintf(&b, `<rect x="%d.5" y="%d.5" width="11" height="11" rx="2" fill="%s" stroke="%s"/>`+"\n", x, h+pad+9, it.fill, colBorder)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="%s" font-size="12" fill="%s" dominant-baseline="central">%s</text>`+"\n", x+17, h+pad+15, fontFamily, colTextDark, html.EscapeString(it.text))
		x += 17 + 7*len(it.text) + 18
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}
