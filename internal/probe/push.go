package probe

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatusmod/internal/config"
)

const (
	PushPrefix   = "/mod/push/"
	maxPushBody  = 64 << 10
	pushInterval = time.Second
)

// PushURL is the public URL a script calls to report the status of a push endpoint.
func PushURL(cfg *config.Config, baseURL, key string) string {
	return strings.TrimRight(baseURL, "/") + PushPrefix + cfg.PushToken(key)
}

// PushHandler serves GET/POST /mod/push/{token}?status=up|down&msg=...&ping=<ms> without login.
// The token identifies the endpoint; one push per endpoint per second is accepted.
func (r *Runner) PushHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "use GET or POST", http.StatusMethodNotAllowed)
			return
		}
		j := r.pushJob(strings.TrimPrefix(req.URL.Path, PushPrefix))
		if j == nil {
			http.Error(w, "unknown push token", http.StatusNotFound)
			return
		}
		req.Body = http.MaxBytesReader(w, req.Body, maxPushBody)
		if err := req.ParseForm(); err != nil {
			http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
			return
		}
		success, ok := parseStatus(req.Form.Get("status"))
		if !ok {
			http.Error(w, "status must be up or down", http.StatusBadRequest)
			return
		}
		msg := truncate(strings.TrimSpace(req.Form.Get("msg")), maxErrorLength)
		var d time.Duration
		if p := req.Form.Get("ping"); p != "" {
			f, err := strconv.ParseFloat(p, 64)
			if err != nil || f < 0 {
				http.Error(w, "ping must be the response time in milliseconds", http.StatusBadRequest)
				return
			}
			d = time.Duration(f * float64(time.Millisecond))
		}

		now := time.Now()
		j.mu.Lock()
		if now.Sub(j.lastPush) < pushInterval {
			j.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many pushes, at most one per second", http.StatusTooManyRequests)
			return
		}
		j.lastPush = now
		j.mu.Unlock()

		errText := ""
		if !success {
			errText = msg
			if errText == "" {
				errText = "reported down"
			}
		}
		err := r.push(req.Context(), j.target.Key, success, errText, d)
		values := Values{}
		if msg != "" {
			values["MSG"] = msg
		}
		j.mu.Lock()
		j.status.Last = Result{Time: now, Success: success, Error: errText, Duration: d, Values: values}
		j.mu.Unlock()
		j.setPushError(err)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
}

func (r *Runner) pushJob(token string) *job {
	if token == "" {
		return nil
	}
	cfg := r.cfg()
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, j := range r.jobs {
		if j.target.Push() && subtle.ConstantTimeCompare([]byte(cfg.PushToken(key)), []byte(token)) == 1 {
			return j
		}
	}
	return nil
}

func parseStatus(s string) (success, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "up", "ok", "true", "1", "success":
		return true, true
	case "down", "fail", "failed", "false", "0", "error":
		return false, true
	}
	return false, false
}
