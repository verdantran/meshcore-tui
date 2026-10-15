package sim

import (
	"context"
	"testing"

	"meshcore-tui/internal/radio"
)

func TestSimDevice(t *testing.T) {
	s := New(4).Fast()
	ctx := context.Background()
	d := s.Device()
	st, _ := d.Settings(ctx)
	if st.Self.Name != "MeshCore-k4" || st.MaxTxPower != 22 || st.Scope != "bristol" || len(st.RepeatFreqs) != 2 {
		t.Fatalf("settings = %+v", st)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.SetName(ctx, "k4-portable"))
	must(d.SetLocation(ctx, 51.45, -2.59))
	must(d.SetRadio(ctx, radio.RadioParams{FreqKHz: 869500, BwHz: 250000, SF: 11, CR: 5, Repeat: true}))
	must(d.SetTxPower(ctx, 14))
	must(d.SetTuning(ctx, radio.Tuning{RxDelay: 10, AirtimeFactor: 2}))
	must(d.SetOther(ctx, radio.Other{ManualAdd: true}))
	must(d.SetPathHashMode(ctx, 1))
	must(d.SetDefaultScope(ctx, ""))
	must(d.SetPin(ctx, 0))
	must(d.SetCustomVar(ctx, "gps", "1"))
	must(d.ImportKey(ctx, [64]byte{9}))
	must(d.SendAdvert(ctx, true))
	must(d.SyncClock(ctx))
	st, _ = d.Settings(ctx)
	if st.Self.Name != "k4-portable" || st.Self.Lat != 51.45 || st.Self.FreqKHz != 869500 || !st.Radio.Repeat || st.Self.TxPower != 14 ||
		st.Tuning.RxDelay != 10 || !st.Other.ManualAdd || st.PathHashMode != 1 || st.Scope != "" || st.Pin != 0 {
		t.Errorf("after edits = %+v", st)
	}
	if k, _ := d.ExportKey(ctx); k[0] != 9 {
		t.Error("imported key")
	}
	if vs, _ := d.CustomVars(ctx); vs[0].Value != "1" {
		t.Errorf("vars = %+v", vs)
	}
	for _, err := range []error{
		d.SetRadio(ctx, radio.RadioParams{FreqKHz: 868000, Repeat: true}), d.SetTxPower(ctx, 30), d.SetPin(ctx, 12),
		d.SetCustomVar(ctx, "nope", "1"),
	} {
		if err == nil {
			t.Error("bad edit accepted")
		}
	}
	if sto, _ := d.Storage(ctx); sto.TotalKB == 0 {
		t.Error("storage")
	}
	if c, err := d.Clock(ctx); err != nil || c.IsZero() {
		t.Error("clock")
	}
	a, _ := d.Stats(ctx)
	if a.Recv == 0 || a.Uptime == 0 {
		t.Errorf("stats = %+v", a)
	}

	d.Reboot(ctx)
	waitFor[radio.Lost](t, s)
	waitFor[radio.Restored](t, s)
	d.FactoryReset(ctx)
	waitFor[radio.Restored](t, s)
	if st, _ := d.Settings(ctx); st.Self.Name != "MeshCore-k4" {
		t.Error("factory reset restores the defaults")
	}
}
