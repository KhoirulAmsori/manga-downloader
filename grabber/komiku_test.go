// Copyright (C) 2023-2026 Òscar Casajuana Alonso

package grabber

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// komikuSeriesHTML mirrors the bits of a series page the grabber reads: the
// #Judul header with the itemprop title, and a chapter table whose rows
// disagree with their own slugs the way re-uploaded chapters do
const komikuSeriesHTML = `<!DOCTYPE html><html><head>
<title>Komik One Piece - Komiku</title>
<meta property="og:title" content="Komik One Piece">
</head><body>
<div id="Judul"><header><h1><span>Komik <span itemprop="name">One Piece</span></span></h1></header></div>
<table id="Daftar_Chapter">
<tbody>
<tr><th>Chapter</th><th>Tanggal Rilis</th></tr>
<tr itemprop="itemListElement"><td><meta itemprop="position" content="1"><a href="/one-piece-chapter-1194/" itemprop="url"><span itemprop="name"><b>Chapter 1194</b></span></a></td><td class="tanggalseries">26/09/2026</td></tr>
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-1053-7/" itemprop="url"><span itemprop="name"><b>Chapter 1053.7</b></span></a></td><td class="tanggalseries">17/07/2022</td></tr>
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-1053/" itemprop="url"><span itemprop="name"><b>Chapter 1053</b></span></a></td><td class="tanggalseries">17/06/2022</td></tr>
<!-- slug tail says 1189.2, the row itself says plain 1189 -->
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-1189-2/" itemprop="url"><span itemprop="name"><b>Chapter 1189</b></span></a></td><td class="tanggalseries">25/07/2026</td></tr>
<!-- same number again, but dated older: the row above wins -->
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-1189/" itemprop="url"><span itemprop="name"><b>Chapter 1189</b></span></a></td><td class="tanggalseries">10/07/2026</td></tr>
<!-- a five-year-old copy whose label carries a trailing dot, re-uploaded in
     2026 under a "-2" slug: the dated re-upload must win here -->
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-100/" itemprop="url"><span itemprop="name"><b>Chapter 100.</b></span></a></td><td class="tanggalseries">14/11/2021</td></tr>
<tr itemprop="itemListElement"><td><a href="/one-piece-chapter-100-2/" itemprop="url"><span itemprop="name"><b>Chapter 100</b></span></a></td><td class="tanggalseries">13/06/2026</td></tr>
</tbody>
</table>
</body></html>`

// komikuReaderHTML mirrors a reader page: three ad images inside the reader
// box that must not be mistaken for pages, pages spread over the several
// hosts the CDN round-robins over, and valueGambar declaring the real count
const komikuReaderHTML = `<!DOCTYPE html><html><body>
<div class="chapterInfo" valueGambar="3" valueChapter="332"></div>
<div id="Baca_Komik">
<img src="https://image2.komiku.to/komiku-promosi.webp">
<img src="https://image2.komiku.to/upload5/hitoribocchi/332/1.webp" class="klazy ww" id="1">
<img src="https://image4.komiku.to/upload5/hitoribocchi/332/2.webp" class="klazy ww" id="2" onerror="this.src=this.src.replace('image4.komiku.to','img.komiku.org')">
<img src="//img.komiku.org/upload5/hitoribocchi/332/3.webp" class="klazy ww" id="3">
<img class="ads1img" src="/asset/img/komikuplus2.jpg">
<img src="/asset/img/Loading.gif" alt="Chapter Berikutnya">
</div>
</body></html>`

func komikuDoc(t *testing.T, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}

	return doc
}

func TestKomikuSeriesTitle(t *testing.T) {
	doc := komikuDoc(t, komikuSeriesHTML)
	if got := komikuSeriesTitle(doc); got != "One Piece" {
		t.Errorf("komikuSeriesTitle() = %q, want %q", got, "One Piece")
	}
}

// TestKomikuSeriesTitleFallsBackWithoutItemprop covers a series page whose
// header lost its itemprop markup: the <title>/og:title fallbacks both still
// carry the site's own prefix and suffix, which must be stripped
func TestKomikuSeriesTitleFallsBackWithoutItemprop(t *testing.T) {
	doc := komikuDoc(t, `<html><head>
<title>Komik One Piece - Komiku</title>
<meta property="og:title" content="Komik One Piece">
</head><body></body></html>`)
	if got := komikuSeriesTitle(doc); got != "One Piece" {
		t.Errorf("komikuSeriesTitle() = %q, want %q", got, "One Piece")
	}
}

func TestKomikuChapters(t *testing.T) {
	chapters, err := komikuChapters(komikuDoc(t, komikuSeriesHTML), "https://komiku.org")
	if err != nil {
		t.Fatalf("komikuChapters() error = %v", err)
	}

	want := []struct {
		number float64
		title  string
		url    string
	}{
		{1194, "Chapter 1194", "https://komiku.org/one-piece-chapter-1194/"},
		{1053.7, "Chapter 1053.7", "https://komiku.org/one-piece-chapter-1053-7/"},
		{1053, "Chapter 1053", "https://komiku.org/one-piece-chapter-1053/"},
		// the row's own label wins over the slug's "-2" tail...
		{1189, "Chapter 1189", "https://komiku.org/one-piece-chapter-1189-2/"},
		// ...and for a number claimed twice, the later upload wins over the
		// five-year-old copy even though that one is listed first
		{100, "Chapter 100", "https://komiku.org/one-piece-chapter-100-2/"},
	}

	if len(chapters) != len(want) {
		t.Fatalf("komikuChapters() returned %d chapters, want %d (the duplicate-number rows must collapse)", len(chapters), len(want))
	}

	for i, w := range want {
		got := chapters[i].(*KomikuChapter)
		if got.GetNumber() != w.number {
			t.Errorf("chapter %d: number = %v, want %v", i, got.GetNumber(), w.number)
		}
		if got.GetTitle() != w.title {
			t.Errorf("chapter %d: title = %q, want %q", i, got.GetTitle(), w.title)
		}
		if got.URL != w.url {
			t.Errorf("chapter %d: url = %q, want %q", i, got.URL, w.url)
		}
	}
}

func TestKomikuChaptersWithoutTable(t *testing.T) {
	if _, err := komikuChapters(komikuDoc(t, `<html><body><h1>Nope</h1></body></html>`), "https://komiku.org"); err == nil {
		t.Error("komikuChapters() with no chapter table should error, got nil")
	}
}

// TestKomikuChaptersSkipsUnreadableRows makes sure one row whose label is no
// chapter at all (an announcement, an extra) only costs that row, not the
// whole table
func TestKomikuChaptersSkipsUnreadableRows(t *testing.T) {
	html := strings.Replace(komikuSeriesHTML, "</tbody>",
		`<tr itemprop="itemListElement"><td><a href="/manga/one-piece/" itemprop="url"><span itemprop="name"><b>News</b></span></a></td><td class="tanggalseries">01/01/2026</td></tr>
		</tbody>`, 1)

	chapters, err := komikuChapters(komikuDoc(t, html), "https://komiku.org")
	if err != nil {
		t.Fatalf("komikuChapters() error = %v", err)
	}
	if len(chapters) != 5 {
		t.Errorf("komikuChapters() returned %d chapters, want 5", len(chapters))
	}
	for _, c := range chapters {
		if c.GetTitle() == "News" {
			t.Error("the unreadable row shouldn't have been kept as a chapter")
		}
	}
}

func TestKomikuRowDate(t *testing.T) {
	newer := komikuRowDate("13/06/2026")
	older := komikuRowDate("14/11/2021")
	if newer <= older {
		t.Errorf("13/06/2026 (%d) should sort after 14/11/2021 (%d)", newer, older)
	}
	// a row without a readable date (the header text, a missing cell, an
	// unexpected format) must lose to any dated row rather than break the parse
	if got := komikuRowDate("Tanggal Rilis"); got != 0 {
		t.Errorf("komikuRowDate(%q) = %d, want 0", "Tanggal Rilis", got)
	}
}

func TestKomikuChapterNumber(t *testing.T) {
	cases := []struct {
		name   string
		label  string
		href   string
		want   float64
		wantOK bool
	}{
		{"label only", "Chapter 1194", "/one-piece-chapter-1194/", 1194, true},
		{"label with decimals", "Chapter 1053.7", "/one-piece-chapter-1053-7/", 1053.7, true},
		// the slug tail is a re-upload marker, not a decimal part, but the
		// label is what settles it: without a label the tail means decimal
		{"no label, slug tail is a decimal part", "", "/one-piece-chapter-1053-7/", 1053.7, true},
		{"no label, plain slug", "", "/one-piece-chapter-500/", 500, true},
		{"absolute href with trailing slash", "Chapter 1", "https://komiku.org/one-piece-chapter-1/", 1, true},
		{"not a chapter link", "whatever", "/manga/one-piece/", 0, false},
	}

	for _, c := range cases {
		got, ok := komikuChapterNumber(c.label, c.href)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%s: number = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestKomikuPages(t *testing.T) {
	pages, err := komikuPages(komikuDoc(t, komikuReaderHTML), "https://komiku.org")
	if err != nil {
		t.Fatalf("komikuPages() error = %v", err)
	}

	want := []string{
		"https://image2.komiku.to/upload5/hitoribocchi/332/1.webp",
		"https://image4.komiku.to/upload5/hitoribocchi/332/2.webp",
		"https://img.komiku.org/upload5/hitoribocchi/332/3.webp",
	}
	if len(pages) != len(want) {
		t.Fatalf("komikuPages() returned %d pages, want %d", len(pages), len(want))
	}
	for i, w := range want {
		if pages[i] != w {
			t.Errorf("page %d = %q, want %q", i, pages[i], w)
		}
	}
}

// TestKomikuPagesFailLoudly is the regression test for the grabber's guard:
// a reader that stops server-rendering pages (or a renamed klazy class) must
// error out rather than ship a truncated chapter
func TestKomikuPagesFailLoudly(t *testing.T) {
	// valueGambar says 3 but the third page never makes it into the HTML
	truncated := strings.Replace(komikuReaderHTML, `src="//img.komiku.org/upload5/hitoribocchi/332/3.webp"`, `src="data:image/gif;base64,R0lGOD"`, 1)
	if _, err := komikuPages(komikuDoc(t, truncated), "https://komiku.org"); err == nil {
		t.Error("komikuPages() with fewer pages than declared should error, got nil")
	}

	// no klazy images at all: only the ads
	noPages := strings.ReplaceAll(komikuReaderHTML, `class="klazy ww"`, `class="not-a-page"`)
	if _, err := komikuPages(komikuDoc(t, noPages), "https://komiku.org"); err == nil {
		t.Error("komikuPages() with no page images should error, got nil")
	}
}

func TestKomikuTest(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://komiku.org/manga/one-piece/", true},
		{"https://komiku.id/manga/one-piece/", true},
		{"https://www.komiku.org/one-piece-chapter-1/", true},
		{"https://example.com/manga/one-piece/", false},
	}

	k := &Komiku{Grabber: &Grabber{}}
	for _, c := range cases {
		k.URL = c.url
		got, err := k.Test()
		if err != nil {
			t.Errorf("Test(%q) error = %v", c.url, err)
			continue
		}
		if got != c.want {
			t.Errorf("Test(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}
