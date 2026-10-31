package fbgd

import "testing"

func rng(a, b int) []int {
	var out []int
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// r289: create frames (from t0) of five captures -> the JOINKEY=auto fold frame (same cases as the pytest).
func TestJoinFoldFrame(t *testing.T) {
	cases := []struct {
		name   string
		frames []int
		want   int
	}{
		{"42972", append(rng(0, 9), 27, 39), 9},
		{"15884", append([]int{0, 1}, append(rng(3, 10), 21)...), 10},
		{"c182734", append(rng(0, 11), 13, 14, 31, 33, 36), 14},
		{"trial09", []int{0, 2, 3, 5, 7, 8, 37}, 8},
		{"trial05 floor", []int{0, 3}, 5},
		{"cap", rng(0, 30), 19},
		{"empty", nil, 5},
		{"gap exactly 5 empty frames ends", []int{0, 1, 2, 3, 4, 5, 6, 7, 13}, 7},
		{"4 empty frames continue", []int{0, 1, 2, 3, 4, 5, 6, 7, 12}, 12},
		{"unsorted dupes negative", []int{8, -3, 0, 8, 2, 4, 6, 7}, 8},
	}
	for _, c := range cases {
		if got := JoinFoldFrame(c.frames, JoinGap, JoinMin, JoinMax); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
