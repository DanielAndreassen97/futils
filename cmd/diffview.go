package cmd

import "strings"

// DiffLine is one line of a unified diff. Op is ' ' (context), '-' (only in
// old), or '+' (only in new).
type DiffLine struct {
	Op   byte
	Text string
}

// unifiedLineDiff computes a line-level diff of old→new using a longest-common-
// subsequence over lines, emitting context/removed/added lines in order. At a
// divergence, removed lines precede added lines.
func unifiedLineDiff(oldText, newText string) []DiffLine {
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")

	// LCS length table.
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, DiffLine{' ', a[i]})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			out = append(out, DiffLine{'-', a[i]})
			i++
		} else {
			out = append(out, DiffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{'+', b[j]})
	}
	return out
}
