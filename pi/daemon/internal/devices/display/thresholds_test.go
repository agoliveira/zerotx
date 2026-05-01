package display

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSerializeThresholds_Nil(t *testing.T) {
	got := serializeThresholds(nil)
	want := "DISP THRESHOLDS"
	if got != want {
		t.Errorf("nil thresholds: got %q, want %q", got, want)
	}
}

func TestSerializeThresholds_Empty(t *testing.T) {
	got := serializeThresholds(&Thresholds{})
	want := "DISP THRESHOLDS"
	if got != want {
		t.Errorf("empty thresholds: got %q, want %q", got, want)
	}
}

func TestSerializeThresholds_Battery(t *testing.T) {
	got := serializeThresholds(&Thresholds{
		Battery: &BatteryThresholds{WarnV: 14.4, CritV: 13.6, MinV: 12.8, FullV: 16.8},
	})
	want := "DISP THRESHOLDS bat_warn=14.40 bat_crit=13.60 bat_min=12.80 bat_full=16.80"
	if got != want {
		t.Errorf("battery only:\n got: %s\nwant: %s", got, want)
	}
}

func TestSerializeThresholds_AltitudeAndDistance(t *testing.T) {
	got := serializeThresholds(&Thresholds{
		Altitude: &AltitudeThresholds{WarnM: 700, CritM: 900},
		Distance: &DistanceThresholds{WarnM: 7000, CritM: 9000},
	})
	want := "DISP THRESHOLDS alt_warn=700 alt_crit=900 dist_warn=7000 dist_crit=9000"
	if got != want {
		t.Errorf("alt+dist:\n got: %s\nwant: %s", got, want)
	}
}

func TestSerializeThresholds_Link(t *testing.T) {
	got := serializeThresholds(&Thresholds{
		Link: &LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 70, LQCritPct: 50},
	})
	want := "DISP THRESHOLDS rssi_warn=-90 rssi_crit=-100 lq_warn=70 lq_crit=50"
	if got != want {
		t.Errorf("link only:\n got: %s\nwant: %s", got, want)
	}
}

func TestSerializeThresholds_FlightTime(t *testing.T) {
	got := serializeThresholds(&Thresholds{
		FlightTime: &FlightTimeThresholds{WarnS: 600, CritS: 900},
	})
	want := "DISP THRESHOLDS time_warn=600 time_crit=900"
	if got != want {
		t.Errorf("flight time only:\n got: %s\nwant: %s", got, want)
	}
}

func TestSerializeThresholds_AllDomains(t *testing.T) {
	got := serializeThresholds(&Thresholds{
		Battery:    &BatteryThresholds{WarnV: 14.4, CritV: 13.6, MinV: 12.8, FullV: 16.8},
		Altitude:   &AltitudeThresholds{WarnM: 700, CritM: 900},
		Distance:   &DistanceThresholds{WarnM: 7000, CritM: 9000},
		Link:       &LinkThresholds{RSSIWarnDBM: -90, RSSICritDBM: -100, LQWarnPct: 70, LQCritPct: 50},
		FlightTime: &FlightTimeThresholds{WarnS: 600, CritS: 900},
	})
	wantTokens := []string{
		"bat_warn=14.40", "bat_crit=13.60", "bat_min=12.80", "bat_full=16.80",
		"alt_warn=700", "alt_crit=900",
		"dist_warn=7000", "dist_crit=9000",
		"rssi_warn=-90", "rssi_crit=-100", "lq_warn=70", "lq_crit=50",
		"time_warn=600", "time_crit=900",
	}
	if !strings.HasPrefix(got, "DISP THRESHOLDS ") {
		t.Fatalf("missing prefix: %s", got)
	}
	for _, tok := range wantTokens {
		if !strings.Contains(got, tok) {
			t.Errorf("missing token %q in: %s", tok, got)
		}
	}
}

func TestDriverEndToEnd_SetThresholds(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second,
		QueueSize:    16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	d.SetThresholds(&Thresholds{
		Battery:  &BatteryThresholds{WarnV: 14.4, CritV: 13.6, MinV: 12.8, FullV: 16.8},
		Altitude: &AltitudeThresholds{WarnM: 700, CritM: 900},
	})
	time.Sleep(50 * time.Millisecond)

	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()
	if !strings.Contains(out, "DISP THRESHOLDS bat_warn=14.40 bat_crit=13.60 bat_min=12.80 bat_full=16.80 alt_warn=700 alt_crit=900\n") {
		t.Errorf("expected threshold line in output:\n%s", out)
	}
}

func TestDriverEndToEnd_SetThresholdsNil(t *testing.T) {
	p := newPipe()
	d := New(p, Config{
		SnapshotRate: 1 * time.Second,
		QueueSize:    16,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	d.SetThresholds(nil)
	time.Sleep(50 * time.Millisecond)

	cancel()
	d.Close()
	<-runDone

	out := p.daemonOutput()
	if !strings.Contains(out, "DISP THRESHOLDS\n") {
		t.Errorf("expected bare DISP THRESHOLDS clear line:\n%s", out)
	}
}
