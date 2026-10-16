package wager

import "testing"

func TestStatusTransitionsTable(t *testing.T) {
	all := []Status{Pending, PendingReference, Processed, Rejected, Failed}
	allowed := map[Status][]Status{
		Pending:          {PendingReference, Processed, Rejected, Failed},
		PendingReference: {Processed, Rejected, Failed},
	}

	for _, from := range all {
		for _, to := range all {
			want := false
			for _, candidate := range allowed[from] {
				want = want || candidate == to
			}

			if from.canMoveTo(to) != want {
				t.Fatalf("%s -> %s = %v, want %v", from, to, !want, want)
			}
		}
	}
}
