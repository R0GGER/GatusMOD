package badge

import (
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Kinds of endpoint badges; health has no duration.
const (
	KindHealth        = "health"
	KindUptime        = "uptimes"
	KindResponseTimes = "response-times"
)

var (
	gatusFill = regexp.MustCompile(`<path fill="(#[0-9a-fA-F]{3,8})"`)
	gatusText = regexp.MustCompile(`<text x="[^"]*" y="[^"]*">\s*([^<]*?)\s*</text>`)

	errBadgeNotFound = errors.New("not found")
)

// EndpointURL is the URL of a badge of one endpoint. Gatus draws its badges in the flat style only, so the
// other styles are served by GatusMOD, which redraws the badge of Gatus.
func EndpointURL(base, key, kind, duration, style string) string {
	path := "/" + kind
	if kind != KindHealth {
		path += "/" + duration
	}
	base = strings.TrimRight(base, "/")
	if style == "" || style == Styles[0] || !contains(Styles, style) {
		return base + "/api/v1/endpoints/" + key + path + "/badge.svg"
	}
	return base + Prefix + "/endpoints/" + key + path + "/badge.svg?style=" + url.QueryEscape(style)
}

// endpoint serves /mod/badges/endpoints/{key}/health/badge.svg and .../{kind}/{duration}/badge.svg.
func (h *Handler) endpoint(w http.ResponseWriter, r *http.Request) {
	key, kind, duration := r.PathValue("key"), r.PathValue("kind"), r.PathValue("duration")
	style := r.URL.Query().Get("style")
	if !contains(Styles, style) {
		style = Styles[0]
	}
	path := "/" + KindHealth
	if kind != "" {
		if (kind != KindUptime && kind != KindResponseTimes) || !contains(Durations, duration) {
			writeLiveSVG(w, http.StatusBadRequest, Render(style, "badge", "invalid badge", colorGrey))
			return
		}
		path = "/" + kind + "/" + duration
	}
	cfg := h.deps.Config()
	if cfg.HiddenKey(key, nil) {
		writeLiveSVG(w, http.StatusNotFound, Render(style, "badge", "not found", colorGrey))
		return
	}
	u := strings.TrimRight(cfg.Upstream, "/") + "/api/v1/endpoints/" + url.PathEscape(key) + path + "/badge.svg"
	label, message, color, err := h.fetchGatusBadge(r.Context(), u)
	switch {
	case errors.Is(err, errBadgeNotFound):
		writeLiveSVG(w, http.StatusNotFound, Render(style, "badge", "not found", colorGrey))
	case err != nil:
		writeLiveSVG(w, http.StatusBadGateway, Render(style, "badge", "no data", colorGrey))
	default:
		writeLiveSVG(w, http.StatusOK, Render(style, label, message, color))
	}
}

// fetchGatusBadge reads the label, message and color of a Gatus badge, so the colors and the response-time
// thresholds of Gatus stay the same in every style.
func (h *Handler) fetchGatusBadge(ctx context.Context, u string) (label, message, color string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := h.deps.Client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", "", "", errBadgeNotFound
	case resp.StatusCode != http.StatusOK:
		return "", "", "", errors.New(resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", "", "", err
	}
	fills := gatusFill.FindAllSubmatch(body, -1)
	texts := gatusText.FindAllSubmatch(body, -1)
	if len(fills) < 2 || len(texts) < 2 {
		return "", "", "", errors.New("unexpected badge from Gatus")
	}
	return html.UnescapeString(string(texts[0][1])), html.UnescapeString(string(texts[1][1])), string(fills[1][1]), nil
}

// writeLiveSVG is not cached, like the badges of Gatus: a health badge must show the current state.
func writeLiveSVG(w http.ResponseWriter, status int, svg []byte) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(status)
	_, _ = w.Write(svg)
}
