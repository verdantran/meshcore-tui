package admin

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRegions(t *testing.T) {
	got := ParseRegions("* F\n bristol^ F\n  bath F\n wales\n")
	want := []Region{{"*", 0, false, true}, {"bristol", 1, true, true}, {"bath", 2, false, true}, {"wales", 1, false, false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("regions = %+v", got)
	}
	cut := "* F\n" + strings.Repeat(" r F\n", 31) + " partia"
	if rs := ParseRegions(cut); len(rs) != 32 || rs[len(rs)-1].Name != "r" {
		t.Errorf("a truncated reply drops its partial line: %d, %+v", len(rs), rs[len(rs)-1])
	}
}
