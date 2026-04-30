# Audio bank attribution and licensing

The ZeroTX audio bank is generated from the dictionary at `dictionary.yml`
using **ElevenLabs** TTS via their REST API. Synthesis happens at build
time on the operator's machine; generated audio is stored locally in
`sounds/<bank>/`.

## Voices used

| Bank          | Voice            | Provenance                                |
| ------------- | ---------------- | ----------------------------------------- |
| `en`          | Mia              | ElevenLabs library voice                  |
| `pt`          | Larissa          | ElevenLabs library voice                  |
| `pt-adilson`  | Adilson (cloned) | Cloned from project owner's voice samples |
| `pt-pirate`   | Adilson (cloned) | Same clone as `pt-adilson`                |

## Licensing

Audio generated via the ElevenLabs API is owned by the user under their
Terms of Service. For personal and non-commercial hobby use, the generated
clips are yours to use, modify, and redistribute. Commercial distribution
of generated audio requires the appropriate ElevenLabs subscription tier;
ZeroTX as a hobby project doesn't trigger this, but anyone forking ZeroTX
for commercial purposes should re-read the current ElevenLabs TOS.

The cloned voice (`pt-adilson`, `pt-pirate`) is the project owner's own
voice. ElevenLabs voice cloning produces synthesis the trained user owns;
the cloned banks ship under the project's GPLv3 licence with the project
owner's consent.

If anyone ever wanted to distribute ZeroTX without an ElevenLabs dependency
(licence cleanliness, offline-only requirements, etc.), the audio bank can
be regenerated using one of:

- **Piper** (MIT-licensed, runs offline, lower quality)
- **eSpeak-ng** (GPLv3, robotic but truly free)
- **Hand-recorded** by a contributor who licences their voice under
  the project's terms

The dictionary system supports any of these — only the build script's
synthesis call changes. Lookup names, repeat policies, and the daemon's
audio package stay the same.

## Hand-recorded overrides

Anything in `overrides/<bank>/` is presumed to be the recorder's own voice
and licensed under the project's GPLv3 terms. By contributing overrides,
you licence your recording under the same.
