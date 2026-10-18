package support

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type Source struct {
	Ref     string  `json:"ref"`
	Kind    string  `json:"kind"`
	Source  string  `json:"source"`
	Section string  `json:"section"`
	ChunkID string  `json:"chunkId"`
	Text    string  `json:"text"`
	Score   float64 `json:"score,omitempty"`
}
type Index struct {
	Chunks    []Source `json:"chunks"`
	Hash      string   `json:"hash"`
	Documents int      `json:"documents"`
}

func BuildIndex(root string) (*Index, error) {
	paths, e := filepath.Glob(root + "/*.md")
	if e != nil || len(paths) == 0 {
		return nil, fmt.Errorf("knowledge base is empty")
	}
	sort.Strings(paths)
	index := &Index{Documents: len(paths)}
	hash := sha256.New()
	for _, p := range paths {
		st, e := os.Lstat(p)
		if e != nil || !st.Mode().IsRegular() || st.Size() > 128<<10 {
			return nil, fmt.Errorf("invalid knowledge document")
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		hash.Write([]byte(filepath.Base(p)))
		hash.Write(b)
		title := "Введение"
		var block []string
		flush := func() {
			text := strings.TrimSpace(strings.Join(block, "\n"))
			if text == "" {
				return
			}
			r := []rune(text)
			for a := 0; a < len(r); a += 1200 {
				z := min(a+1400, len(r))
				t := string(r[a:z])
				id := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Base(p)+title+t)))
				index.Chunks = append(index.Chunks, Source{Kind: "documentation", Source: "knowledge/" + filepath.Base(p), Section: title, ChunkID: id[:16], Text: t})
				if z == len(r) {
					break
				}
			}
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "#") {
				flush()
				block = nil
				title = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
			block = append(block, line)
		}
		flush()
	}
	index.Hash = fmt.Sprintf("%x", hash.Sum(nil))
	return index, nil
}

var stopwords = map[string]bool{}

func init() {
	for _, s := range strings.Fields("почему как что это где мне меня мой моя еще уже для или при the is a an to of у в на с и не по я вы ли за из от") {
		stopwords[s] = true
	}
}
func terms(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(text, "ё", "е")), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	out := []string{}
	for _, w := range words {
		if stopwords[w] || len([]rune(w)) < 2 {
			continue
		}
		if len([]rune(w)) > 5 {
			for _, suffix := range []string{"иями", "ение", "ения", "ами", "ого", "ией", "ий", "ая", "ые", "ой", "ов", "ах", "ом", "ы", "а", "я", "у", "е", "и"} {
				if strings.HasSuffix(w, suffix) {
					w = strings.TrimSuffix(w, suffix)
					break
				}
			}
		}
		out = append(out, w)
	}
	return out
}
func (i *Index) Search(query string) []Source {
	q := terms(query)
	seen := map[string]bool{}
	unique := []string{}
	for _, t := range q {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}
	q = unique
	tf := make([]map[string]int, len(i.Chunks))
	df := map[string]int{}
	length := make([]int, len(tf))
	avg := 0.0
	for n, s := range i.Chunks {
		tf[n] = map[string]int{}
		for _, t := range terms(s.Section + " " + s.Section + " " + s.Text) {
			tf[n][t]++
			length[n]++
		}
		for t := range tf[n] {
			df[t]++
		}
		avg += float64(length[n])
	}
	if avg == 0 {
		return nil
	}
	avg /= float64(len(tf))
	hits := []Source{}
	for n, s := range i.Chunks {
		score := 0.0
		for _, t := range q {
			f := float64(tf[n][t])
			if f > 0 {
				score += math.Log(1+(float64(len(tf)-df[t])+0.5)/(float64(df[t])+0.5)) * f * 2.2 / (f + 1.2*(0.25+0.75*float64(length[n])/avg))
			}
		}
		if score >= 0.6 {
			s.Score = score
			hits = append(hits, s)
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	if len(hits) > 4 {
		hits = hits[:4]
	}
	for n := range hits {
		hits[n].Ref = fmt.Sprintf("D%d", n+1)
	}
	return hits
}
