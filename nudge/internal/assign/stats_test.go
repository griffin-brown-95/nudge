package assign

import "gonum.org/v1/gonum/stat/distuv"

// chiSquareGOF tests whether counts are consistent with every category being
// equally likely. A small p-value means the split is too uneven to be chance.
func chiSquareGOF(counts []int) (stat, p float64) {
	n := 0
	for _, c := range counts {
		n += c
	}
	exp := float64(n) / float64(len(counts))
	for _, c := range counts {
		d := float64(c) - exp
		stat += d * d / exp
	}
	return stat, distuv.ChiSquared{K: float64(len(counts) - 1)}.Survival(stat)
}

// chiSquareIndependence tests whether rows and columns of a contingency table
// are unrelated. A small p-value means knowing one predicts the other.
func chiSquareIndependence(table [][]int) (stat, p float64) {
	rows, cols := len(table), len(table[0])
	rowSum := make([]float64, rows)
	colSum := make([]float64, cols)
	var n float64
	for i := range table {
		for j, c := range table[i] {
			rowSum[i] += float64(c)
			colSum[j] += float64(c)
			n += float64(c)
		}
	}
	for i := range table {
		for j, c := range table[i] {
			exp := rowSum[i] * colSum[j] / n
			d := float64(c) - exp
			stat += d * d / exp
		}
	}
	df := float64((rows - 1) * (cols - 1))
	return stat, distuv.ChiSquared{K: df}.Survival(stat)
}
