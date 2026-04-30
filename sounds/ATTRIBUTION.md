# Audio bank attribution and licensing

The ZeroTX audio bank is generated from the dictionary at
`dictionary.yml` using Microsoft Edge TTS — the neural text-to-speech
service exposed via the Microsoft Edge browser. The synthesis happens
on the user's machine via the `edge-tts` Python CLI.

## Voices used

- **en**: `en-US-AvaNeural` — Microsoft Azure neural voice "Ava"
- **pt**: `pt-BR-FranciscaNeural` — Microsoft Azure neural voice "Francisca"

## Licensing

Microsoft Edge TTS doesn't publish a redistribution licence for the
voice models themselves; they're a service. The generated *output*
audio falls in a gray zone: Microsoft has not historically pursued
hobbyist or open-source projects that ship Edge TTS-generated audio,
and many similar projects do so. The pragmatic position taken here is
that ZeroTX is a non-commercial hobby project, the audio is incidental
to the project's purpose, and re-generation on any contributor's
machine is one command.

If anyone ever wanted to commercialise ZeroTX or distribute it through
channels with stricter licensing requirements, the audio bank should be
regenerated using one of:

- **Piper** (MIT-licensed, runs offline, lower quality)
- **eSpeak-ng** (GPLv3, robotic but truly free)
- **Hand-recorded** by a contributor who licences their voice under the
  project's terms

The dictionary system supports either path — all that changes is the
generator. The lookup names, repeat policies, and audio package stay
the same.

## Hand-recorded overrides

Anything in `overrides/<lang>/` is presumed to be the recorder's own
voice and licensed under the project's terms (GPLv3, matching the
rest of ZeroTX). Contributors who add overrides implicitly licence
their recordings under the same.
