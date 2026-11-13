package service

import (
	"math"
	"testing"
)

func TestRelocationMath(t *testing.T) {
	for _, c := range []struct {
		i, o, n int
		want    int32
	}{{40, 100, 50, 20}, {0, 0, 50, 0}, {1000, 1, 50, 49}, {-1, 100, 50, 0}, {5, 10, 0, 0}, {1, 4, 10, 3}} {
		if got := relocationIndex(c.i, c.o, c.n); got != c.want {
			t.Fatalf("%+v got %d", c, got)
		}
	}
	for _, c := range []struct {
		r    float64
		n    int
		want int32
	}{{.42, 100, 42}, {1, 100, 100}, {2, 100, 100}, {-1, 100, 0}, {.5, 0, 0}, {math.NaN(), 5, 0}} {
		if got := relocationOffset(c.r, c.n); got != c.want {
			t.Fatalf("%+v got %d", c, got)
		}
	}
}
