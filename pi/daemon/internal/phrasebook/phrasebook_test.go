package phrasebook

import "testing"

func TestResolveLang(t *testing.T) {
	cases := map[string]string{
		"":     "en",
		"en":   "en",
		"EN":   "en",
		"  pt": "pt",
		"pt":   "pt",
		"fr":   "en", // unknown
		"jp":   "en",
	}
	for in, want := range cases {
		if got := resolveLang(in); got != want {
			t.Errorf("resolveLang(%q): got %q want %q", in, got, want)
		}
	}
}

func TestBootGreeting(t *testing.T) {
	cases := []struct {
		lang, model, want string
	}{
		{"en", "", "ZeroTX online. Awaiting model."},
		{"en", "Big Talon", "ZeroTX online. Big Talon ready."},
		{"pt", "", "ZeroTX online. Aguardando modelo."},
		{"pt", "Big Talon", "ZeroTX online. Big Talon pronto."},
		{"fr", "", "ZeroTX online. Awaiting model."}, // fallback to en
	}
	for _, c := range cases {
		if got := BootGreeting(c.lang, c.model); got != c.want {
			t.Errorf("BootGreeting(%q,%q): got %q want %q", c.lang, c.model, got, c.want)
		}
	}
}

func TestJoystickEvents(t *testing.T) {
	if got := JoystickDisconnected("pt"); got != "Joystick desconectado." {
		t.Errorf("pt: got %q", got)
	}
	if got := JoystickReconnected("pt"); got != "Joystick reconectado." {
		t.Errorf("pt: got %q", got)
	}
	if got := JoystickDisconnected("en"); got != "Joystick disconnected." {
		t.Errorf("en: got %q", got)
	}
}

func TestFlightMode(t *testing.T) {
	if got := FlightMode("en", "ANGL"); got != "Flight mode: angle." {
		t.Errorf("en/ANGL: got %q", got)
	}
	if got := FlightMode("pt", "ANGL"); got != "Modo de voo: ângulo." {
		t.Errorf("pt/ANGL: got %q", got)
	}
	if got := FlightMode("pt", "RTH"); got != "Modo de voo: retorno para casa." {
		t.Errorf("pt/RTH: got %q", got)
	}
	// Unknown FC mode falls through to lowercase.
	if got := FlightMode("en", "WTF"); got != "Flight mode: wtf." {
		t.Errorf("en unknown: got %q", got)
	}
}

func TestDurationPhrase(t *testing.T) {
	cases := []struct {
		lang string
		sec  int
		want string
	}{
		{"en", 1, "1 second"},
		{"en", 5, "5 seconds"},
		{"en", 60, "1 minute"},
		{"en", 61, "1 minute 1 second"},
		{"en", 252, "4 minutes 12 seconds"},
		{"pt", 1, "1 segundo"},
		{"pt", 5, "5 segundos"},
		{"pt", 60, "1 minuto"},
		{"pt", 61, "1 minuto 1 segundo"},
		{"pt", 252, "4 minutos 12 segundos"},
	}
	for _, c := range cases {
		if got := DurationPhrase(c.lang, c.sec); got != c.want {
			t.Errorf("%s/%d: got %q want %q", c.lang, c.sec, got, c.want)
		}
	}
}

func TestPreflightBattery(t *testing.T) {
	if got := PreflightBattery("en", 4, 16.2, 78); got != "Battery 4 cell, 16.2 volts, 78 percent." {
		t.Errorf("en full: got %q", got)
	}
	if got := PreflightBattery("pt", 4, 16.2, 78); got != "Bateria 4 células, 16.2 volts, 78 por cento." {
		t.Errorf("pt full: got %q", got)
	}
	if got := PreflightBattery("en", 0, 0, 0); got != "" {
		t.Errorf("empty: got %q want empty", got)
	}
}

func TestPostFlightHeader(t *testing.T) {
	if got := PostFlightHeader("en"); got != "Flight complete." {
		t.Errorf("en: got %q", got)
	}
	if got := PostFlightHeader("pt"); got != "Voo concluído." {
		t.Errorf("pt: got %q", got)
	}
}
