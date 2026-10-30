package stats

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestWilson(t *testing.T) {
	tests := []struct {
		hits, n int64
		lo, hi  float64
	}{
		{7, 10, 0.3968, 0.8922},   // textbook value
		{0, 10, 0, 0.2775},        // never below 0
		{10, 10, 0.7225, 1},       // never above 1
		{50, 100, 0.4038, 0.5962}, // symmetric around one half
	}
	for _, tc := range tests {
		lo, hi := Wilson(tc.hits, tc.n)
		if !near(lo, tc.lo) || !near(hi, tc.hi) {
			t.Errorf("Wilson(%d, %d) = [%.4f, %.4f], want [%.4f, %.4f]", tc.hits, tc.n, lo, hi, tc.lo, tc.hi)
		}
	}
	if lo, hi := Wilson(0, 0); lo != 0 || hi != 1 {
		t.Errorf("Wilson(0, 0) = [%v, %v], want [0, 1]", lo, hi)
	}
}

func TestRateGating(t *testing.T) {
	small := NewRate(20, MinN-1)
	if small.Share != nil || small.Lo != nil || small.Hits != 0 || small.N != MinN-1 {
		t.Errorf("below MinN must expose only N, got %+v", small)
	}
	big := NewRate(20, MinN)
	if big.Share == nil || !near(*big.Share, 20.0/MinN) || big.Hits != 20 || *big.Lo >= *big.Share || *big.Hi <= *big.Share {
		t.Errorf("at MinN want share with interval around it, got %+v", big)
	}
}

func TestMatchups(t *testing.T) {
	got := NewMatchups([]MatchupCounts{
		{Attribute: "color", A: "blue", B: "red", N: 40, AWins: 10},   // red wins 30/40
		{Attribute: "size", A: "base", B: "plus8", N: 100, AWins: 60}, // base wins 60/100
		{Attribute: "color", A: "gray", B: "green", N: 5, AWins: 5},   // 5-0 but too small
		{Attribute: "emphasis", A: "soft", B: "solid", N: 300, AWins: 90},
	})
	if got[0].Winner != "solid" || *got[0].Rank != 1 {
		t.Errorf("most one-sided by lower bound should be solid (210/300), got %+v", got[0])
	}
	if got[1].Winner != "red" || got[1].Loser != "blue" || *got[1].Rank != 2 {
		t.Errorf("red should beat blue and rank 2, got %+v", got[1])
	}
	last := got[len(got)-1]
	if last.Winner != "gray" || last.Rank != nil || last.Share != nil {
		t.Errorf("a 5-0 sample must be unranked and hidden, got %+v", last)
	}
}

func TestBalance(t *testing.T) {
	fair := NewBalance("frame", "trial", []LevelCount{{"tiles", 5020}, {"split", 4980}})
	if fair.OK == nil || !*fair.OK {
		t.Errorf("50.2/49.8 over 10k should pass, got %+v", fair)
	}
	broken := NewBalance("frame", "trial", []LevelCount{{"tiles", 5600}, {"split", 4400}})
	if broken.OK == nil || *broken.OK {
		t.Errorf("56/44 over 10k is a sample ratio mismatch, got p=%v", *broken.P)
	}
	tiny := NewBalance("prime", "visitor", []LevelCount{{"none", 3}, {"gut", 1}, {"careful", 0}})
	if tiny.OK != nil || tiny.Total != 4 {
		t.Errorf("too little data must be undecided, got %+v", tiny)
	}
}

func TestDecisionGate(t *testing.T) {
	m := 900.0
	d := Decision{N: 3, MedianLatencyMS: &m}.Gate()
	if d.MedianLatencyMS != nil || d.N != 3 {
		t.Errorf("small decision rows must be hidden, got %+v", d)
	}
	if d := (Decision{N: MinN, MedianLatencyMS: &m}).Gate(); d.MedianLatencyMS == nil {
		t.Error("rows at MinN must be shown")
	}
}
