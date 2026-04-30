# Audio bank attribution and licensing

The ZeroTX public sound bank is generated from `dictionary.yml` using a
swappable synthesizer. The default is **edge-tts** (free, no API key,
runs via Microsoft's Edge TTS service); alternatives including
ElevenLabs, Piper, and eSpeak-NG are documented in `README.md`.

## What's committed to git

- The dictionary (`dictionary.yml`) : track names and spoken text in en
  and pt. The wording is generic FPV/RC vocabulary
- The build script (`scripts/build-sounds.sh`)
- This file
- README and personal.yml.example

## What's NOT committed

- Generated audio (`sounds/<bank>/`) : gitignored. Anyone with the
  default synthesizer regenerates it locally
- Personal config (`sounds/personal.yml`) : gitignored. Voice IDs,
  cloned voices, and personal text variants live here
- Hand-recorded overrides (under `overrides/<bank>/`) : gitignored
  by default. Operators can selectively commit specific overrides
  if they want to share them

## Licensing of generated audio

Licensing depends on the synthesizer used:

- **edge-tts** uses Microsoft's free Edge TTS endpoint. There's no
  formal redistribution license for the generated audio; the pragmatic
  position taken by hobbyist projects is that audio generated for
  personal use is fine. For commercial distribution, a proper license
  is needed
- **ElevenLabs** generated audio is owned by the user under their
  Terms of Service. Starter tier ($6/month and up) includes a
  commercial license. Cloned voices remain the user's
- **Piper, eSpeak-NG** are open-source (MIT and GPLv3 respectively);
  generated audio is unencumbered
- **Hand-recorded** audio is licensed by the recorder; contributors
  to ZeroTX overrides license under the project's GPLv3 by submitting

## Cloned voices

If you use a cloned voice via ElevenLabs (or another voice cloning
service), the cloned voice references your account's voices. Treat
voice IDs as semi-sensitive. Anyone with the ID and your API key can
synthesize using your clone. This is why `personal.yml` is gitignored
and `personal.yml.example` uses placeholder strings.

## Spoken content licensing

The dictionary's en and pt text is original wording for standard FPV/RC
events. It's licensed under the project's GPLv2-or-later license like
the rest of ZeroTX. Translations or wording suggestions submitted as
PRs are accepted under the same terms.
