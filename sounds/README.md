# ZeroTX sound bank

Audio for in-flight alarms and announcements. Dictionary-driven, multi-bank,
generator-agnostic.

## Quick start

```sh
sudo apt install yq jq pipx
pipx install edge-tts        # the default synthesizer
pipx ensurepath

./scripts/build-sounds.sh    # generates sounds/en/ and sounds/pt/
```

That's it. The default synthesizer is **edge-tts** (Microsoft Edge's
free TTS, runs anywhere, no API key). The default voices are
`en-US-AvaNeural` for English and `pt-BR-FranciscaNeural` for Portuguese.

For higher quality, voice cloning, or other languages, swap the
synthesizer. See "Alternatives" below.

## What's here

- `dictionary.yml` : the **public dictionary**. Track names, spoken text
  per language, and bank declarations. Edit to change wording or add
  tracks. Committed to git
- `personal.yml.example` : template for `personal.yml` (gitignored).
  This is where private voice IDs, custom banks, cloned voices, and
  per-track personal text variants go
- `scripts/build-sounds.sh` (in repo root) : generator script. Reads
  the merged dictionary and synthesizes audio
- `<bank>/` : generated audio per bank. Not committed (gitignored)
- `overrides/<bank>/` : hand-recorded WAV/MP3/OGG files that override
  generated audio for specific tracks. Committed to git

## Banks

A "bank" is a complete set of audio for one voice. The daemon's
`-sounds-lang` flag selects which bank plays at runtime. The public
dictionary ships with `en` and `pt`. Add your own by creating
`sounds/personal.yml` with extra bank declarations.

## How banks merge

At build time the script reads `dictionary.yml` and, if it exists,
merges `personal.yml` over it. Rules:

- **Banks declared in `personal.yml` extend** the public list.
  Add new banks (cloned voice, joke variants, mood banks) without
  touching the public dictionary
- **Bank declarations in `personal.yml` override** the public ones
  by name. Replace the `en` voice without forking the dictionary
- **Track text in `personal.yml` extends** the public tracks per bank.
  Adds entries for new banks; overrides specific track/bank pairs
- **Empty text skips generation**. `pt-pirate: ""` doesn't try to
  synthesize, doesn't write a file. Lets you ship structural
  placeholders that get filled in iteratively

## Building the audio

```sh
./scripts/build-sounds.sh                  # all banks, incremental
./scripts/build-sounds.sh --force          # regenerate everything
./scripts/build-sounds.sh --bank en        # one bank only
./scripts/build-sounds.sh --track armed    # one track (across all banks)
./scripts/build-sounds.sh --dry-run        # preview without synthesizing
```

## Listening

```sh
mplayer sounds/en/armed.mp3
vlc --intf dummy --play-and-exit sounds/en/armed.mp3
```

(`mpv` is not in Ubuntu repos. `mplayer` works for everything;
`vlc` is the GUI alternative.)

## Audio file requirements (engine-agnostic)

The daemon's audio package looks up files at:

```
sounds/<bank>/<track>.<ext>
```

It accepts `.mp3`, `.wav`, and `.ogg` extensions. Required properties:

- **Mono** audio (stereo wastes space and the radio output is mono anyway)
- **22 kHz sample rate or higher** (16 kHz is the minimum that doesn't
  sound tinny for speech; 22 kHz is the standard for voice prompts)
- **Reasonably small** (under ~100 KB per clip; longer clips delay the
  audio queue)
- Any bitrate. Speech compression is forgiving

If you produce audio with these properties using any tool, the daemon
plays it. Replace the synthesizer, hand-record clips, mix from voice
samples. It doesn't care.

## Alternatives to edge-tts

The default `synthesize()` function in `scripts/build-sounds.sh` is the
swappable section. Replace its body to use a different engine.

| Engine | Quality | Cost | Offline | Code-switching | Voice cloning |
| ------ | ------- | ---- | ------- | -------------- | ------------- |
| **edge-tts** (default) | Decent | Free | No | Mixed | No |
| **ElevenLabs** | Excellent | Paid (Starter $6/mo) | No | Native | Yes |
| **Piper** | Decent | Free | Yes | Limited | No |
| **eSpeak-NG** | Robotic | Free | Yes | No | No |
| **Hand-recorded** | Personal | Free (your time) | Yes | Whatever you say | n/a |

### When to swap

- **Hitting code-switching issues** (loanwords like "failsafe" mispronounced
  in Portuguese): try ElevenLabs. The multilingual_v2 model handles mixed
  text natively
- **Need offline operation** (no network at the field): use Piper. The
  voices are smaller and worse than ElevenLabs but they run anywhere
- **Don't want a hosted dependency at all**: use eSpeak-NG. It will sound
  obviously synthetic but that's a deliberate aesthetic choice some people
  prefer
- **Want a personal touch**: hand-record selected tracks into
  `sounds/overrides/<bank>/<track>.wav`. The build script merges overrides
  on top of generated audio per pass

### Swapping example: ElevenLabs

Replace the body of `synthesize()` in `scripts/build-sounds.sh` with:

```sh
synthesize() {
  local bank="$1" voice_id="$2" model="$3" text="$4" outfile="$5"

  if [[ -z "$voice_id" || "$voice_id" == REPLACE_* ]]; then
    echo "no voice_id for bank $bank" >&2
    return 1
  fi

  local payload http_code
  payload="$(jq -nc --arg text "$text" --arg model "${model:-eleven_multilingual_v2}" \
    '{text: $text, model_id: $model}')"

  http_code="$(curl -sS -X POST \
    "https://api.elevenlabs.io/v1/text-to-speech/${voice_id}?output_format=mp3_44100_128" \
    -H "xi-api-key: $ELEVENLABS_API_KEY" \
    -H "Content-Type: application/json" \
    -d "$payload" \
    -o "$outfile" \
    -w "%{http_code}")"

  [[ "$http_code" == "200" ]]
}
```

Then `export ELEVENLABS_API_KEY=...` and run the build script. ZeroTX
was initially developed using this engine. The voices are notably
better than free alternatives, especially for cloned voices.

## Pointing the daemon at a bank

```sh
zerotxd \
  -sounds-dir /path/to/zerotx/sounds \
  -sounds-lang pt \
  -audio-threshold notice
```

`-sounds-lang` accepts any bank name from your merged dictionary
(public or personal).

## Hand-recorded overrides

Drop WAV/MP3/OGG files in `sounds/overrides/<bank>/<track>.<ext>`. The
build script copies them over the generated audio at the end of each
bank pass. Use this for safety-critical tracks (`armed`, `failsafe`)
where you want a specific voice or recording without regenerating
the whole bank.

```sh
mkdir -p sounds/overrides/en
arecord -f cd -r 22050 -c 1 -d 3 sounds/overrides/en/armed.wav
./scripts/build-sounds.sh --bank en --track armed
```

## Stitched announcements

When the daemon receives a `Play(track)` for a track that doesn't have
a whole-phrase recording, it falls back to **stitching**. looking up
a curated decomposition into building blocks (e.g. `bat-low` ->
`w-battery` + `low`) and playing them in sequence with an 80ms
inter-fragment gap.

The dictionary ships building blocks for:

- Numbers: `n-0` through `n-30`, decades to `n-90`, hundreds to
  `n-900`, thousands to `n-9000`
- Units: `u-volts`, `u-meters`, `u-kilometers`, `u-percent`, `u-minutes`,
  `u-seconds`, etc.
- Composite words: `w-battery`, `w-altitude`, `w-link`, etc.
- Connectives: `c-and`, `c-at`, `c-of`

This means stitched values like "battery twelve point four volts" or
"three kilometers four hundred meters" work without explicit
whole-phrase recordings. See the daemon's `audio/decompositions.go`
for the curated decomposition map.

## License notes

See `ATTRIBUTION.md` for licensing details on the public bank's
spoken content and the synthesizer engines.
