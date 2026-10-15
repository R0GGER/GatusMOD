package badge

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

const (
	Prefix      = "/mod/badges"
	cacheTTL    = 5 * time.Minute
	errorTTL    = 30 * time.Second
	parallel    = 8
	maxLabelLen = 40

	ContentPercentage = "percentage"
	ContentEndpoints  = "percentage-endpoints"
	CheckOverall      = "overall"
)

var (
	Durations = []string{"1h", "24h", "7d", "30d"}
	Styles    = []string{"flat", "flat-square", "plastic"}
	Contents  = []string{ContentPercentage, ContentEndpoints}
)

type Deps struct {
	Config  func() *config.Config
	Records func() []store.Record
	Client  *http.Client
}

// Handler serves /mod/badges/uptimes/{duration}/badge.svg: the average uptime of all endpoints, a group or an import.
// It is public, like the badges of Gatus itself.
type Handler struct {
	deps  Deps
	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	done   chan struct{}
	at     time.Time
	uptime float64
	count  int
	err    error
}

func New(deps Deps) *Handler {
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Handler{deps: deps, cache: map[string]*entry{}}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/uptimes/{duration}/badge.svg", h.uptime)
	mux.HandleFunc("GET "+Prefix+"/endpoints/{key}/health/badge.svg", h.endpoint)
	mux.HandleFunc("GET "+Prefix+"/endpoints/{key}/{kind}/{duration}/badge.svg", h.endpoint)
	return mux
}

// URL builds the badge URL for the given options; empty options use the defaults.
func URL(base, duration, style, content, check string) string {
	q := url.Values{}
	if style != "" && style != Styles[0] {
		q.Set("style", style)
	}
	if content != "" && content != ContentPercentage {
		q.Set("content", content)
	}
	if check != "" && check != CheckOverall {
		q.Set("check", check)
	}
	u := strings.TrimRight(base, "/") + Prefix + "/uptimes/" + duration + "/badge.svg"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (h *Handler) uptime(w http.ResponseWriter, r *http.Request) {
	duration := r.PathValue("duration")
	q := r.URL.Query()
	style := q.Get("style")
	if !contains(Styles, style) {
		style = Styles[0]
	}
	label := q.Get("label")
	if label == "" || len(label) > maxLabelLen {
		label = "uptime " + duration
	}
	if !contains(Durations, duration) {
		writeSVG(w, http.StatusBadRequest, Render(style, "uptime", "invalid duration", colorGrey))
		return
	}
	check := q.Get("check")
	if check == "" {
		check = CheckOverall
	}
	cfg := h.deps.Config()
	e := h.get(r.Context(), cfg, duration, check)
	switch {
	case e.err == errUnknownCheck:
		writeSVG(w, http.StatusNotFound, Render(style, label, "unknown check", colorGrey))
		return
	case e.err != nil || e.count == 0:
		writeSVG(w, http.StatusOK, Render(style, label, "no data", colorGrey))
		return
	}
	msg := Percent(e.uptime)
	if q.Get("content") == ContentEndpoints {
		msg += " · " + strconv.Itoa(e.count) + " endpoints"
	}
	writeSVG(w, http.StatusOK, Render(style, label, msg, Color(e.uptime)))
}

func writeSVG(w http.ResponseWriter, status int, svg []byte) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(status)
	_, _ = w.Write(svg)
}

var errUnknownCheck = fmt.Errorf("unknown check")

// get returns a cached result; concurrent requests for the same badge share one computation.
func (h *Handler) get(ctx context.Context, cfg *config.Config, duration, check string) *entry {
	key := duration + "\x00" + check + "\x00" + privateSignature(cfg)
	h.mu.Lock()
	if e, ok := h.cache[key]; ok {
		select {
		case <-e.done:
			ttl := cacheTTL
			if e.err != nil {
				ttl = errorTTL
			}
			if time.Since(e.at) < ttl {
				h.mu.Unlock()
				return e
			}
		default:
			h.mu.Unlock()
			<-e.done
			return e
		}
	}
	e := &entry{done: make(chan struct{})}
	h.cache[key] = e
	h.mu.Unlock()
	// The computation is shared, so it must not be cancelled when the first client goes away.
	e.uptime, e.count, e.err = h.compute(context.WithoutCancel(ctx), cfg, duration, check)
	e.at = time.Now()
	close(e.done)
	if e.err == errUnknownCheck {
		// Not cached: the check comes from the query string, so caching it would let anybody fill the cache.
		h.mu.Lock()
		delete(h.cache, key)
		h.mu.Unlock()
	}
	return e
}

func (h *Handler) compute(ctx context.Context, cfg *config.Config, duration, check string) (float64, int, error) {
	endpoints, _, _ := List(ctx, h.deps.Client, cfg, h.deps.Records())
	visible := endpoints[:0:0]
	for _, ep := range endpoints {
		if !cfg.HiddenKey(ep.Key, nil) {
			visible = append(visible, ep)
		}
	}
	keys, err := Select(visible, check)
	if err != nil {
		return 0, 0, err
	}
	var (
		mu    sync.Mutex
		sum   float64
		count int
		wg    sync.WaitGroup
		sem   = make(chan struct{}, parallel)
	)
	base := strings.TrimRight(cfg.Upstream, "/")
	for _, key := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer wg.Done()
			defer func() { <-sem }()
			v, ok := h.fetchUptime(ctx, base+"/api/v1/endpoints/"+url.PathEscape(key)+"/uptimes/"+duration)
			if ok {
				mu.Lock()
				sum += v
				count++
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	if count == 0 {
		return 0, 0, nil
	}
	return sum / float64(count), count, nil
}

// privateSignature changes when the endpoints hidden by private status pages change, so a cached badge never
// counts endpoints that became private.
func privateSignature(cfg *config.Config) string {
	var b strings.Builder
	for _, p := range cfg.StatusPages {
		if p.Private() {
			b.WriteString(strings.Join(p.Groups, "\x01") + "\x02" + strings.Join(p.Endpoints, "\x01") + "\x03")
		}
	}
	return b.String()
}

// Select returns the keys of the endpoints a check covers: overall, group:<name> or import:<id>.
func Select(endpoints []Endpoint, check string) ([]string, error) {
	kind, value, _ := strings.Cut(check, ":")
	var keys []string
	for _, ep := range endpoints {
		switch {
		case check == CheckOverall,
			kind == "group" && ep.Group == value,
			kind == "import" && ep.Source == value:
			keys = append(keys, ep.Key)
		}
	}
	if check != CheckOverall && len(keys) == 0 {
		return nil, errUnknownCheck
	}
	return keys, nil
}

func (h *Handler) fetchUptime(ctx context.Context, u string) (float64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false
	}
	resp, err := h.deps.Client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(body)), 64)
	return v, err == nil && v >= 0 && v <= 1
}

const colorGrey = "#9f9f9f"

func Color(uptime float64) string {
	switch {
	case uptime >= 0.999:
		return "#44cc11"
	case uptime >= 0.99:
		return "#97ca00"
	case uptime >= 0.95:
		return "#dfb317"
	case uptime >= 0.90:
		return "#fe7d37"
	}
	return "#e05d44"
}

func Percent(uptime float64) string {
	s := strconv.FormatFloat(uptime*100, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	return s + "%"
}

// textWidth approximates Verdana 11px, the font shields.io badges use.
func textWidth(s string) int {
	w := 0.0
	for _, r := range s {
		switch {
		case strings.ContainsRune("il.,:;|!'· ", r):
			w += 3.6
		case strings.ContainsRune("fjrt()[]-", r):
			w += 4.7
		case r == 'm' || r == 'w' || r == '%' || r == 'M' || r == 'W':
			w += 10
		case r >= 'A' && r <= 'Z':
			w += 7.6
		default:
			w += 6.9
		}
	}
	return int(w + 0.5)
}

// Render draws a shields.io-like badge.
func Render(style, label, message, color string) []byte {
	lw, mw := textWidth(label)+10, textWidth(message)+10
	width := lw + mw
	height, rx, textY := 20, 3, 14
	gradient := `<linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`
	switch style {
	case "flat-square":
		rx, gradient = 0, ""
	case "plastic":
		height, rx, textY = 18, 4, 13
		gradient = `<linearGradient id="s" x2="0" y2="100%"><stop offset="0" stop-color="#fff" stop-opacity=".7"/><stop offset=".1" stop-color="#aaa" stop-opacity=".1"/><stop offset=".9" stop-opacity=".3"/><stop offset="1" stop-opacity=".5"/></linearGradient>`
	}
	l, m := html.EscapeString(label), html.EscapeString(message)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" role="img" aria-label="%s: %s"><title>%s: %s</title>`, width, height, l, m, l, m)
	b.WriteString(gradient)
	fmt.Fprintf(&b, `<clipPath id="r"><rect width="%d" height="%d" rx="%d" fill="#fff"/></clipPath>`, width, height, rx)
	fmt.Fprintf(&b, `<g clip-path="url(#r)"><rect width="%d" height="%d" fill="#555"/><rect x="%d" width="%d" height="%d" fill="%s"/>`, lw, height, lw, mw, height, color)
	if gradient != "" {
		fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="url(#s)"/>`, width, height)
	}
	b.WriteString(`</g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`)
	for _, t := range []struct {
		x    int
		text string
	}{{lw / 2, l}, {lw + mw/2, m}} {
		if gradient != "" {
			fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#010101" fill-opacity=".3">%s</text>`, t.x, textY+1, t.text)
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d">%s</text>`, t.x, textY, t.text)
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
