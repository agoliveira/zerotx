// protocol.cpp - implementation of the line-oriented protocol.

#include "protocol.h"

#include <string.h>

namespace proto {

void LineReader::resetBuffer() {
  used_ = 0;
  overflow_ = false;
}

bool LineReader::poll(Command& cmd) {
  while (in_.available() > 0) {
    int c = in_.read();
    if (c < 0) break;

    // Newline ends the line. We accept both \n and \r\n; \r alone is
    // treated as harmless filler.
    if (c == '\n') {
      // Null-terminate. If we overflowed, just discard and report.
      if (overflow_) {
        writeError(in_, "protocol", "line-too-long");
        resetBuffer();
        continue;
      }
      buf_[used_] = '\0';
      bool ok = tokenize(cmd);
      resetBuffer();
      if (ok) return true;
      // Skip blank/comment lines silently; on parse failures with
      // content, tokenize() emits its own error.
      continue;
    }
    if (c == '\r') {
      continue;  // ignore CR
    }

    if (overflow_) {
      // Already past max; just consume bytes until newline.
      continue;
    }
    if (used_ + 1 >= MAX_LINE) {
      overflow_ = true;
      continue;
    }
    buf_[used_++] = static_cast<char>(c);
  }
  return false;
}

bool LineReader::tokenize(Command& cmd) {
  cmd.tokenCount = 0;

  // Strip leading whitespace. Skip blanks and comments outright.
  char* p = buf_;
  while (*p == ' ' || *p == '\t') p++;
  if (*p == '\0' || *p == '#') return false;

  // Walk tokens, replacing whitespace with NUL terminators in place.
  // The Command's token pointers reference the buffer directly.
  while (*p != '\0' && cmd.tokenCount < MAX_TOKENS) {
    cmd.tokens[cmd.tokenCount++] = p;
    while (*p != '\0' && *p != ' ' && *p != '\t') p++;
    if (*p != '\0') {
      *p++ = '\0';
      while (*p == ' ' || *p == '\t') p++;
    }
  }

  // Excess tokens get silently truncated at MAX_TOKENS; not worth
  // erroring since a misuse here is a bug in the daemon, not a
  // user-typing scenario.
  return cmd.tokenCount > 0;
}

void writeResponse(Stream& out, const char* body) {
  out.print("> ");
  out.print(body);
  out.print('\n');
}

void writeError(Stream& out, const char* subsystem, const char* message) {
  out.print("! ");
  out.print(subsystem);
  out.print(' ');
  out.print(message);
  out.print('\n');
}

void writeEvent(Stream& out, const char* subsystem, const char* args) {
  out.print("EVENT ");
  out.print(subsystem);
  if (args && *args) {
    out.print(' ');
    out.print(args);
  }
  out.print('\n');
}

}  // namespace proto
