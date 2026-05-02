// Package phrasebook produces localized TTS phrases for ZeroTX
// announcements. All text spoken by the daemon's TTS pipeline goes
// through this package so adding a language is a one-stop edit and
// no hardcoded strings drift across the codebase.
//
// Design:
//
//   - Stateless: every function takes its inputs and returns a string.
//   - Lang-keyed: each phrase has one implementation per supported
//     language. Unknown languages fall back to "en".
//   - Numbers stay numerals (not spelled out). Piper handles them
//     in the target language pronunciation natively.
//
// To add a new language: add the lang tag to supportedLangs and add
// a case in each phrase function. To add a new phrase: add a function
// with the en/pt cases and call it from the daemon code.
package phrasebook

import (
	"fmt"
	"strings"
)

// supportedLangs is the set of language tags this phrasebook knows
// about. Used by the resolver to fall back gracefully when the
// caller passes an unknown lang.
var supportedLangs = map[string]bool{
	"en": true,
	"pt": true,
}

// resolveLang normalizes the input and returns a known language.
// Empty / unknown / whitespace input returns "en".
func resolveLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if supportedLangs[l] {
		return l
	}
	return "en"
}

// === Boot / model / joystick chrome ===

// BootGreeting: with model name (preferred) or generic awaiting form.
func BootGreeting(lang, modelName string) string {
	lang = resolveLang(lang)
	modelName = strings.TrimSpace(modelName)
	if modelName != "" {
		switch lang {
		case "pt":
			return "ZeroTX online. " + modelName + " pronto."
		}
		return "ZeroTX online. " + modelName + " ready."
	}
	switch lang {
	case "pt":
		return "ZeroTX online. Aguardando modelo."
	}
	return "ZeroTX online. Awaiting model."
}

// ModelLoaded: with name (preferred) or generic.
func ModelLoaded(lang, modelName string) string {
	lang = resolveLang(lang)
	modelName = strings.TrimSpace(modelName)
	if modelName != "" {
		switch lang {
		case "pt":
			return "Modelo carregado: " + modelName + "."
		}
		return "Model loaded: " + modelName + "."
	}
	switch lang {
	case "pt":
		return "Modelo carregado."
	}
	return "Model loaded."
}

// ModelUnloaded: with name (preferred) or generic.
func ModelUnloaded(lang, modelName string) string {
	lang = resolveLang(lang)
	modelName = strings.TrimSpace(modelName)
	if modelName != "" {
		switch lang {
		case "pt":
			return "Modelo " + modelName + " descarregado."
		}
		return "Model " + modelName + " unloaded."
	}
	switch lang {
	case "pt":
		return "Modelo descarregado."
	}
	return "Model unloaded."
}

func JoystickDisconnected(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Joystick desconectado."
	}
	return "Joystick disconnected."
}

func JoystickReconnected(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Joystick reconectado."
	}
	return "Joystick reconnected."
}

// === Flight modes ===

// FlightMode: prefix + humanized mode. Used for mode-change
// announcements and the periodic mode field.
func FlightMode(lang, fcMode string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Modo de voo: " + HumanizeMode(lang, fcMode) + "."
	}
	return "Flight mode: " + HumanizeMode(lang, fcMode) + "."
}

// HumanizeMode maps an FC-emitted mode code to a localized natural
// string. Unknown modes fall through to lowercase as-is so the
// operator hears something readable.
func HumanizeMode(lang, m string) string {
	lang = resolveLang(lang)
	switch lang {
	case "pt":
		switch m {
		case "ANGL":
			return "ângulo"
		case "HORI":
			return "horizonte"
		case "MANU":
			return "manual"
		case "ACRO":
			return "acro"
		case "AIR":
			return "modo aéreo"
		case "NAV":
			return "navegação"
		case "RTH":
			return "retorno para casa"
		case "WP", "MISS":
			return "missão de waypoints"
		case "LAUN":
			return "lançamento"
		case "PASS":
			return "passagem direta"
		case "FS":
			return "failsafe"
		case "ALTH":
			return "manter altitude"
		case "POSH":
			return "manter posição"
		case "CRUISE":
			return "cruzeiro"
		case "STAB":
			return "estabilizado"
		case "OK":
			return "pronto"
		case "WAIT":
			return "aguardando armar"
		}
		return strings.ToLower(m)
	}
	switch m {
	case "ANGL":
		return "angle"
	case "HORI":
		return "horizon"
	case "MANU":
		return "manual"
	case "ACRO":
		return "acro"
	case "AIR":
		return "air mode"
	case "NAV":
		return "navigation"
	case "RTH":
		return "return to home"
	case "WP", "MISS":
		return "waypoint mission"
	case "LAUN":
		return "launch"
	case "PASS":
		return "passthrough"
	case "FS":
		return "failsafe"
	case "ALTH":
		return "altitude hold"
	case "POSH":
		return "position hold"
	case "CRUISE":
		return "cruise"
	case "STAB":
		return "stabilized"
	case "OK":
		return "ready"
	case "WAIT":
		return "waiting for arming"
	}
	return strings.ToLower(m)
}

// === Pre-flight summary ===

// PreflightModelHeader: "Big Talon." (just the name + period). Empty
// when modelName is empty so the caller can omit the fragment.
func PreflightModelHeader(lang, modelName string) string {
	if strings.TrimSpace(modelName) == "" {
		return ""
	}
	return modelName + "."
}

// PreflightBattery: built from cell count, volts, percent. Any
// component can be skipped by passing 0 for it. Returns "" if no
// component is present.
func PreflightBattery(lang string, cellCount int, volts float64, percent uint8) string {
	lang = resolveLang(lang)
	if cellCount <= 0 && volts <= 0 && percent == 0 {
		return ""
	}
	frag := "Battery"
	cellWord := "cell"
	if lang == "pt" {
		frag = "Bateria"
		cellWord = "células"
	}
	if cellCount > 0 {
		frag += fmt.Sprintf(" %d %s", cellCount, cellWord)
	}
	if volts > 0 {
		if lang == "pt" {
			frag += fmt.Sprintf(", %.1f volts", volts)
		} else {
			frag += fmt.Sprintf(", %.1f volts", volts)
		}
	}
	if percent > 0 {
		if lang == "pt" {
			frag += fmt.Sprintf(", %d por cento", percent)
		} else {
			frag += fmt.Sprintf(", %d percent", percent)
		}
	}
	return frag + "."
}

// PreflightGPS: sat count + lock state.
func PreflightGPS(lang string, sats int, lock bool) string {
	lang = resolveLang(lang)
	if lang == "pt" {
		state := "sem fix"
		if lock {
			state = "GPS fixado"
		}
		return fmt.Sprintf("%d satélites, %s.", sats, state)
	}
	state := "no lock"
	if lock {
		state = "GPS lock"
	}
	return fmt.Sprintf("%d satellites, %s.", sats, state)
}

// PreflightLink: link quality percent.
func PreflightLink(lang string, lqPercent int) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Link %d por cento.", lqPercent)
	}
	return fmt.Sprintf("Link %d percent.", lqPercent)
}

// PreflightReady: closing phrase.
func PreflightReady(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Pronto para armar."
	}
	return "Ready to arm."
}

// === Post-flight summary ===

func PostFlightHeader(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Voo concluído."
	}
	return "Flight complete."
}

// DurationSentence wraps a duration value into a sentence-ending
// fragment ("4 minutes 12 seconds.").
func DurationSentence(lang string, sec int) string {
	return DurationPhrase(lang, sec) + "."
}

// DurationPhrase produces "X minutes Y seconds" / "X seconds" in the
// given language. No trailing period; callers add it if needed.
func DurationPhrase(lang string, sec int) string {
	lang = resolveLang(lang)
	if sec < 60 {
		return secondsPhrase(lang, sec)
	}
	mins := sec / 60
	rem := sec % 60
	out := minutesPhrase(lang, mins)
	if rem > 0 {
		out += " " + secondsPhrase(lang, rem)
	}
	return out
}

func minutesPhrase(lang string, n int) string {
	if lang == "pt" {
		if n == 1 {
			return "1 minuto"
		}
		return fmt.Sprintf("%d minutos", n)
	}
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}

func secondsPhrase(lang string, n int) string {
	if lang == "pt" {
		if n == 1 {
			return "1 segundo"
		}
		return fmt.Sprintf("%d segundos", n)
	}
	if n == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", n)
}

func PeakDistance(lang string, meters int64) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Distância máxima %d metros.", meters)
	}
	return fmt.Sprintf("Peak distance %d meters.", meters)
}

// PeakDistanceAt is like PeakDistance with an optional location
// clause. When place is empty it returns the same string as
// PeakDistance. When non-empty, it appends a localized "near $place"
// before the trailing period.
//
//	PeakDistanceAt("en", 1200, "Vila Industrial")
//	  -> "Peak distance 1200 meters near Vila Industrial."
//	PeakDistanceAt("pt", 1200, "Vila Industrial")
//	  -> "Distância máxima 1200 metros perto de Vila Industrial."
func PeakDistanceAt(lang string, meters int64, place string) string {
	if place == "" {
		return PeakDistance(lang, meters)
	}
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Distância máxima %d metros perto de %s.", meters, place)
	}
	return fmt.Sprintf("Peak distance %d meters near %s.", meters, place)
}

func PeakAltitude(lang string, meters int64) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Altitude máxima %d metros.", meters)
	}
	return fmt.Sprintf("Peak altitude %d meters.", meters)
}

// PeakAltitudeAt: see PeakDistanceAt for semantics.
func PeakAltitudeAt(lang string, meters int64, place string) string {
	if place == "" {
		return PeakAltitude(lang, meters)
	}
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Altitude máxima %d metros sobre %s.", meters, place)
	}
	return fmt.Sprintf("Peak altitude %d meters over %s.", meters, place)
}

func FailsafeTriggered(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Failsafe acionado."
	}
	return "Failsafe triggered."
}

func RTHTriggered(lang string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Retorno para casa acionado."
	}
	return "Return to home triggered."
}

func BatteryCriticalAt(lang string, sec int) string {
	switch resolveLang(lang) {
	case "pt":
		return "Bateria crítica aos " + DurationPhrase(lang, sec) + "."
	}
	return "Battery critical at " + DurationPhrase(lang, sec) + "."
}

func BatteryLowAt(lang string, sec int) string {
	switch resolveLang(lang) {
	case "pt":
		return "Bateria baixa aos " + DurationPhrase(lang, sec) + "."
	}
	return "Battery low at " + DurationPhrase(lang, sec) + "."
}

// === Periodic in-flight status ===

// PeriodicBattery: percent + volts. Either may be zero (skip).
// Returns "" when both are zero.
func PeriodicBattery(lang string, percent uint8, volts float64) string {
	lang = resolveLang(lang)
	if percent == 0 && volts <= 0 {
		return ""
	}
	frag := "Battery"
	if lang == "pt" {
		frag = "Bateria"
	}
	if percent > 0 {
		if lang == "pt" {
			frag += fmt.Sprintf(" %d por cento", percent)
		} else {
			frag += fmt.Sprintf(" %d percent", percent)
		}
	}
	if volts > 0 {
		frag += fmt.Sprintf(", %.1f volts", volts)
	}
	return frag + "."
}

func PeriodicDistance(lang string, meters int32) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Distância %d metros.", meters)
	}
	return fmt.Sprintf("Distance %d meters.", meters)
}

func PeriodicAltitude(lang string, meters int32) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Altitude %d metros.", meters)
	}
	return fmt.Sprintf("Altitude %d meters.", meters)
}

func PeriodicSpeed(lang string, kmh int) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Velocidade %d quilômetros por hora.", kmh)
	}
	return fmt.Sprintf("Speed %d kilometers per hour.", kmh)
}

func PeriodicLink(lang string, lqPercent uint8) string {
	switch resolveLang(lang) {
	case "pt":
		return fmt.Sprintf("Link %d por cento.", lqPercent)
	}
	return fmt.Sprintf("Link %d percent.", lqPercent)
}

func PeriodicMode(lang, fcMode string) string {
	switch resolveLang(lang) {
	case "pt":
		return "Modo " + HumanizeMode(lang, fcMode) + "."
	}
	return "Mode " + HumanizeMode(lang, fcMode) + "."
}

func PeriodicTimeAloft(lang string, sec int) string {
	switch resolveLang(lang) {
	case "pt":
		return "No ar há " + DurationPhrase(lang, sec) + "."
	}
	return "Aloft " + DurationPhrase(lang, sec) + "."
}
