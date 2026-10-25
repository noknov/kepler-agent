package memory

import (
	"math"
	"testing"
)

func TestNormalizedVectorUsesStableScaling(t *testing.T) {
	for _, input := range [][]float64{{math.MaxFloat64, math.MaxFloat64}, {math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}, {3, 4}} {
		vector, err := normalizedVector(input)
		if err != nil {
			t.Fatal(err)
		}
		norm := vector[0]*vector[0] + vector[1]*vector[1]
		if math.Abs(norm-1) > 1e-14 {
			t.Fatal("invalid normalized vector", vector)
		}
	}
	for _, input := range [][]float64{nil, {0, 0}, {math.Inf(1), 0}, {math.NaN(), 1}} {
		if _, err := normalizedVector(input); err == nil {
			t.Fatal("accepted invalid vector", input)
		}
	}
}
