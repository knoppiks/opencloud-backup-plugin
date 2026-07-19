package jobs

import "testing"

func TestJobKindsDistinct(t *testing.T) {
	if KindBackup == KindPrune || KindPrune == KindRestore || KindBackup == KindRestore {
		t.Fatal("job kinds must be distinct")
	}
}
