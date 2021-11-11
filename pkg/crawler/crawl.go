package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/html/atom"

	"github.com/s0rg/crawley/pkg/client"
	"github.com/s0rg/crawley/pkg/links"
	"github.com/s0rg/crawley/pkg/robots"
	"github.com/s0rg/crawley/pkg/set"
)

type crawlClient interface {
	Get(context.Context, string) (io.ReadCloser, error)
	Head(context.Context, string) (http.Header, error)
}

const (
	nMID = 64
	nBIG = nMID * 2

	crawlTimeout  = 5 * time.Second
	robotsTimeout = 3 * time.Second
	contentType   = "Content-Type"
	contentHTML   = "text/html"
)

type task struct {
	URI   *url.URL
	Crawl bool
	Done  bool
}

// Crawler holds crawling process config and state.
type Crawler struct {
	cfg      *config
	wg       sync.WaitGroup
	handleCh chan string
	crawlCh  chan *url.URL
	taskCh   chan task
	robots   *robots.TXT
}

// New creates Crawler instance.
func New(opts ...Option) (c *Crawler) {
	cfg := &config{}

	for _, o := range opts {
		o(cfg)
	}

	cfg.validate()

	return &Crawler{cfg: cfg}
}

// Run starts crawling process for given base uri.
func (c *Crawler) Run(uri string, fn func(string)) (err error) {
	var base *url.URL

	if base, err = url.Parse(uri); err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	n := (c.cfg.Workers + c.cfg.Depth + 1)
	c.handleCh = make(chan string, n*nMID)
	c.crawlCh = make(chan *url.URL, n*nMID)
	c.taskCh = make(chan task, n*nBIG)

	defer c.close()

	seen := make(set.U64)
	seen.Add(urlHash(base))

	web := client.New(c.cfg.UserAgent, c.cfg.Workers, c.cfg.SkipSSL)
	c.initRobots(base, web)

	for i := 0; i < c.cfg.Workers; i++ {
		go c.crawler(web)
	}

	c.wg.Add(c.cfg.Workers)

	go c.handler(fn)

	c.crawlCh <- base

	var t task

	for w := 1; w > 0; {
		t = <-c.taskCh

		switch {
		case t.Done:
			w--
		case seen.Add(urlHash(t.URI)):
			if c.crawl(base, &t) {
				w++
			}

			if !c.cfg.SkipDirs || isResorce(t.URI.Path) {
				c.handleCh <- t.URI.String()
			}
		}
	}

	return nil
}

func (c *Crawler) DumpConfig() string {
	return c.cfg.String()
}

func (c *Crawler) crawl(b *url.URL, t *task) (yes bool) {
	if !t.Crawl {
		return
	}

	if !canCrawl(b, t.URI, c.cfg.Depth) {
		return
	}

	if c.cfg.Robots == RobotsRespect && c.robots.Forbidden(t.URI.Path) {
		return
	}

	go func(u *url.URL) { c.crawlCh <- u }(t.URI)

	return true
}

func (c *Crawler) close() {
	close(c.crawlCh)
	c.wg.Wait() // wait for crawlers

	c.wg.Add(1)
	close(c.handleCh)
	c.wg.Wait() // wait for handler

	close(c.taskCh)
}

func (c *Crawler) initRobots(host *url.URL, web crawlClient) {
	c.robots = robots.AllowALL()

	if c.cfg.Robots == RobotsIgnore {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), robotsTimeout)
	defer cancel()

	body, err := web.Get(ctx, robots.URL(host))
	if err != nil {
		var herr client.HTTPError

		if !errors.As(err, &herr) {
			log.Println("[-] GET /robots.txt:", err)

			return
		}

		if herr.Code() == 500 {
			c.robots = robots.DenyALL()
		}

		return
	}

	defer body.Close()

	rbt, err := robots.FromReader(c.cfg.UserAgent, body)
	if err != nil {
		log.Println("[-] parse robots.txt:", err)

		return
	}

	c.robots = rbt

	c.crawlRobots(host)
}

func (c *Crawler) crawlRobots(host *url.URL) {
	base := *host
	base.Fragment = ""
	base.RawQuery = ""

	for _, u := range c.robots.Links() {
		t := base
		t.Path = u

		c.linkHandler(atom.A, &t)
	}

	for _, u := range c.robots.Sitemaps() {
		if t, e := url.Parse(u); e == nil {
			c.linkHandler(atom.A, t)
		}
	}
}

func (c *Crawler) linkHandler(a atom.Atom, u *url.URL) {
	t := task{URI: u}

	switch a {
	case atom.A, atom.Iframe:
		t.Crawl = true
	}

	c.taskCh <- t
}

func (c *Crawler) crawler(web crawlClient) {
	defer c.wg.Done()

	for uri := range c.crawlCh {
		if c.cfg.Delay > 0 {
			time.Sleep(c.cfg.Delay)
		}

		ctx, cancel := context.WithTimeout(context.Background(), crawlTimeout)
		us := uri.String()

		var parse bool

		if hdrs, err := web.Head(ctx, us); err != nil {
			log.Printf("[-] HEAD %s: %v", us, err)
		} else if typ, _, perr := mime.ParseMediaType(hdrs.Get(contentType)); perr == nil {
			parse = typ == contentHTML
		}

		if parse {
			if body, err := web.Get(ctx, us); err != nil {
				log.Printf("[-] GET %s: %v", us, err)
			} else {
				links.Extract(uri, body, c.cfg.Brute, c.linkHandler)
			}
		}

		cancel()

		c.taskCh <- task{Done: true}
	}
}

func (c *Crawler) handler(fn func(string)) {
	for s := range c.handleCh {
		fn(s)
	}

	c.wg.Done()
}
