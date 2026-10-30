package assign

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"nudge/internal/config"
)

// These are experiment-validity tests: if they fail, data collected with the
// current assignment logic can't be trusted.

const (
	nSamples = 100_000
	// Family-wise significance level. Tests are deterministic (fixed IDs), so
	// a pass is a pass forever; alpha only sets how strict "balanced" is.
	alpha = 0.001
	// Max allowed absolute gap between a level's observed and expected share.
	shareTolerance = 0.01
	trialsPerID    = 50
)

func loadConfig(t testing.TB) *config.Config {
	t.Helper()
	c, err := config.Load("../../experiments.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// syntheticIDs returns n reproducible UUIDv4-shaped visitor IDs.
func syntheticIDs(n int) []string {
	r := rand.New(rand.NewPCG(42, 1337))
	ids := make([]string, n)
	for i := range ids {
		a, b := r.Uint64(), r.Uint64()
		ids[i] = fmt.Sprintf("%08x-%04x-4%03x-%04x-%012x",
			a>>32, (a>>16)&0xffff, a&0xfff, (b>>48)&0x3fff|0x8000, b&0xffffffffffff)
	}
	return ids
}

func TestDeterminism(t *testing.T) {
	cfg := loadConfig(t)
	a1, a2 := New(cfg), New(loadConfig(t))
	for _, id := range syntheticIDs(1000) {
		first := a1.Trials(id, 0, 25)
		if again := a1.Trials(id, 0, 25); !reflect.DeepEqual(first, again) {
			t.Fatalf("vid %s: repeated call changed trials", id)
		}
		if other := a2.Trials(id, 0, 25); !reflect.DeepEqual(first, other) {
			t.Fatalf("vid %s: fresh assigner disagrees", id)
		}
		if !reflect.DeepEqual(a1.Visitor(id), a2.Visitor(id)) {
			t.Fatalf("vid %s: visitor levels differ", id)
		}
		// Fetching a block on its own must match the same block fetched in a run.
		if mid := a1.Trials(id, 10, 5); !reflect.DeepEqual(first[10:15], mid) {
			t.Fatalf("vid %s: trials depend on how they were fetched", id)
		}
	}
}

func TestVersionBumpReshuffles(t *testing.T) {
	v1 := loadConfig(t)
	v2 := loadConfig(t)
	v2.Version++
	a1, a2 := New(v1), New(v2)

	f, _ := v1.Factor("color")
	changed, n := 0, 0
	for _, id := range syntheticIDs(10_000) {
		n++
		if a1.index(f, id, 0, 1) != a2.index(f, id, 0, 1) {
			changed++
		}
	}
	// Independent redraws differ with probability 1 - 1/k.
	want := 1 - 1/float64(len(f.Levels))
	if got := float64(changed) / float64(n); math.Abs(got-want) > 0.02 {
		t.Errorf("version bump changed %.3f of assignments, want ≈ %.3f", got, want)
	}
}

func TestTrialShape(t *testing.T) {
	cfg := loadConfig(t)
	a := New(cfg)
	id := syntheticIDs(1)[0]
	for i, tr := range a.Trials(id, 0, 3*cfg.BlockSize) {
		if tr.Index != i || tr.Block != i/cfg.BlockSize {
			t.Fatalf("trial %d: index/block = %d/%d", i, tr.Index, tr.Block)
		}
		for _, f := range cfg.Factors {
			var got []string
			switch f.Scope {
			case config.ScopeTrial:
				got = []string{tr.Trial[f.Name]}
			case config.ScopeOption:
				got = []string{tr.Options[0][f.Name], tr.Options[1][f.Name]}
			case config.ScopeVisitor:
				got = []string{a.Visitor(id)[f.Name]}
			}
			for _, l := range got {
				if !slices.Contains(f.Levels, l) {
					t.Errorf("trial %d: factor %s has invalid level %q", i, f.Name, l)
				}
			}
		}
	}
}

func TestVisitorFactorsFixedAcrossTrials(t *testing.T) {
	cfg := loadConfig(t)
	a := New(cfg)
	for _, f := range cfg.ByScope(config.ScopeVisitor) {
		for _, id := range syntheticIDs(200) {
			want := a.index(f, id, 0, 0)
			for trial := range 30 {
				for slot := range 3 {
					if got := a.index(f, id, trial, slot); got != want {
						t.Fatalf("%s: visitor factor varied with trial/slot", f.Name)
					}
				}
			}
		}
	}
}

// dim is one randomized quantity, e.g. "color of slot 1" or "labels".
type dim struct {
	name   string
	levels int
	at     func(id string, trial int) int
}

// dims enumerates every independently randomized quantity in a trial,
// including the same option factor in the next trial (serial independence).
func dims(a *Assigner, cfg *config.Config) []dim {
	var out []dim
	for _, f := range cfg.Factors {
		switch f.Scope {
		case config.ScopeVisitor:
			out = append(out, dim{f.Name, len(f.Levels), func(id string, _ int) int { return a.index(f, id, 0, 0) }})
		case config.ScopeTrial:
			out = append(out, dim{f.Name, len(f.Levels), func(id string, tr int) int { return a.index(f, id, tr, 0) }})
		case config.ScopeOption:
			for slot := 1; slot <= NumSlots; slot++ {
				out = append(out, dim{fmt.Sprintf("%s/slot%d", f.Name, slot), len(f.Levels),
					func(id string, tr int) int { return a.index(f, id, tr, slot) }})
			}
			out = append(out, dim{f.Name + "/slot1/next_trial", len(f.Levels),
				func(id string, tr int) int { return a.index(f, id, tr+1, 1) }})
		}
	}
	return out
}

// sample i uses a distinct visitor and spreads trial indexes over 0..49, so
// both the visitor and the trial index vary across the sample.
func samples(ids []string) func(i int) (string, int) {
	return func(i int) (string, int) { return ids[i], i % trialsPerID }
}

func TestBalance(t *testing.T) {
	cfg := loadConfig(t)
	a := New(cfg)
	sample := samples(syntheticIDs(nSamples))
	ds := dims(a, cfg)
	perTest := alpha / float64(len(ds)) // Bonferroni

	for _, d := range ds {
		t.Run(d.name, func(t *testing.T) {
			counts := make([]int, d.levels)
			for i := range nSamples {
				counts[d.at(sample(i))]++
			}
			want := 1 / float64(d.levels)
			for l, c := range counts {
				if got := float64(c) / nSamples; math.Abs(got-want) > shareTolerance {
					t.Errorf("level %d share %.4f, want %.4f ± %.2f", l, got, want, shareTolerance)
				}
			}
			if stat, p := chiSquareGOF(counts); p < perTest {
				t.Errorf("chi-square = %.2f, p = %.2g < %.2g: counts %v are not uniform", stat, p, perTest, counts)
			}
		})
	}
}

func TestIndependence(t *testing.T) {
	cfg := loadConfig(t)
	a := New(cfg)
	sample := samples(syntheticIDs(nSamples))
	ds := dims(a, cfg)

	// Precompute each dim's level for every sample.
	vals := make([][]int, len(ds))
	for k, d := range ds {
		vals[k] = make([]int, nSamples)
		for i := range nSamples {
			vals[k][i] = d.at(sample(i))
		}
	}

	pairs := len(ds) * (len(ds) - 1) / 2
	perTest := alpha / float64(pairs)
	for x := range ds {
		for y := x + 1; y < len(ds); y++ {
			dx, dy := ds[x], ds[y]
			t.Run(dx.name+"~"+dy.name, func(t *testing.T) {
				table := make([][]int, dx.levels)
				for r := range table {
					table[r] = make([]int, dy.levels)
				}
				for i := range nSamples {
					table[vals[x][i]][vals[y][i]]++
				}
				if stat, p := chiSquareIndependence(table); p < perTest {
					t.Errorf("chi-square = %.2f, p = %.2g < %.2g: factors are associated", stat, p, perTest)
				}
			})
		}
	}
	t.Logf("checked %d factor pairs at per-pair alpha %.2g", pairs, perTest)
}

// The statistical checks above are only meaningful if they can actually catch
// a broken assignment. These negative controls prove they do.
func TestChiSquareCatchesBrokenAssignment(t *testing.T) {
	t.Run("skewed split", func(t *testing.T) {
		// 51/49 over 100k: a 1-point position-style bias.
		if _, p := chiSquareGOF([]int{51_000, 49_000}); p > 1e-6 {
			t.Errorf("GOF missed a 51/49 skew: p = %.2g", p)
		}
	})
	t.Run("correlated factors", func(t *testing.T) {
		// Second factor copies the first 5% of the time, else independent.
		r := rand.New(rand.NewPCG(7, 7))
		table := [][]int{make([]int, 6), make([]int, 6), make([]int, 6), make([]int, 6), make([]int, 6), make([]int, 6)}
		for range nSamples {
			x := r.IntN(6)
			y := r.IntN(6)
			if r.Float64() < 0.05 {
				y = x
			}
			table[x][y]++
		}
		if _, p := chiSquareIndependence(table); p > 1e-6 {
			t.Errorf("independence test missed a 5%% coupling: p = %.2g", p)
		}
	})
	t.Run("shared salt", func(t *testing.T) {
		// Two factors with the same salt and level count are identical — the
		// reason config validation forbids shared salts.
		cfg := loadConfig(t)
		a := New(cfg)
		f, _ := cfg.Factor("label_order")
		g := f
		g.Name = "label_order_copy"
		table := [][]int{{0, 0}, {0, 0}}
		for _, id := range syntheticIDs(10_000) {
			table[a.index(f, id, 3, 0)][a.index(g, id, 3, 0)]++
		}
		if _, p := chiSquareIndependence(table); p > 1e-6 {
			t.Errorf("shared salt not detected: p = %.2g", p)
		}
	})
}
