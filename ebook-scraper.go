package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/bmaupin/go-epub"
	"github.com/gocolly/colly"
	"github.com/schollz/progressbar/v3"
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

type ScrapedBook struct {
	meta     Metadata
	toc      []TOCEntry
	chapters map[string]Chapter
}

type Scraper = func(*colly.Collector, string) (ScrapedBook, error)

func assembleEpub(book ScrapedBook) (*epub.Epub, error) {
	doc := epub.NewEpub(book.meta.Title)
	doc.SetAuthor(book.meta.Author)
	coverImage, err := doc.AddImage(book.meta.CoverURL, "cover")
	if err != nil {
		return nil, err
	}
	coverCSS, err := doc.AddCSS("assets/cover.css", "")
	if err != nil {
		return nil, err
	}
	doc.SetCover(coverImage, coverCSS)
	doc.SetDescription(book.meta.Description)

	bar := progressbar.Default(int64(len(book.toc)), "Assemble epub")
	for _, tocEntry := range book.toc {
		bar.Add(1)
		chapter := book.chapters[tocEntry.URL]
		_, err := doc.AddSection(chapter.Content, chapter.Title, "", "")
		if err != nil {
			return nil, err
		}
	}

	return doc, nil
}

func main() {
	flag.Usage = func() {
		fmt.Printf("Usage: %s <URL>\n", os.Args[0])
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}
	baseURL := flag.Arg(0)

	handlers := map[string]Scraper{
		"www.royalroad.com": scrapeRoyalRoad,
		"phrack.org":        scrapePhrack,
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		fmt.Println("Invalid url:", err)
		os.Exit(1)
	}
	handler, ok := handlers[parsedURL.Host]
	if !ok {
		fmt.Println("No handler available for url")
		os.Exit(1)
	}

	baseCollector := colly.NewCollector(
		colly.CacheDir(".cache"),
		colly.AllowedDomains(parsedURL.Host),
		func(col *colly.Collector) {
			col.Limit(&colly.LimitRule{DomainGlob: "*", Parallelism: 5})
		},
	)

	scrapedBook, err := handler(baseCollector, baseURL)
	if err != nil {
		fmt.Println("Scraping failed: {}", err)
	}
	doc, err := assembleEpub(scrapedBook)
	if err != nil {
		fmt.Println("Assembling failed: {}", err)
	}

	filename := strings.ToLower(strings.ReplaceAll(doc.Title(), " ", "-")) + ".epub"
	doc.Write(filename)
	fmt.Println("Wrote to", filename)
}

func scrapeRoyalRoad(baseCollector *colly.Collector, baseURL string) (ScrapedBook, error) {
	var meta Metadata
	var toc []TOCEntry
	var chapters = make(map[string]Chapter)

	mainCollector := baseCollector.Clone()
	chapterCollector := mainCollector.Clone()

	logVisits(mainCollector)
	logVisits(chapterCollector)

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
		chapterTitle := e.ChildText(".fic-header h1")
		chapterContent := "<h2>" + chapterTitle + "</h2>" + childHTML(e, ".chapter-content")
		chapters[chapterURL] = Chapter{
			Title:   chapterTitle,
			Content: chapterContent,
		}
	})

	err := mainCollector.Visit(baseURL)
	if err != nil {
		return ScrapedBook{}, err
	}
	return ScrapedBook{meta, toc, chapters}, nil
}

func scrapePhrack(baseCollector *colly.Collector, baseURL string) (ScrapedBook, error) {
	meta := Metadata{
		Title: "Phrack Magazine", CoverURL: "http://phrack.org/images/phrack-logo.jpg",
	}
	var toc []TOCEntry
	var chapters = make(map[string]Chapter)

	logVisits(baseCollector)
	baseCollector.OnHTML(".details a", func(e *colly.HTMLElement) {
		childURL := e.Request.AbsoluteURL(e.Attr("href"))
		baseCollector.Visit(childURL)
	})
	baseCollector.OnHTML(".tissue a", func(e *colly.HTMLElement) {
		childURL := e.Request.AbsoluteURL(e.Attr("href"))
		toc = append(toc, TOCEntry{URL: childURL})
		baseCollector.Visit(childURL)
	})
	baseCollector.OnHTML("body", func(e *colly.HTMLElement) {
		chapterURL := e.Request.URL.String()
		chapterTitle := e.ChildText(".p-title")
		chapterContent := "<pre>" + e.ChildText("pre") + "</pre>"
		chapters[chapterURL] = Chapter{Title: chapterTitle, Content: chapterContent}
	})
	err := baseCollector.Visit(baseURL)
	if err != nil {
		return ScrapedBook{}, err
	}
	return ScrapedBook{meta, toc, chapters}, nil
}

func logVisits(collector *colly.Collector) {
	collector.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL)
	})
}

func childHTML(e *colly.HTMLElement, goquerySelector string) string {
	text, err := e.DOM.Find(goquerySelector).Html()
	if err != nil {
		return ""
	}
	return text
}
