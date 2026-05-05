// protocol.h - line-oriented text protocol over Serial0 (USB-CDC).
//
// All traffic is text, newline-terminated. Three verbs:
//
//   SET <subsystem>[.<instance>] <param> [args...]
//   GET <subsystem>[.<instance>] [<param>]
//   EVENT <subsystem>[.<instance>] [args...]   (firmware -> daemon only)
//
// Examples:
//   SET led.trackball red-blink
//   SET vfd.0 mode armed
//   SET vfd.0 brightness 2
//   SET buzzer beep 1000 200
//   GET caps
//   GET version
//   EVENT button.0 down
//   EVENT sensor.ldr 423
//   EVENT boot watchdog
//
// Responses to GET commands look like the request echoed with values:
//   GET caps                -> > caps vfd.0 led.trackball ws.0 sensor.ldr ...
//   GET sensor.ldr          -> > sensor.ldr 423
//
// Errors are emitted as "! <subsystem> <message>":
//   SET vfd.99 mode armed   -> ! vfd.99 unknown-instance
//   SET led.trackball wat   -> ! led.trackball invalid-state
//
// Lines starting with '#' are comments and ignored.

#ifndef ZEROTX_IO_PROTOCOL_H
#define ZEROTX_IO_PROTOCOL_H

#include <Arduino.h>

namespace proto {

// Maximum line length the parser will accept. Lines longer than this
// are dropped on the floor (the firmware emits an error, then resets
// its line buffer).
constexpr size_t MAX_LINE = 128;

// Maximum number of whitespace-separated tokens per line. Must be
// big enough for the most arg-heavy command we have, with headroom.
constexpr size_t MAX_TOKENS = 8;

// A parsed command. Tokens point into a private buffer owned by the
// LineReader; consumers must finish using them before the next
// readLine() call.
struct Command {
  uint8_t      tokenCount = 0;
  const char*  tokens[MAX_TOKENS] = {};

  // Convenience accessors. Returns nullptr if the index is out of
  // range so callers can write idiomatic null-checks.
  const char* verb() const      { return token(0); }
  const char* target() const    { return token(1); }
  const char* param() const     { return token(2); }
  const char* arg(size_t i) const { return token(3 + i); }

  const char* token(size_t i) const {
    return i < tokenCount ? tokens[i] : nullptr;
  }
};

// Reads one line from the given Stream into an internal buffer.
// Returns true when a full line is available in `cmd`. Returns false
// if no line is ready yet (call again next loop iteration).
//
// The reader handles both '\n' and '\r\n' terminators, ignores blank
// lines and comment lines, and emits an error if a line exceeds
// MAX_LINE.
class LineReader {
public:
  LineReader(Stream& in) : in_(in) {}

  // Try to read one complete line. Returns true if cmd was filled in.
  bool poll(Command& cmd);

private:
  Stream& in_;
  char    buf_[MAX_LINE];
  size_t  used_ = 0;
  bool    overflow_ = false;

  void resetBuffer();
  bool tokenize(Command& cmd);
};

// Helper: write a response line. Caller composes the full body
// without leading prefix; this writes "> body\n" to the stream.
void writeResponse(Stream& out, const char* body);

// Helper: write an error line. "! subsystem message\n".
void writeError(Stream& out, const char* subsystem, const char* message);

// Helper: write an event line. "EVENT subsystem args...\n".
// The variadic helper is the common case; integer/string append is
// done by callers using snprintf into a stack buffer.
void writeEvent(Stream& out, const char* subsystem, const char* args);

}  // namespace proto

#endif  // ZEROTX_IO_PROTOCOL_H
