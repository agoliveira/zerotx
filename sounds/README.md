# ZeroTX sound bank

Audio for in-flight alarms and announcements. Multi-bank, ElevenLabs-driven,
voice-flexible.

## Banks

A "bank" is a complete set of audio for one voice. The daemon's `-sounds-lang`
flag selects which bank plays at runtime. Round 1 ships four banks:

| Bank          | Description                                                |
| ------------- | ---------------------------------------------------------- |
| `en`          | Professional English (Mia)                                 |
| `pt`          | Professional Brazilian Portuguese (Larissa)                |
| `pt-adilson`  | Cloned voice of project owner, neutral register            |
| `pt-pirate`   | Cloned voice of project owner, profane register (joke bank)|

Adding more banks (different language, different mood, different speaker)
is just a YAML edit — see "Adding a new bank" below.

## What's here

- `dictionary.yml` — bank definitions (voice IDs, models) plus per-bank
  spoken text for each track. Edit this to change wording, voices, or banks
- `scripts/build-sounds.sh` (in repo root, not here) — generator script
- `<bank>/` — generated MP3s per bank. Not committed; created by the build
  script
- `overrides/<bank>/` — hand-recorded WAV/MP3 files that override generated
  audio for specific tracks. Place a file with the same name as a track and
  it takes precedence

## Building the audio bank

One-time install on Ubuntu:

```sh
sudo apt install yq jq curl
```

Get an API key from <https://elevenlabs.io/app/settings/api-keys> and export
it (add to `~/.bashrc` or `~/.profile` to persist):

```sh
export ELEVENLABS_API_KEY='sk_...'
```

Generate everything:

```sh
./scripts/build-sounds.sh
```

That populates `sounds/<bank>/` for every bank with non-empty text. On a
typical connection the full bank generates in a few minutes. The pt-pirate
bank starts empty — entries are placeholders for the project owner to fill
in over time.

Incremental — re-run after editing `dictionary.yml`, only changed entries
regenerate:

```sh
./scripts/build-sounds.sh
```

Force regenerate everything:

```sh
./scripts/build-sounds.sh --force
```

One bank only:

```sh
./scripts/build-sounds.sh --bank pt
```

One track only (across all banks):

```sh
./scripts/build-sounds.sh --track armed
```

One track, one bank:

```sh
./scripts/build-sounds.sh --bank pt-pirate --track armed
```

Dry run — show what would be generated without making API calls:

```sh
./scripts/build-sounds.sh --dry-run
```

## Filling in the pt-pirate bank

The pt-pirate bank ships with empty entries clearly marked `# FILL IN` in
`dictionary.yml`. The dictionary also annotates each track with register
hints in comments — guidance like `[CRIT-REPEAT]` (repeats every 5s, keep
SHORT) versus `[ONCE]` (plays once, full personality budget). Use these
to budget length and energy when writing.

Iterate one track at a time:

```sh
# Edit dictionary.yml, fill in pt-pirate for track "armed"
$EDITOR sounds/dictionary.yml

# Generate just that one
./scripts/build-sounds.sh --bank pt-pirate --track armed

# Listen
mpv sounds/pt-pirate/armed.mp3

# Refine, regenerate (need --force since file now exists)
./scripts/build-sounds.sh --bank pt-pirate --track armed --force
```

When you have a batch ready, drop `--track` and it processes everything
non-empty in the bank.

## Adding a new bank

Edit `dictionary.yml`, add a new entry under `banks:`:

```yaml
banks:
  en-curse:
    voice_id: "YOUR_VOICE_ID"
    model: eleven_multilingual_v2
    description: "English profane bank"
```

Then add `en-curse:` text to every track you want filled (empty text or
omitted entries skip cleanly). Run the build with `--bank en-curse`.

## Pointing the daemon at a bank

```sh
zerotxd \
  -sounds-dir /path/to/zerotx/sounds \
  -sounds-lang pt-pirate \
  -audio-threshold notice
```

Sample lookup is `<sounds-dir>/<bank>/<track>.<ext>` with fallback to
`<sounds-dir>/<track>.<ext>` for language-neutral sounds. Extensions tried:
`.mp3`, `.wav`, `.ogg`.

## Hand-recorded overrides

To replace a generated track with your own recording:

```sh
mkdir -p sounds/overrides/pt-adilson
arecord -f cd -r 22050 -c 1 -d 3 sounds/overrides/pt-adilson/armed.wav
./scripts/build-sounds.sh --bank pt-adilson --track armed
```

The build script copies overrides into the bank dir at the end of each pass,
so the override always wins regardless of whether ElevenLabs ran for that
track or not.

## Why MP3, not WAV

The ElevenLabs API returns MP3 natively at the format we request
(`mp3_44100_128`). paplay and aplay both handle MP3 on modern Ubuntu/Debian
without additional packages. Files are smaller. If a specific clip causes
problems, drop a WAV override.

## Why not commit the audio?

Voice IDs and dictionary text churn during development. Committing 300+ MP3s
that immediately go stale on the next dictionary tweak isn't useful, and the
extra repo size matters when cloning over slow links. Once the dictionary
stabilises this might change.

Anyone with an ElevenLabs API key can regenerate the bank in minutes.

## License note

See `ATTRIBUTION.md` in this directory for voice provenance and licensing
details.
