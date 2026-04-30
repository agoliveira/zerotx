# ZeroTX sound bank

Audio for in-flight alarms and announcements. Dictionary-driven, generated on
demand, voice-agnostic.

## What's here

- `dictionary.yml` — track names + spoken text per language. Edit this to
  change wording or add new tracks
- `scripts/build-sounds.sh` (in repo root, not here) — generator script
- `<lang>/` — generated MP3s. Not committed; created by the build script
- `overrides/<lang>/` — hand-recorded WAV/MP3 files that override the
  generated audio for specific tracks. Place a file with the same name as
  a track and it takes precedence

## Building the audio bank

One-time install on Ubuntu:

```sh
sudo apt install pipx yq
pipx ensurepath
pipx install edge-tts
```

Generate everything:

```sh
./scripts/build-sounds.sh
```

That populates `sounds/en/` and `sounds/pt/` with one MP3 per dictionary entry.
On a typical machine the full bank generates in a few minutes.

Incremental — re-run after editing `dictionary.yml`, only changed entries
regenerate:

```sh
./scripts/build-sounds.sh
```

Force regenerate everything (after voice change, etc.):

```sh
./scripts/build-sounds.sh --force
```

One language only:

```sh
./scripts/build-sounds.sh --lang en
```

One track only:

```sh
./scripts/build-sounds.sh --track armed
```

## Voices

Default voices are configured in `dictionary.yml`:

```yaml
voices:
  en: en-US-AvaNeural
  pt: pt-BR-FranciscaNeural
```

To pick different voices:

```sh
edge-tts --list-voices | grep en-US
edge-tts --voice en-US-VoiceName --text "test" --write-media test.mp3
mpv test.mp3
```

Edit `dictionary.yml`, then re-run with `--force`.

## Hand-recorded overrides

To replace a generated track with your own recording:

```sh
# Record bat-low in your own voice
arecord -f cd -r 22050 -c 1 -d 3 sounds/overrides/en/bat-low.wav
./scripts/build-sounds.sh --track bat-low
```

The build script copies overrides into the language dir at the end of each
pass, so the override always wins.

## Pointing the daemon at the bank

```sh
zerotxd -sounds-dir /path/to/zerotx/sounds -sounds-lang en -audio-threshold notice
```

Sample lookup is `<sounds-dir>/<lang>/<track>.<ext>` with fallback to
`<sounds-dir>/<track>.<ext>` for language-neutral sounds. The daemon's audio
package tries `.mp3`, `.wav`, `.ogg` extensions in that order.

## Why MP3, not WAV

Edge TTS produces MP3 natively. paplay and aplay both handle MP3 on modern
Ubuntu/Debian without additional packages. Files are smaller. If a specific
clip causes problems, drop a WAV override.

## Why not commit the audio?

The dictionary churns frequently in early development. Committing 150+ MP3s
that immediately go stale on the next dictionary tweak isn't useful, and the
extra repo size matters when cloning over slow links. Once the dictionary
stabilises this might change.

Anyone with `edge-tts` and `yq` installed can regenerate the bank in minutes.

## License note

See `ATTRIBUTION.md` in this directory for voice model attribution and
licensing details.
