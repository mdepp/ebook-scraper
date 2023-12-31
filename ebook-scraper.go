package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"runtime/pprof"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/gocolly/colly"
	"github.com/gocolly/colly/extensions"
	"github.com/mdepp/go-epub"
	"github.com/schollz/progressbar/v3"
	"go.uber.org/zap"
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

var logger *zap.SugaredLogger

func assembleEpub(book ScrapedBook) (*epub.Epub, error) {
	doc := epub.NewEpub(book.meta.Title)
	doc.SetAuthor(book.meta.Author)

	if book.meta.CoverURL != "" {
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
	}

	bar := progressbar.Default(int64(len(book.toc)))
	defer bar.Finish()
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
	rawLogger, _ := zap.NewDevelopment()
	defer rawLogger.Sync()
	logger = rawLogger.Sugar()

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <URL>\n", os.Args[0])
		flag.PrintDefaults()
	}
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `filename`")
	transport := flag.String("transport", "default", "request transport `backend` [default|curl]")
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}
	baseURL := flag.Arg(0)

	if *cpuprofile != "" {
		logger.Infow("Begin CPU profile", "filename", cpuprofile)
		f, err := os.Create(*cpuprofile)
		if err != nil {
			logger.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	if *transport != "default" && *transport != "curl" {
		logger.Fatal("Transport must be one of default or curl")
	}

	handlers := map[string]Scraper{
		"www.royalroad.com":   scrapeRoyalRoad,
		"phrack.org":          scrapePhrack,
		"www.scribblehub.com": scrapeScribblehub,
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		logger.Fatal(err)
	}
	handler, ok := handlers[parsedURL.Host]
	if !ok {
		logger.Fatalw("No handler for host", "host", parsedURL.Host)
	}

	baseCollector := colly.NewCollector(
		colly.CacheDir(".cache"),
		colly.AllowedDomains(parsedURL.Host),
		func(col *colly.Collector) {
			col.Limit(&colly.LimitRule{DomainGlob: "*", Parallelism: 5})
			logger.Debugw("Set transport backend", "transport", transport)
			if *transport == "curl" {
				col.WithTransport(CurlTransport{})
			}
		},
	)

	logger.Infow("Scrape html", "baseURL", baseURL)
	scrapedBook, err := handler(baseCollector, baseURL)
	if err != nil {
		logger.Fatal(err)
	}
	logger.Infow("Assemble epub", "title", scrapedBook.meta.Title, "chapters", len(scrapedBook.toc))
	doc, err := assembleEpub(scrapedBook)
	if err != nil {
		logger.Fatal(err)
	}
	filename := strings.ToLower(strings.ReplaceAll(doc.Title(), " ", "-")) + ".epub"
	logger.Infow("Write to file", "filename", filename)
	doc.Write(filename)
	logger.Infow("All done")
}

func scrapeRoyalRoad(baseCollector *colly.Collector, baseURL string) (ScrapedBook, error) {
	var meta Metadata
	var toc []TOCEntry
	var chapters = make(map[string]Chapter)

	mainCollector := baseCollector.Clone()
	chapterCollector := mainCollector.Clone()

	setupCommonHandlers(mainCollector)
	setupCommonHandlers(chapterCollector)

	mainCollector.OnHTML("html", func(e *colly.HTMLElement) {
		coverURL := e.Request.AbsoluteURL(e.ChildAttr(".fic-header img[data-type=\"cover\"]", "src"))
		if strings.Contains(coverURL, "/nocover") {
			coverURL = ""
		}
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
	tocSet := mapset.NewSet[string]()
	var chapters = make(map[string]Chapter)

	setupCommonHandlers(baseCollector)
	baseCollector.OnHTML(".tissue a", func(e *colly.HTMLElement) {
		childURL := e.Request.AbsoluteURL(e.Attr("href"))
		if !tocSet.Contains(childURL) {
			toc = append(toc, TOCEntry{URL: childURL})
			tocSet.Add(childURL)
		}
		baseCollector.Visit(childURL)
	})
	baseCollector.OnHTML(".details a", func(e *colly.HTMLElement) {
		childURL := e.Request.AbsoluteURL(e.Attr("href"))
		baseCollector.Visit(childURL)
	})
	baseCollector.OnHTML("body", func(e *colly.HTMLElement) {
		chapterURL := e.Request.URL.String()
		chapterTitle := e.ChildText(".p-title")
		chapterContent := "<pre>" + childHTML(e, "pre") + "</pre>"
		chapters[chapterURL] = Chapter{Title: chapterTitle, Content: chapterContent}
	})
	err := baseCollector.Visit(baseURL)
	if err != nil {
		return ScrapedBook{}, err
	}
	return ScrapedBook{meta, toc, chapters}, nil
}

func scrapeScribblehub(baseCollector *colly.Collector, baseURL string) (ScrapedBook, error) {
	var meta Metadata
	var toc []TOCEntry
	var chapters = make(map[string]Chapter)

	setupCommonHandlers(baseCollector)
	baseCollector.OnHTML("body", func(e *colly.HTMLElement) {
		firstChapterURL := e.ChildAttr(".read_buttons a:first-child", "href")
		if firstChapterURL != "" {
			meta = Metadata{
				Title:       e.ChildText(".fic_title"),
				Author:      e.ChildText(".auth_name_fic"),
				CoverURL:    e.ChildAttr(".fic_image img", "src"),
				Description: childHTML(e, ".wi_fic_desc"),
			}
			baseCollector.Visit(firstChapterURL)
		}
		chapterContent := childHTML(e, ".chp_raw")
		if chapterContent != "" {
			chapterURL := e.Request.URL.String()
			toc = append(toc, TOCEntry{
				URL: chapterURL,
			})
			chapters[chapterURL] = Chapter{
				Title:   e.ChildText(".chapter-title"),
				Content: chapterContent,
			}
		}
		nextChapterURL := e.ChildAttr(".btn-next", "href")
		if nextChapterURL != "" {
			baseCollector.Visit(nextChapterURL)
		}
	})

	err := baseCollector.Visit(baseURL)
	if err != nil {
		return ScrapedBook{}, err
	}
	return ScrapedBook{meta, toc, chapters}, nil
}

func setupCommonHandlers(collector *colly.Collector) {
	extensions.RandomUserAgent(collector)
	collector.OnRequest(func(r *colly.Request) {
		logger.Debugw("Visit", "method", r.Method, "url", r.URL, "headers", r.Headers)
	})
	collector.OnError(func(r *colly.Response, err error) {
		logger.Warnw("Error", "status", r.StatusCode, "request", r.Request, "headers", r.Headers, "error", err)
	})
	collector.OnResponse(func(r *colly.Response) {
		logger.Debugw("Response", "url", r.Request.URL, "status", r.StatusCode)
	})
}

func childHTML(e *colly.HTMLElement, goquerySelector string) string {
	text, err := e.DOM.Find(goquerySelector).Html()
	if err != nil {
		return ""
	}
	return text
}
