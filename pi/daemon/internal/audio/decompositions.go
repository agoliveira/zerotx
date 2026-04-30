package audio

// Decomposition table for stitched audio fallback.
//
// When the whole-phrase lookup for a track name fails (e.g.
// `bat-low.mp3` doesn't exist in the sound bank), the player tries
// decomposing the name into a sequence of building-block names that
// might exist individually. Each block is then resolved and played
// in order with a small inter-fragment gap.
//
// The mapping is hand-curated, not algorithmic. Splitting on hyphens
// would produce wrong results: `fm-acr` is one concept (Acro flight
// mode) but `bat-low` really is `battery` + `low`. The map captures
// the intent explicitly.
//
// To add a stitched fallback for a new compound name:
//   1. Add the entry to the map below
//   2. Make sure each fragment is itself a track name in the
//      dictionary (typically a `w-*` word, `n-*` number, `u-*` unit,
//      or other building-block prefix)
//
// If a fragment is missing at runtime, the player logs and continues
// with the rest of the sequence. Partial stitches are better than
// silence.

// decomposition holds the canonical compound→fragments mapping.
var decomposition = map[string][]string{
	// Battery family. EdgeTX-equivalent names; whole-phrase versions
	// can ship in the bank, but the stitched fallback covers the case
	// where they're missing or where future events need composition.
	"bat-low":          {"w-battery", "low"},
	"bat-critical":     {"w-battery", "critical"},
	"bat-empty":        {"w-battery", "empty"},
	"bat-cell-warning": {"w-battery", "cell", "warning"},

	// GPS family.
	"gps-lock":   {"w-gps", "lock"},
	"gps-lost":   {"w-gps", "lost"},
	"gps-fix-3d": {"w-gps", "w-fix"},
	"gps-glitch": {"w-gps", "glitch"},
	"home-set":   {"home", "set"},

	// Link / RF family.
	"link-lost":     {"w-link", "lost"},
	"link-restored": {"w-link", "restored"},

	// RTH / mission.
	"rth":            {"return", "to", "home"},
	"rth-cancelled":  {"return", "cancelled"},
	"arrived-home":   {"arrived", "home"},
	"mission-start":  {"mission", "started"},
	"mission-end":    {"mission", "complete"},

	// Pre-flight gates.
	"joystick-connected":    {"joystick", "connected"},
	"joystick-disconnected": {"joystick", "disconnected"},
	"model-loaded":          {"model", "loaded"},
	"model-unloaded":        {"model", "unloaded"},
	"arming-blocked":        {"arming", "blocked"},

	// Flight modes — INAV. Treated as single concepts (no decomposition).
	// Each `fm-*` is its own indivisible track. We deliberately don't
	// decompose them: "altitude hold" stitched as ["altitude", "hold"]
	// would be slower and would sound stilted compared to the single
	// `fm-althold` recording. They live here as documentation of the
	// design choice, NOT as decomposition entries.

	// Audio threshold transitions.
	"threshold-info":     {"all", "sounds", "enabled"},
	"threshold-notice":   {"notice", "level"},
	"threshold-warning":  {"warnings", "only"},
	"threshold-critical": {"critical", "only"},

	// System events.
	"daemon-started":       {"zerotx", "ready"},
	"daemon-shutting-down": {"shutting", "down"},
	"recording-saved":      {"recording", "saved"},
	"landing-required":     {"landing", "required"},
	"power-on":             {"powered", "on"},
}

// decompose returns the fragment list for a compound track name, or
// (nil, false) if no decomposition is registered. Used by the player
// as a fallback when whole-phrase lookup fails.
func decompose(name string) ([]string, bool) {
	parts, ok := decomposition[name]
	if !ok {
		return nil, false
	}
	// Defensive copy so callers can't mutate the table.
	out := make([]string, len(parts))
	copy(out, parts)
	return out, true
}
