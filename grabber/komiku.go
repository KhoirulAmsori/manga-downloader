// Copyright (C) 2023-2026 Òscar Casajuana Alonso

package grabber

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/elboletaire/manga-downloader/http"
	"github.com/fatih/color"
)

// Komiku is a grabber for komiku.org (komiku.id 301s here), the Indonesian
// site. Everything is server-rendered plain HTML over plain HTTP, no
// Cloudflare, no browser.
//
// What makes a generic selector grabber a poor fit:
//
//   - The series page renders *every* chapter in #Daftar_Chapter (up to ~1200
//     rows, ~500KB), newest first, each row carrying its own itemprop="url"
//     link plus the number the site itself prints. Slugs and labels disagree
//     on re-uploads: /one-piece-chapter-1053-7/ is labelled "Chapter 1053.7"
//     but /one-piece-chapter-1189-2/ is labelled plain "Chapter 1189", so the
//     label wins and the slug is only a fallback.
//   - Reader pages all live in #Baca_Komik with class "klazy", which is the
//     only thing separating them from the three ad/promo <img>s rendered in
//     the same container. Filtering by host instead (as an older scraper did,
//     keeping only img.komiku.org) drops about half the pages: they are
//     round-robined over image2..image9.komiku.to and img.komiku.org, each
//     with an onerror fallback to the other.
//   - .chapterInfo states the chapter's own page count in valueGambar, so a
//     reader that stops server-rendering pages (or a renamed class) fails
//     loudly instead of silently shipping a truncated chapter.
type Komiku struct {
	*Grabber
	title string
	// series caches the large series page shared by FetchTitle and
	// FetchChapters, so the two together fetch it only once
	series *goquery.Document
}

// KomikuChapter represents a Komiku chapter
type KomikuChapter struct {
	Chapter
	// URL is the chapter's reader URL: links are site-root-relative in the
	// chapter table, resolved here against the site's host
	URL string
}

func NewKomiku(g *Grabber) *Komiku {
	return &Komiku{Grabber: g}
}

// Test returns true if the URL is a komiku.org URL
func (k *Komiku) Test() (bool, error) {
	re := regexp.MustCompile(`komiku\.(org|id)`)
	return re.MatchString(k.URL), nil
}

// FetchTitle fetches and returns the manga title
func (k *Komiku) FetchTitle() (string, error) {
	if k.title != "" {
		return k.title, nil
	}

	doc, err := k.seriesPage()
	if err != nil {
		return "", err
	}

	title := komikuSeriesTitle(doc)
	if title == "" {
		return "", fmt.Errorf("could not find the series title in %s", k.URL)
	}

	k.title = sanitizeTitle(title)

	return k.title, nil
}

// FetchChapters returns the chapters of the manga. The whole list is already
// server-rendered in the series page (no pagination, no ajax, no "load more"),
// so this reuses the document FetchTitle fetched.
func (k *Komiku) FetchChapters() (Filterables, []error) {
	doc, err := k.seriesPage()
	if err != nil {
		return nil, []error{err}
	}

	chapters, err := komikuChapters(doc, k.BaseUrl())
	if err != nil {
		return nil, []error{err}
	}

	return chapters, nil
}

// komikuChapterDateRe matches a chapter row's release date (dd/mm/yyyy, the
// only format the table ever prints)
var komikuChapterDateRe = regexp.MustCompile(`^(\d{2})/(\d{2})/(\d{4})$`)

// komikuRowDate turns a chapter row's release date into an integer that
// compares correctly, for picking between two rows claiming the same chapter
// number. Rows without a readable date return 0, so they lose to any dated
// row: the ordinal isn't a day count, it just has to sort right (the 31-day
// month and 372-day year paddings keep months from overflowing into each
// other).
func komikuRowDate(text string) int {
	m := komikuChapterDateRe.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) != 4 {
		return 0
	}
	day, errDay := strconv.Atoi(m[1])
	month, errMonth := strconv.Atoi(m[2])
	year, errYear := strconv.Atoi(m[3])
	if errDay != nil || errMonth != nil || errYear != nil {
		return 0
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0
	}

	return year*372 + month*31 + day
}

// FetchChapter fetches a chapter and its pages
func (k *Komiku) FetchChapter(f Filterable) (*Chapter, error) {
	kchap, ok := f.(*KomikuChapter)
	if !ok {
		return nil, fmt.Errorf("unexpected chapter type %T", f)
	}

	body, err := http.Get(http.RequestParams{
		URL:     kchap.URL,
		Referer: k.BaseUrl(),
	})
	if err != nil {
		return nil, err
	}
	defer body.Close()

	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, err
	}

	pages, err := komikuPages(doc, k.BaseUrl())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kchap.URL, err)
	}

	chapter := &Chapter{
		Title:      f.GetTitle(),
		Number:     f.GetNumber(),
		Language:   "id",
		PagesCount: int64(len(pages)),
	}
	for i, page := range pages {
		chapter.Pages = append(chapter.Pages, Page{
			Number: int64(i + 1),
			URL:    page,
		})
	}

	return chapter, nil
}

// seriesPage fetches and caches the series page: FetchTitle and FetchChapters
// both need it, and for long series it is a ~500KB document
func (k *Komiku) seriesPage() (*goquery.Document, error) {
	if k.series != nil {
		return k.series, nil
	}

	body, err := http.Get(http.RequestParams{URL: k.URL})
	if err != nil {
		return nil, err
	}
	defer body.Close()

	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, err
	}

	k.series = doc

	return doc, nil
}

// komikuSeriesTitle returns the title of a series page. The series name sits
// in the #Judul header as an itemprop="name" span, already free of the site's
// own "Komik " prefix and " - Komiku" suffix, which the <title>/og:title
// fallbacks below still carry (the same header renders "<series> Chapter N" on
// reader pages, hence the #Judul scope)
func komikuSeriesTitle(doc *goquery.Document) string {
	if title := strings.TrimSpace(doc.Find(`#Judul h1 span[itemprop="name"]`).First().Text()); title != "" {
		return title
	}

	og, _ := doc.Find(`meta[property="og:title"]`).Attr("content")
	title := strings.TrimSpace(og)
	if title == "" {
		title = strings.TrimSuffix(strings.TrimSpace(doc.Find("title").First().Text()), " - Komiku")
	}

	return strings.TrimPrefix(title, "Komik ")
}

// komikuChapterRows is the chapter table's rows: every one of them carries the
// schema.org list markup, the chapter link and the label the site prints
const komikuChapterRows = `#Daftar_Chapter tr[itemprop="itemListElement"]`

// komikuChapterLabelRe matches the number the site prints in a chapter row,
// i.e. "Chapter 1194" or "Chapter 101.5" (any trailing title is ignored)
var komikuChapterLabelRe = regexp.MustCompile(`(?i)^chapter\s+(\d+(?:\.\d+)?)`)

// komikuChapterSlugRe matches a chapter link's last path segment, i.e.
// "one-piece-chapter-1053-7" -> "one-piece", "1053", "7". The optional
// trailing "-N" is a re-upload/part suffix (see komikuChapterNumber)
var komikuChapterSlugRe = regexp.MustCompile(`^(.+)-chapter-(\d+)(?:-(\d+))?/?$`)

// komikuChapters parses the series page's chapter table, resolving its
// root-relative links against base
func komikuChapters(doc *goquery.Document, base string) (Filterables, error) {
	rows := doc.Find(komikuChapterRows)
	if rows.Length() == 0 {
		return nil, fmt.Errorf("no chapters found: %s matched nothing, the chapter table may have been renamed", komikuChapterRows)
	}

	// A re-uploaded chapter shows up as a second row claiming the same
	// number: normally under a distinct label ("Chapter 1053.7" next to plain
	// "Chapter 1053"), but a re-upload of a chapter that never got a part
	// number repeats the very same label ("Chapter 196." next to "Chapter
	// 196", a copy re-uploaded five years later). Keep the row the site dates
	// the newest: rows are listed newest-first, so an undated tie keeps the
	// first one it saw, and a later dated upload replaces an older one.
	type komikuRow struct {
		chapter *KomikuChapter
		date    int
	}
	positions := map[float64]int{}
	parsed := []komikuRow{}
	unparsed := 0
	rows.Each(func(_ int, row *goquery.Selection) {
		href := row.Find(`a[itemprop="url"]`).First().AttrOr("href", "")
		label := sanitizeTitle(row.Find(`span[itemprop="name"]`).First().Text())

		number, ok := komikuChapterNumber(label, href)
		if !ok {
			// skipped rather than fatal: one row the number regex can't
			// read (a "Chapter SP" extra, say) must not lose the other
			// thousand, but it is worth saying out loud
			unparsed++
			return
		}

		title := label
		if title == "" {
			title = "Chapter " + formatChapterNumber(number)
		}

		candidate := komikuRow{
			chapter: &KomikuChapter{
				Chapter: Chapter{
					Number:   number,
					Title:    title,
					Language: "id",
				},
				URL: komikuAbsURL(base, href),
			},
			date: komikuRowDate(row.Find(".tanggalseries").First().Text()),
		}

		pos, dup := positions[number]
		if !dup {
			positions[number] = len(parsed)
			parsed = append(parsed, candidate)
			return
		}
		if candidate.date > parsed[pos].date {
			parsed[pos] = candidate
		}
	})

	if len(parsed) == 0 {
		return nil, fmt.Errorf("none of the %d chapter rows could be parsed", rows.Length())
	}
	if unparsed > 0 {
		color.Yellow("- komiku: skipped %d chapter rows whose number couldn't be read (%s)", unparsed, komikuChapterRows)
	}

	chapters := make(Filterables, 0, len(parsed))
	for _, p := range parsed {
		chapters = append(chapters, p.chapter)
	}

	return chapters, nil
}

// komikuChapterNumber reads a chapter's number out of its row. The label is
// the site's own truth and wins over the URL slug: they disagree on
// re-uploads, where a slug tail like "-chapter-1189-2" reads as 1189.2 while
// the row says plain "Chapter 1189". The slug is only used for rows without a
// label, where the tail does mean a decimal part ("…-chapter-1053-7" is
// chapter 1053.7).
func komikuChapterNumber(label, href string) (float64, bool) {
	if m := komikuChapterLabelRe.FindStringSubmatch(strings.TrimSpace(label)); len(m) == 2 {
		if number, err := strconv.ParseFloat(m[1], 64); err == nil {
			return number, true
		}
	}

	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return 0, false
	}
	segment := strings.TrimSuffix(u.Path, "/")
	if i := strings.LastIndex(segment, "/"); i >= 0 {
		segment = segment[i+1:]
	}

	m := komikuChapterSlugRe.FindStringSubmatch(segment)
	if len(m) != 4 {
		return 0, false
	}
	number, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return 0, false
	}
	if m[3] != "" {
		fraction, err := strconv.ParseFloat("0."+m[3], 64)
		if err != nil {
			return 0, false
		}
		number += fraction
	}

	return number, true
}

// komikuAbsURL resolves a link the site serves root-relative against the
// site's host, leaving absolute links untouched
func komikuAbsURL(base, href string) string {
	href = strings.TrimSpace(href)
	switch {
	case href == "":
		return ""
	case strings.HasPrefix(href, "http"):
		return href
	default:
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(href, "/")
	}
}

// komikuReaderSelector picks the reader's page images. Everything the site
// renders inside the reader box shares it, and only the real pages carry the
// klazy class: the promo, the premium banner and the "next chapter" spinner
// don't, so they're excluded without needing to know their paths
const komikuReaderSelector = `#Baca_Komik img.klazy`

// komikuPages returns the chapter's page image URLs, in reading order (the
// reader is server-rendered top to bottom, no lazy placeholders)
func komikuPages(doc *goquery.Document, base string) ([]string, error) {
	pages := []string{}
	doc.Find(komikuReaderSelector).Each(func(_ int, s *goquery.Selection) {
		if src := komikuSrc(base, s); src != "" {
			pages = append(pages, src)
		}
	})

	if len(pages) == 0 {
		return nil, fmt.Errorf("no page images found (%s): the chapter may be paywalled or the reader markup changed", komikuReaderSelector)
	}

	// valueGambar is the site's own page count for the chapter being read;
	// comparing against it turns a partly-matching selector into an error
	// instead of a quietly truncated download
	if declared := komikuDeclaredPages(doc); declared > 0 && declared != len(pages) {
		return nil, fmt.Errorf("the reader declares %d pages but only %d were found in the HTML", declared, len(pages))
	}

	return pages, nil
}

// komikuSrc resolves an image tag's source, falling back to data-src and
// handling the protocol-relative and root-relative forms the CDN uses
func komikuSrc(base string, s *goquery.Selection) string {
	src := strings.TrimSpace(s.AttrOr("src", ""))
	if src == "" || strings.HasPrefix(src, "data:") {
		src = strings.TrimSpace(s.AttrOr("data-src", ""))
	}
	switch {
	case src == "" || strings.HasPrefix(src, "data:"):
		return ""
	case strings.HasPrefix(src, "//"):
		return "https:" + src
	default:
		return komikuAbsURL(base, src)
	}
}

// komikuDeclaredPages reads the reader's own page count (valueGambar on the
// first .chapterInfo block, which describes the chapter this HTML renders;
// further blocks would belong to infiniscrolled chapters that aren't here).
// The HTML parser lowercases attribute names, and this one doesn't start
// lowercase, so both spellings are tried.
func komikuDeclaredPages(doc *goquery.Document) int {
	info := doc.Find(".chapterInfo").First()
	value := info.AttrOr("valuegambar", "")
	if value == "" {
		value = info.AttrOr("valueGambar", "")
	}

	pages, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}

	return pages
}
