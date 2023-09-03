package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/bmaupin/go-epub"
	"github.com/gocolly/colly"
)

type TOCEntry struct {
	URL string
}

type Chapter struct {
	Title   string
	Content string
}

type Metadata struct {
	Title       string
	Author      string
	CoverURL    string
	Description string
}

func childHTML(e *colly.HTMLElement, goquerySelector string) string {
	text, err := e.DOM.Find(goquerySelector).Html()
	if err != nil {
		return ""
	}
	return text
}

func scrapeRoyalRoad(baseCollector *colly.Collector, baseURL string) (*epub.Epub, error) {
	var meta Metadata
	var toc []TOCEntry
	var chapters = make(map[string]Chapter)

	mainCollector := baseCollector.Clone()
	chapterCollector := mainCollector.Clone()

	mainCollector.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL)
	})

	chapterCollector.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL)
	})

	mainCollector.OnHTML("html", func(e *colly.HTMLElement) {
		coverURL := e.ChildAttr(".fic-header img[data-type=\"cover\"]", "src")
		meta = Metadata{
			Title:       e.ChildText(".fic-title h1"),
			Author:      e.ChildText(".fic-title h4 a"),
			CoverURL:    strings.ReplaceAll(coverURL, "covers-full", "covers-large"),
			Description: childHTML(e, ".description .hidden-content"),
		}
	})

	mainCollector.OnHTML("#chapters", func(e *colly.HTMLElement) {
		e.ForEach("tr td:nth-child(1) a", func(index int, anchor *colly.HTMLElement) {
			chapterURL := e.Request.AbsoluteURL(anchor.Attr("href"))
			toc = append(toc, TOCEntry{URL: chapterURL})
			chapterCollector.Visit(chapterURL)
		})
	})

	chapterCollector.OnHTML("html", func(e *colly.HTMLElement) {
		chapterURL := e.Request.URL.String()
		chapters[chapterURL] = Chapter{
			Title:   e.ChildText(".fic-header h1"),
			Content: childHTML(e, ".chapter-content"),
		}
	})

	mainCollector.Visit(baseURL)

	doc := epub.NewEpub(meta.Title)
	doc.SetAuthor(meta.Author)
	coverImage, err := doc.AddImage(meta.CoverURL, "cover")
	if err != nil {
		return nil, err
	}
	coverCSS, err := doc.AddCSS("assets/cover.css", "")
	if err != nil {
		return nil, err
	}
	doc.SetCover(coverImage, coverCSS)
	doc.SetDescription(meta.Description)

	for _, tocEntry := range toc {
		chapter := chapters[tocEntry.URL]
		chapterPrelude := "<h2>" + chapter.Title + "</h2>"
		_, err := doc.AddSection(chapterPrelude+chapter.Content, chapter.Title, "", "")
		if err != nil {
			return nil, err
		}
	}

	return doc, nil
}

func main() {
	flag.Usage = func() {
		fmt.Println("Usage: ./ebook-scraper <URL>")
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}
	baseURL := flag.Arg(0)

	baseCollector := colly.NewCollector(
		colly.CacheDir(".cache"),
		colly.AllowedDomains("www.royalroad.com"),
		func(col *colly.Collector) {
			col.Limit(&colly.LimitRule{DomainGlob: "*", Parallelism: 5})
		},
	)

	doc, err := scrapeRoyalRoad(baseCollector, baseURL)
	if err != nil {
		fmt.Println("Scraping failed: {}", err)
	}

	filename := strings.ToLower(strings.ReplaceAll(doc.Title(), " ", "-")) + ".epub"
	doc.Write(filename)
	fmt.Println("Wrote to", filename)
}
