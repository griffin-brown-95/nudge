// Package stats builds the public results snapshot served at GET /stats.
//
// Rates are computed here from raw counts (not in SQL) so one tested function
// decides both the confidence interval and whether a result is shown at all:
// anything with fewer than MinN trials is reported with its count only.
package stats

import (
	"math"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat/distuv"
)

// MinN is the number of trials a comparison needs before its rate is shown.
const MinN = 30

// BalanceAlpha is the p-value below which a factor's split is flagged as a
// sample ratio mismatch (the randomization or logging is likely broken).
const BalanceAlpha = 0.001

type Snapshot struct {
	Version     int             `json:"version"`
	GeneratedAt time.Time       `json:"generated_at"`
	MinN        int             `json:"min_n"`
	Progress    Progress        `json:"progress"`
	Daily       []Day           `json:"daily"`
	Balance     []FactorBalance `json:"balance"`
	Position    []PositionRate  `json:"position"`
	Attributes  []LevelRate     `json:"attributes"`
	Matchups    []Matchup       `json:"matchups"`
	Decisions   []Decision      `json:"decisions"`
}

type Progress struct {
	Visitors      int64    `json:"visitors"`
	Sessions      int64    `json:"sessions"`
	TrialsShown   int64    `json:"trials_shown"`
	Choices       int64    `json:"choices"`
	ChoicesLast24 int64    `json:"choices_last_24h"`
	Abandonment   *float64 `json:"abandonment"` // share of trials shown >10 min ago with no choice
}

type Day struct {
	Date     string `json:"date"` // YYYY-MM-DD, UTC
	Choices  int64  `json:"choices"`
	Visitors int64  `json:"visitors"`
}

// Rate is a proportion with its 95% Wilson interval, or just the count
// while N < MinN.
type Rate struct {
	N     int64    `json:"n"`
	Hits  int64    `json:"hits"`
	Share *float64 `json:"share"`
	Lo    *float64 `json:"lo"`
	Hi    *float64 `json:"hi"`
}

// NewRate gates and computes a rate. Hits are dropped too while gated, so the
// hidden result can't be recovered from the JSON.
func NewRate(hits, n int64) Rate {
	if n < MinN {
		return Rate{N: n}
	}
	share := float64(hits) / float64(n)
	lo, hi := Wilson(hits, n)
	return Rate{N: n, Hits: hits, Share: &share, Lo: &lo, Hi: &hi}
}

type PositionRate struct {
	Frame  *string `json:"frame"`  // null = all frames
	Layout *string `json:"layout"` // null = all layouts
	Rate           // hits = picked the first-rendered option
}

type LevelRate struct {
	Frame     *string `json:"frame"` // null = all frames
	Attribute string  `json:"attribute"`
	Level     string  `json:"level"`
	Rate              // hits = chosen, among trials where the opponent had a different level
}

type Matchup struct {
	Attribute string   `json:"attribute"`
	Winner    string   `json:"winner"`
	Loser     string   `json:"loser"`
	N         int64    `json:"n"`
	Share     *float64 `json:"winner_share"`
	Lo        *float64 `json:"winner_share_lo"`
	Rank      *int     `json:"rank"` // by Lo, among matchups with N >= MinN
}

// NewMatchups orients each head-to-head so the side with more wins is the
// winner, gates small samples, and ranks the rest by the pessimistic bound.
func NewMatchups(rows []MatchupCounts) []Matchup {
	out := make([]Matchup, 0, len(rows))
	for _, r := range rows {
		m := Matchup{Attribute: r.Attribute, Winner: r.A, Loser: r.B, N: r.N}
		wins := r.AWins
		if r.N-r.AWins > r.AWins {
			m.Winner, m.Loser, wins = r.B, r.A, r.N-r.AWins
		}
		if r.N >= MinN {
			share := float64(wins) / float64(r.N)
			lo, _ := Wilson(wins, r.N)
			m.Share, m.Lo = &share, &lo
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Lo != nil && b.Lo != nil:
			return *a.Lo > *b.Lo
		case a.Lo != nil || b.Lo != nil:
			return a.Lo != nil
		default:
			return a.N > b.N
		}
	})
	rank := 0
	for i := range out {
		if out[i].Lo != nil {
			rank++
			r := rank
			out[i].Rank = &r
		}
	}
	return out
}

type MatchupCounts struct {
	Attribute string
	A, B      string
	N, AWins  int64
}

type Decision struct {
	Frame               *string  `json:"frame"` // null = all frames
	Input               *string  `json:"input"` // null = all inputs
	N                   int64    `json:"n"`
	MedianLatencyMS     *float64 `json:"median_latency_ms"`
	ConsideredBoth      *float64 `json:"considered_both_share"`
	ChangedMind         *float64 `json:"changed_mind_share"`
	FirstConsideredWins *float64 `json:"first_considered_wins"`
}

// Gate hides a decision row's measures while it has too few choices.
func (d Decision) Gate() Decision {
	if d.N < MinN {
		return Decision{Frame: d.Frame, Input: d.Input, N: d.N}
	}
	return d
}

// FactorBalance is the live sample-ratio check for one factor: are levels
// being assigned (and logged) in the equal shares the design expects?
type FactorBalance struct {
	Factor string       `json:"factor"`
	Scope  string       `json:"scope"`
	Levels []LevelCount `json:"levels"`
	Total  int64        `json:"total"`
	Chi2   *float64     `json:"chi2"`
	P      *float64     `json:"p"`
	OK     *bool        `json:"ok"` // null until there's enough data to judge
}

type LevelCount struct {
	Level string `json:"level"`
	N     int64  `json:"n"`
}

// NewBalance runs a chi-square goodness-of-fit test against equal shares.
// It needs at least 5 expected per level to be meaningful.
func NewBalance(factor, scope string, levels []LevelCount) FactorBalance {
	b := FactorBalance{Factor: factor, Scope: scope, Levels: levels}
	for _, l := range levels {
		b.Total += l.N
	}
	k := float64(len(levels))
	exp := float64(b.Total) / k
	if len(levels) < 2 || exp < 5 {
		return b
	}
	var chi2 float64
	for _, l := range levels {
		d := float64(l.N) - exp
		chi2 += d * d / exp
	}
	p := distuv.ChiSquared{K: k - 1}.Survival(chi2)
	ok := p >= BalanceAlpha
	b.Chi2, b.P, b.OK = &chi2, &p, &ok
	return b
}

// Wilson returns the 95% Wilson score interval for hits/n.
func Wilson(hits, n int64) (lo, hi float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.959963984540054
	p := float64(hits) / float64(n)
	nf := float64(n)
	den := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return math.Max(0, center-half), math.Min(1, center+half)
}
