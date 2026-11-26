package advisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type LocalHTTP struct {
	root    string
	client  *http.Client
	metrics *Metrics
}

type SnapshotGuard struct {
	Graph
	Required string
}

func (g SnapshotGuard) Catalog(ctx context.Context) (Catalog, error) {
	out, err := g.Graph.Catalog(ctx)
	if err == nil && out.Version != g.Required {
		return Catalog{}, Changed
	}
	return out, err
}

func (g SnapshotGuard) Check(ctx context.Context, id string) (CourseCheck, error) {
	out, err := g.Graph.Check(ctx, id)
	if err == nil && out.Version != g.Required {
		return CourseCheck{}, Changed
	}
	return out, err
}

func NewLocalHTTP(base string, timeout time.Duration) (*LocalHTTP, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("expected an explicit IPv4 loopback HTTP port")
	}
	transport := &http.Transport{MaxConnsPerHost: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &LocalHTTP{root: strings.TrimSuffix(base, "/"), client: &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (h *LocalHTTP) Observe(m *Metrics) { h.metrics = m }

func (h *LocalHTTP) exchange(ctx context.Context, path string, in, out any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, h.client.Timeout)
	defer cancel()
	if h.metrics != nil {
		h.metrics.dependencyRequests.Add(1)
		defer func() {
			if err != nil {
				h.metrics.dependencyErrors.Add(1)
			}
		}()
	}
	id, _ := randomID(8)
	retry, err := h.once(ctx, path, in, out, id)
	if err == nil || !retry || in != nil {
		return err
	}
	timer := time.NewTimer(time.Duration(24+rand.IntN(25)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		_, err = h.once(ctx, path, in, out, id)
		return err
	}
}

func (h *LocalHTTP) once(ctx context.Context, path string, in, out any, id string) (bool, error) {
	var body io.Reader
	method := http.MethodGet
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return false, err
		}
		method, body = http.MethodPost, bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.root+path, body)
	if err != nil {
		return false, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", id)
	res, err := h.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504, Dependency
	}
	return false, decodeLimit(res.Body, out, 1024*1024)
}

func decodeLimit(r io.Reader, out any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return Invalid
	}
	return json.Unmarshal(data, out)
}

func (h *LocalHTTP) Catalog(ctx context.Context) (Catalog, error) {
	var answer struct {
		Version string `json:"snapshot_version"`
		Data    struct {
			Items []Course `json:"items"`
			Total int      `json:"total"`
		} `json:"data"`
	}
	if err := h.exchange(ctx, "/api/v1/courses?limit=100", nil, &answer); err != nil {
		return Catalog{}, Dependency
	}
	if answer.Data.Total != len(answer.Data.Items) {
		return Catalog{}, CatalogSize
	}
	return Catalog{answer.Version, answer.Data.Items}, nil
}

func (h *LocalHTTP) Check(ctx context.Context, id string) (CourseCheck, error) {
	if !idRE.MatchString(id) {
		return CourseCheck{}, Invalid
	}
	var answer struct {
		Version string `json:"snapshot_version"`
		Policy  string `json:"policy"`
		Data    struct {
			Course   Course    `json:"course"`
			Required *[]Course `json:"all_prerequisites"`
		} `json:"data"`
	}
	err := h.exchange(ctx, "/api/v1/prerequisites/"+url.PathEscape(id)+"?policy=required&path_limit=0", nil, &answer)
	if err != nil || answer.Policy != "required" || answer.Data.Course.ID != id || answer.Data.Required == nil {
		return CourseCheck{}, Dependency
	}
	out := CourseCheck{Version: answer.Version, Required: []string{}}
	for _, c := range *answer.Data.Required {
		out.Required = append(out.Required, c.ID)
	}
	return out, nil
}

type Corpus struct {
	Rows  []Evidence
	mu    sync.Mutex
	cache map[string]cachedEvidence
}

type cachedEvidence struct {
	until time.Time
	rows  []Evidence
}

func validEvidence(row Evidence) bool {
	return idRE.MatchString(row.DocID) && row.Version != "" && bounded(row.Version, 80) && row.Section != "" && bounded(row.Section, 100) &&
		row.Source != "" && bounded(row.Source, 140) && row.Excerpt != "" && bounded(row.Excerpt, 600)
}

func LoadCorpus(file string, fallback []Evidence) (*Corpus, error) {
	rows := fallback
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err = decodeLimit(f, &rows, 1024*1024); err != nil {
			return nil, err
		}
	}
	if len(rows) > 200 {
		return nil, Invalid
	}
	for _, row := range rows {
		if !validEvidence(row) {
			return nil, Invalid
		}
	}
	return &Corpus{Rows: rows, cache: make(map[string]cachedEvidence)}, nil
}

func (c *Corpus) Find(ctx context.Context, query string) ([]Evidence, string, error) {
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return strings.ContainsRune(" ,，、；;/\t\n", r) })
	query = strings.Join(words, " ")
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.cache[query]; ok && time.Now().Before(v.until) {
		return append([]Evidence(nil), v.rows...), "LOCAL_CORPUS", nil
	}
	seen := map[string]bool{}
	rows := []Evidence{}
	for _, row := range c.Rows {
		row.Score = 0
		for _, word := range words {
			if word != "" && !seen[word] && strings.Contains(strings.ToLower(row.Section+" "+row.Excerpt), word) {
				row.Score++
			}
			seen[word] = true
		}
		clear(seen)
		if row.Score > 0 {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	if len(rows) > 4 {
		rows = rows[:4]
	}
	if len(c.cache) >= 128 {
		clear(c.cache)
	}
	c.cache[query] = cachedEvidence{time.Now().Add(time.Minute), rows}
	return append([]Evidence(nil), rows...), "LOCAL_CORPUS", nil
}

type RetrievalAdapter struct {
	Remote   *LocalHTTP
	Fallback EvidenceSource
}

func (a RetrievalAdapter) Find(ctx context.Context, q string) ([]Evidence, string, error) {
	if a.Remote != nil {
		var response struct {
			Evidence []Evidence `json:"evidence"`
			Mode     string     `json:"mode"`
		}
		err := a.Remote.exchange(ctx, "/evidence", map[string]string{"query": q}, &response)
		if err == nil && len(response.Evidence) > 0 && len(response.Evidence) <= 4 &&
			(response.Mode == "PYTHON_BM25" || response.Mode == "PYTHON_FAISS_BM25_RRF") {
			ok := true
			for _, e := range response.Evidence {
				ok = ok && validEvidence(e)
			}
			if ok {
				return response.Evidence, response.Mode, nil
			}
		}
	}
	if a.Fallback != nil {
		return a.Fallback.Find(ctx, q)
	}
	return nil, "", nil
}

type LocalNarrator struct {
	HTTP  *LocalHTTP
	Model string
}

func (n LocalNarrator) Explain(ctx context.Context, selected []Course) (string, *int, error) {
	// No profile, course name, document, or free text crosses the model boundary.
	in := map[string]any{"model": n.Model, "stream": false, "options": map[string]int{"num_predict": 48},
		"prompt": "Choose one explanation focus for an advisory plan. No facts are needed. Reply with exactly PREREQUISITES or CREDITS."}
	var result struct {
		Response   string `json:"response"`
		Prompt     int    `json:"prompt_eval_count"`
		Completion int    `json:"eval_count"`
	}
	if err := n.HTTP.exchange(ctx, "/api/generate", in, &result); err != nil {
		return "", nil, err
	}
	var text string
	switch strings.TrimSpace(result.Response) {
	case "PREREQUISITES":
		text = "已按提交的已修课程核对必需先修。开课与选课资格仍需确认。"
	case "CREDITS":
		text = "已按本次输入核对学分和门数。该结果不代表学校选课许可。"
	default:
		return "", nil, Invalid
	}
	count := result.Prompt + result.Completion
	if count <= 0 {
		return text, nil, nil
	}
	return text, &count, nil
}
